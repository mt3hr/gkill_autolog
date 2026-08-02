package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/normalize"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// proposalsFileName は --dump-proposals で書き出すファイルの名前。
const proposalsFileName = "proposals.jsonl"

// denyListFileName は URLog にしない URL のパターンを書くファイル。
const denyListFileName = "url_denylist.txt"

// notificationDenyListFileName は Kmemo にしない通知のパターンを書くファイル。
const notificationDenyListFileName = "notification_denylist.txt"

// loadDenyList は URL の除外リストを読む。無ければ既定の内容で作る。
func loadDenyList(cfg *config.Config) (*normalize.DenyList, string, error) {
	return loadDenyListFile(cfg, denyListFileName, normalize.DefaultDenyList)
}

// loadNotificationDenyList は通知の除外リストを読む。無ければ既定の内容で作る。
func loadNotificationDenyList(cfg *config.Config) (*normalize.DenyList, string, error) {
	return loadDenyListFile(cfg, notificationDenyListFileName, normalize.DefaultNotificationDenyList)
}

// loadDenyListFile は除外リストを読む。無ければ既定の内容で作る。
func loadDenyListFile(cfg *config.Config, fileName string, defaultContent string) (*normalize.DenyList, string, error) {
	path := filepath.Join(cfg.Home, fileName)

	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(defaultContent), 0o644); err != nil {
			return nil, path, fmt.Errorf("failed to create %s: %w", path, err)
		}
		content = []byte(defaultContent)
	} else if err != nil {
		return nil, path, fmt.Errorf("failed to read %s: %w", path, err)
	}

	list, err := normalize.ParseDenyList(strings.NewReader(string(content)))
	if err != nil {
		return nil, path, fmt.Errorf("%s: %w", path, err)
	}
	return list, path, nil
}

// resolveCutoff は処理の上限時刻を決める。既定は直近の午前4時。
func resolveCutoff(flag string) (time.Time, error) {
	if flag != "" {
		return rawlog.ParseTime(flag)
	}
	now := time.Now()
	cutoff := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
	if cutoff.After(now) {
		cutoff = cutoff.AddDate(0, 0, -1)
	}
	return cutoff, nil
}

// resolveFrom は処理の開始位置を決める。
//
// 既定は前回のカーソル。カーソルが無ければ7日前まで遡る（要件 §17 の滞留許容に合わせる）。
//
// 低水位マーク (CursorBackfill) がカーソルより過去を指していれば、そこまで遡る。
// 区間イベントは終わってから届くので、前回の取り込みがカーソルを進めた後に
// カーソルより過去の start_time で生ログが入ることがある。マークまで遡らないと、
// その分が二度と取り込まれない。読み直しの重複は台帳のイベント包含が防ぐ。
//
// 2つ目の戻り値は消費した低水位マーク。取り込みが成功したら、呼び出し側が
// ClearBackfillMark へ渡して消す（取り込み中に新しい遅着があれば値が変わり、消えずに残る）。
// マークが無いとき・--since 指定時はゼロ値を返す。
func resolveFrom(ctx context.Context, store *rawlog.Store, flag string) (time.Time, time.Time, error) {
	if flag != "" {
		from, err := rawlog.ParseTime(flag)
		return from, time.Time{}, err
	}
	cursor, ok, err := store.GetCursor(ctx, rawlog.CursorNormalize)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !ok {
		return time.Now().AddDate(0, 0, -7), time.Time{}, nil
	}

	mark, hasMark, err := store.GetCursor(ctx, rawlog.CursorBackfill)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !hasMark {
		return cursor, time.Time{}, nil
	}
	from := cursor
	if mark.Before(from) {
		from = mark
	}
	return from, mark, nil
}

// proposalTime は提案が指す時刻を返す。カーソルの引き戻しに使う。
func proposalTime(proposal normalize.Proposal) time.Time {
	switch {
	case proposal.StartTime != nil:
		return *proposal.StartTime
	case proposal.RelatedTime != nil:
		return *proposal.RelatedTime
	default:
		return time.Time{}
	}
}

// writeJSONL は1行1JSONで書き出す。件数が0でもファイルは作る。
func writeJSONL[T any](path string, items []T) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}

	encoder := json.NewEncoder(file)
	for _, item := range items {
		if err := encoder.Encode(item); err != nil {
			return errors.Join(fmt.Errorf("failed to write %s: %w", path, err), file.Close())
		}
	}
	// Close まで見届ける。ディスク満杯は Close で初めて返ることがあり、
	// 握り潰すと欠けたファイルを「書き出した」と案内してしまう。
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
