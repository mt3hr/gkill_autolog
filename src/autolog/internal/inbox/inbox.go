// Package inbox は共有ディレクトリに置かれた JSONL の生ログを raw.db へ取り込む。
//
// Android では収集アプリと autolog が別のアプリで、共有ストレージ (/sdcard) 経由でしか
// 受け渡せない。共有ストレージは FUSE で、複数プロセスから SQLite を開くと
// ファイルロックが期待どおりに効かない。そこでアプリ側は追記専用の JSONL を書き、
// autolog がそれを読んで自分の raw.db へ入れる。
//
// アプリは書きかけを .jsonl.tmp に作り、書き終えてから .jsonl へ rename する。
// autolog は .jsonl だけを読むので、途中まで書かれた行を読むことがない。
package inbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// FileExtension は取り込み対象のファイル。
const FileExtension = ".jsonl"

// maxLineBytes は1行の上限。通知本文やウィンドウタイトルが入るが、
// 1行が極端に長い場合は壊れたファイルとみなす。
const maxLineBytes = 1 << 20

// Stats は取り込み結果。
type Stats struct {
	// Files は読んだファイル数。
	Files int
	// Read は読めたイベント数。
	Read int
	// Inserted は新しく入った件数。すでにあったものは含まない。
	Inserted int
	// Skipped は端末名や書式が不正で入れなかった件数。
	Skipped int
}

// Options は取り込みの設定。
type Options struct {
	// Dir は JSONL の置き場。
	Dir string
	// AllowedDevices は受け付ける端末名。空ならどの端末でも受け付ける。
	AllowedDevices []rawlog.Device
	// DefaultDevice は device が空のイベントに補う端末名。
	DefaultDevice rawlog.Device
	// KeepFiles が true なら取り込んだファイルを消さない。検証用。
	KeepFiles bool
}

// Ingest は Dir の JSONL をすべて raw.db へ取り込む。
//
// 取り込めたファイルは消す。raw_event は (device, event_id) で重複を弾くので、
// 消す前に落ちて同じファイルを二度読んでも二重登録にならない。
func Ingest(ctx context.Context, store *rawlog.Store, opts Options, logger *slog.Logger) (Stats, error) {
	var stats Stats
	if opts.Dir == "" {
		return stats, nil
	}

	entries, err := os.ReadDir(opts.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		// 収集アプリがまだ何も置いていないだけ。異常ではない。
		return stats, nil
	}
	if err != nil {
		return stats, fmt.Errorf("受け口ディレクトリを読めない %s: %w", opts.Dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), FileExtension) {
			continue
		}
		names = append(names, entry.Name())
	}
	// 古い順に入れる。raw_event の並びは start_time で決まるので順序は本質ではないが、
	// 途中で失敗したときに残るファイルを新しい方へ寄せておく。
	slices.Sort(names)

	for _, name := range names {
		path := filepath.Join(opts.Dir, name)

		events, read, skipped, err := readFile(path, opts, logger)
		if err != nil {
			// 1つ壊れていても他は取り込む。壊れたファイルは消さずに残す。
			logger.Error("受け口のファイルを読めなかった", "path", path, "error", err)
			continue
		}
		stats.Files++
		stats.Read += read
		stats.Skipped += skipped

		inserted, err := store.PutBatch(ctx, events)
		if err != nil {
			return stats, fmt.Errorf("%s の取り込みに失敗した: %w", path, err)
		}
		stats.Inserted += inserted

		if opts.KeepFiles {
			continue
		}
		if skipped > 0 {
			// 受け付けなかった行が残っている。ここで消すとその生ログが恒久に
			// 失われるので、ファイルは残す。原因（端末名の設定やスキーマ）を
			// 直せば次回の取り込みで残りも入り、そのとき消える。
			// 取り込み済みの行は (device, event_id) の重複として弾かれる。
			logger.Warn("受け付けなかった行があるためファイルを残す",
				"path", path, "skipped", skipped)
			continue
		}
		if err := os.Remove(path); err != nil {
			// 消せなくても取り込みは済んでいる。次回は重複として弾かれる。
			logger.Warn("取り込んだファイルを消せなかった", "path", path, "error", err)
		}
	}

	return stats, nil
}

// readFile は1ファイルを読んでイベントに変換する。
func readFile(path string, opts Options, logger *slog.Logger) ([]*rawlog.Event, int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()

	var (
		events  []*rawlog.Event
		read    int
		skipped int
	)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		read++

		event := &rawlog.Event{}
		if err := json.Unmarshal([]byte(line), event); err != nil {
			logger.Warn("受け口の行を解釈できない", "path", path, "line", lineNo, "error", err)
			skipped++
			continue
		}
		if event.Device == "" {
			event.Device = opts.DefaultDevice
		}
		if err := validate(event, opts); err != nil {
			logger.Warn("受け口の行を受け付けない", "path", path, "line", lineNo, "error", err)
			skipped++
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, read, skipped, err
	}

	return events, read, skipped, nil
}

// validate は取り込んでよいイベントかを確かめる。
func validate(event *rawlog.Event, opts Options) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if len(opts.AllowedDevices) > 0 && !slices.Contains(opts.AllowedDevices, event.Device) {
		return fmt.Errorf("端末 %q は受け付けない", event.Device)
	}
	return nil
}
