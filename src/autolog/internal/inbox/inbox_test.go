package inbox

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// eventLine は1件分の JSONL 行を作る。
func eventLine(eventID, device string, at time.Time) string {
	return `{"schema_version":1,"event_id":"` + eventID + `","device":"` + device +
		`","event_type":"session","start_time":"` + rawlog.FormatTime(at) +
		`","captured_at":"` + rawlog.FormatTime(at) + `","payload":{"action":"unlock"}}`
}

func writeInboxFile(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	path := filepath.Join(dir, name)
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}

func openTestStore(t *testing.T) *rawlog.Store {
	t.Helper()
	store, err := rawlog.OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestIngestReadsAndRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	path := writeInboxFile(t, dir, "20260726-1.jsonl",
		eventLine("e1", "Phone", at),
		eventLine("e2", "Phone", at.Add(time.Minute)),
	)

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{
		Dir:            dir,
		AllowedDevices: []rawlog.Device{"Phone"},
		DefaultDevice:  "Phone",
	}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if stats.Files != 1 || stats.Read != 2 || stats.Inserted != 2 || stats.Skipped != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("取り込んだファイルが残っている")
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("raw.db の件数 = %d, want 2", len(events))
	}
}

// 同じイベントを二度取り込んでも増えないこと。
// 削除の直前に落ちて同じファイルを読み直しても壊れない前提のもと。
func TestIngestIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	store := openTestStore(t)

	for range 2 {
		writeInboxFile(t, dir, "batch.jsonl", eventLine("e1", "Phone", at))
		if _, err := Ingest(context.Background(), store, Options{
			Dir: dir, DefaultDevice: "Phone",
		}, quietLogger()); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("件数 = %d, want 1 (重複は弾かれるはず)", len(events))
	}
}

// 許可していない端末のイベントは入れない。
func TestIngestRejectsUnknownDevice(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	writeInboxFile(t, dir, "a.jsonl",
		eventLine("e1", "Phone", at),
		eventLine("e2", "UnknownPhone", at),
	)

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{
		Dir:            dir,
		AllowedDevices: []rawlog.Device{"Phone"},
		DefaultDevice:  "Phone",
	}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Inserted != 1 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Inserted=1 Skipped=1", stats)
	}
}

// 受け付けなかった行があるファイルは消さない。
// 消すとその行の生ログが恒久に失われる。原因を直せば取り込み直せること。
func TestIngestKeepsFileWithSkippedLines(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	path := writeInboxFile(t, dir, "a.jsonl",
		eventLine("e1", "Phone", at),
		eventLine("e2", "Tablet", at),
	)

	store := openTestStore(t)
	opts := Options{Dir: dir, AllowedDevices: []rawlog.Device{"Phone"}, DefaultDevice: "Phone"}
	if _, err := Ingest(context.Background(), store, opts, quietLogger()); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("受け付けなかった行があるのにファイルが消えた")
	}

	// 設定を直す（端末を許可する）と、残りが入ってファイルは消える。
	opts.AllowedDevices = []rawlog.Device{"Phone", "Tablet"}
	stats, err := Ingest(context.Background(), store, opts, quietLogger())
	if err != nil {
		t.Fatalf("Ingest(2回目): %v", err)
	}
	if stats.Inserted != 1 || stats.Skipped != 0 {
		t.Errorf("stats = %+v, want Inserted=1 Skipped=0 (取り込み済みの e1 は重複で弾かれる)", stats)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("全行を受け付けたのにファイルが残っている")
	}
}

// 壊れた行があっても他の行は取り込む。
func TestIngestSkipsBrokenLines(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	writeInboxFile(t, dir, "a.jsonl",
		eventLine("e1", "Phone", at),
		"{壊れた行",
		"",
		eventLine("e2", "Phone", at.Add(time.Second)),
	)

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: dir, DefaultDevice: "Phone"}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Inserted != 2 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Inserted=2 Skipped=1", stats)
	}
}

// 書きかけの .jsonl.tmp は読まない。
func TestIngestIgnoresTempFiles(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	tmpPath := writeInboxFile(t, dir, "writing.jsonl.tmp", eventLine("e1", "Phone", at))

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: dir, DefaultDevice: "Phone"}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 0 || stats.Inserted != 0 {
		t.Errorf("stats = %+v, want 何も取り込まない", stats)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Error("書きかけのファイルを消してしまった")
	}
}

// device が空なら、この端末の名前を補う。
func TestIngestFillsDefaultDevice(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	writeInboxFile(t, dir, "a.jsonl", eventLine("e1", "", at))

	store := openTestStore(t)
	if _, err := Ingest(context.Background(), store, Options{
		Dir: dir, DefaultDevice: "Phone",
	}, quietLogger()); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 || events[0].Device != "Phone" {
		t.Errorf("device が補われていない: %+v", events)
	}
}

// paddedEventLine は改行を除いた行全体がちょうど length バイトになるよう
// 通知本文を詰めた行を作る。詰め物は ASCII なので1文字=1バイト。
// maxLineBytes の境界検証に使う。
func paddedEventLine(t *testing.T, eventID string, at time.Time, length int) string {
	t.Helper()
	prefix := `{"schema_version":1,"event_id":"` + eventID + `","device":"Phone","event_type":"notification","start_time":"` +
		rawlog.FormatTime(at) + `","captured_at":"` + rawlog.FormatTime(at) + `","payload":{"app_label":"App","body":"`
	suffix := `"}}`
	pad := length - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("length %d が短すぎる (最低 %d バイト)", length, len(prefix)+len(suffix))
	}
	return prefix + strings.Repeat("a", pad) + suffix
}

// maxLineBytes を超える行があるファイルは壊れたものとして丸ごと残し、他のファイルは取り込む。
// 境界: 行の中身は改行を上限内に収める必要があるため maxLineBytes-1 バイトまで読める。
// maxLineBytes バイトに達すると改行がバッファに入らず読み取りが失敗する。
// 壊れたファイルを消すと生ログが恒久に失われるので、残ることも確かめる。
func TestIngestSkipsFileWithTooLongLine(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)

	// 名前順に取り込むので、壊れたファイルを先頭に置いて後続へ進むことを確かめる。
	tooLongPath := writeInboxFile(t, dir, "1-toolong.jsonl",
		paddedEventLine(t, "e-long", at, maxLineBytes))
	writeInboxFile(t, dir, "2-limit.jsonl",
		paddedEventLine(t, "e-limit", at.Add(time.Second), maxLineBytes-1))
	writeInboxFile(t, dir, "3-normal.jsonl",
		eventLine("e-normal", "Phone", at.Add(2*time.Second)))

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: dir, DefaultDevice: "Phone"}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// 壊れたファイルは Files に数えず、後続の2ファイルは取り込む。
	if stats.Files != 2 || stats.Inserted != 2 || stats.Skipped != 0 {
		t.Errorf("stats = %+v, want Files=2 Inserted=2 Skipped=0", stats)
	}
	if _, err := os.Stat(tooLongPath); err != nil {
		t.Error("壊れたファイルが消えた (生ログが失われる)")
	}

	// 上限ぎりぎりの行と通常の行が本当に入っていること。
	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	got := make(map[string]bool)
	for _, e := range events {
		got[e.EventID] = true
	}
	if len(events) != 2 || !got["e-limit"] || !got["e-normal"] {
		t.Errorf("raw.db の中身 = %v, want e-limit と e-normal", got)
	}

	// 取り込めた2ファイルは消え、壊れた1ファイルだけが残る。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "1-toolong.jsonl" {
		t.Errorf("残ったファイル = %v, want 1-toolong.jsonl のみ", entries)
	}
}

// KeepFiles=true (取り込み内容の検証用) ではファイルを消さないこと。
// raw.db への追記自体は行われるので、繰り返しても (device, event_id) の重複で弾かれ、
// KeepFiles を外して回すとファイルだけが消える。
func TestIngestKeepFilesPreservesFiles(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	path := writeInboxFile(t, dir, "a.jsonl", eventLine("e1", "Phone", at))

	store := openTestStore(t)
	opts := Options{Dir: dir, DefaultDevice: "Phone", KeepFiles: true}

	stats, err := Ingest(context.Background(), store, opts, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 1 || stats.Inserted != 1 {
		t.Errorf("stats = %+v, want Files=1 Inserted=1", stats)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("KeepFiles=true なのにファイルが消えた")
	}

	// KeepFiles のまま繰り返しても二重登録にならず、ファイルも残る。
	stats, err = Ingest(context.Background(), store, opts, quietLogger())
	if err != nil {
		t.Fatalf("Ingest(2回目): %v", err)
	}
	if stats.Inserted != 0 {
		t.Errorf("2回目の stats = %+v, want Inserted=0", stats)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("2回目の KeepFiles=true でファイルが消えた")
	}

	// KeepFiles を外すと、重複を弾いたうえでファイルが消える。
	opts.KeepFiles = false
	stats, err = Ingest(context.Background(), store, opts, quietLogger())
	if err != nil {
		t.Fatalf("Ingest(3回目): %v", err)
	}
	if stats.Inserted != 0 {
		t.Errorf("3回目の stats = %+v, want Inserted=0", stats)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("KeepFiles=false なのにファイルが残っている")
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("raw.db の件数 = %d, want 1", len(events))
	}
}

// 共有ディレクトリを使わない構成（Windows）では何もしない。
func TestIngestWithoutDirDoesNothing(t *testing.T) {
	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: ""}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

// ディレクトリがまだ無くてもエラーにしない（収集アプリが未起動の場合）。
func TestIngestMissingDirIsNotAnError(t *testing.T) {
	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{
		Dir: filepath.Join(t.TempDir(), "not_created_yet"),
	}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 0 {
		t.Errorf("stats = %+v", stats)
	}
}
