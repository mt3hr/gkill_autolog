// Package ledger は gkill へ書き込み済みの提案を記録する。
//
// Kyou の id は提案から決定的に導く (gkillclient.kyouIDFor) ため、同じ提案の
// 再送は gkill 側では「最新版で上書き」になり増えない。それでも台帳は要る。
// 送信そのものを省くことで URLog のサーバ側フェッチやログイン枠を浪費しないため、
// そしてカーソルの引き戻しで**別の id** の提案が再構成される断片 (後述) を
// 弾くためで、冪等性は「決定的 id」と「台帳」の二段構えになっている。
//
// 提案 id だけでは足りない。カーソルの引き戻し（継続中セッションや書き込み失敗）で
// 確定済み区間のイベント列を途中から読み直すと、部分集合のイベントから
// **別の id** を持つ提案が再構成されるため、id の突合だけでは素通りしてしまう。
// そこで書き込んだ提案の元イベントも (kind, source, device, event_id) で記録し、
// 「元イベントがすべて記録済み」の提案は既に書いた区間の断片とみなして弾く。
//
// 一部だけが記録済みの提案（遅れて届いた生ログが確定済み区間を延ばした形）を
// どう扱うかは収集元によって違うため、台帳は件数 (CoveredCount) を返すだけにし、
// 判断は書き込み側 (gkillclient.Writer) に置く。
//
// 要件 §18 は「二重登録防止」を初期スコープ外としているが、
// §17 の「失敗分を次回再処理可能にする」を満たすには最低限これが要る。
// gkill 側へ問い合わせての重複検査は行わない。
package ledger

// 編集前に読む: .claude/skills/autolog-pipeline/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Ledger は書き込み済み台帳。
type Ledger struct {
	db *sql.DB
}

const createTableSQL = `
CREATE TABLE IF NOT EXISTS written (
  proposal_id TEXT NOT NULL PRIMARY KEY,
  kind        TEXT NOT NULL,
  kyou_id     TEXT NOT NULL,
  written_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_written_at ON written(written_at);

CREATE TABLE IF NOT EXISTS written_event (
  kind        TEXT NOT NULL,
  source      TEXT NOT NULL,
  device      TEXT NOT NULL,
  event_id    TEXT NOT NULL,
  proposal_id TEXT NOT NULL,
  PRIMARY KEY (kind, source, device, event_id)
);
`

// Open は台帳を開き、必要ならスキーマを作成する。
func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory for %s: %w", path, err)
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	if _, err := db.Exec(createTableSQL); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to create schema in %s: %w", path, err), db.Close())
	}
	return &Ledger{db: db}, nil
}

// Close は台帳を閉じる。
func (l *Ledger) Close() error {
	return l.db.Close()
}

// IsWritten は既に書き込み済みかを返す。
func (l *Ledger) IsWritten(ctx context.Context, proposalID string) (bool, error) {
	row := l.db.QueryRowContext(ctx, `SELECT 1 FROM written WHERE proposal_id = ?`, proposalID)

	var one int
	switch err := row.Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("failed to check ledger for %s: %w", proposalID, err)
	}
	return true, nil
}

// LoadWritten は書き込み済みの提案IDをまとめて返す。
// 提案が多いときに1件ずつ問い合わせないで済むようにする。
func (l *Ledger) LoadWritten(ctx context.Context) (map[string]struct{}, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT proposal_id FROM written`)
	if err != nil {
		return nil, fmt.Errorf("failed to load ledger: %w", err)
	}
	defer rows.Close()

	written := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan ledger row: %w", err)
		}
		written[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate ledger: %w", err)
	}
	return written, nil
}

// Record は書き込み済みとして記録する。
// 既に記録があれば何もしない。
//
// eventIDs は提案の元になった生ログのイベントID。
// カーソルの引き戻しで同じイベントの部分集合から別 id の提案が再構成されたとき、
// CoveredCount の判定 (gkillclient.Writer が行う) がこれを使って弾く。
// 提案本体と同じトランザクションで記録し、
// 「提案は載っているのに元イベントが載っていない」中途半端な状態を残さない。
func (l *Ledger) Record(ctx context.Context, proposalID, kind, source, device, kyouID string, eventIDs []string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx to record %s: %w", proposalID, err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO written (proposal_id, kind, kyou_id, written_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(proposal_id) DO NOTHING`,
		proposalID, kind, kyouID, time.Now().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("failed to record %s in ledger: %w", proposalID, err)
	}
	for _, eventID := range eventIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO written_event (kind, source, device, event_id, proposal_id) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(kind, source, device, event_id) DO NOTHING`,
			kind, source, device, eventID, proposalID)
		if err != nil {
			return fmt.Errorf("failed to record event %s of %s in ledger: %w", eventID, proposalID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit record of %s: %w", proposalID, err)
	}
	return nil
}

// CoveredCount は提案の元イベントのうち記録済みの件数と、重複を除いた総数を返す。
//
// covered == total (total > 0) なら、この提案は既に書き込んだ区間を
// カーソルの引き戻しで読み直しただけの断片であり、書き込むと二重登録になる。
// 0 < covered < total は「書き込み済みイベントと新しいイベントが混ざった提案」で、
// 遅れて届いた生ログが確定済みの区間を延ばしたときにできる。
// これをどう扱うかは収集元によって違うので、判断は書き込み側 (gkillclient.Writer) が行う。
//
// 台帳を導入する前に書き込んだ分には元イベントの記録が無いので、
// その範囲の断片は検出できない（一度書かれてしまうと以後は検出できる）。
func (l *Ledger) CoveredCount(ctx context.Context, kind, source, device string, eventIDs []string) (covered int, total int, err error) {
	unique := make([]string, 0, len(eventIDs))
	seen := map[string]struct{}{}
	for _, id := range eventIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return 0, 0, nil
	}

	// IN のプレースホルダ数に上限があるので分けて数える。
	const chunkSize = 500
	for start := 0; start < len(unique); start += chunkSize {
		chunk := unique[start:min(start+chunkSize, len(unique))]

		placeholders := strings.Repeat(",?", len(chunk))[1:]
		params := make([]any, 0, len(chunk)+3)
		params = append(params, kind, source, device)
		for _, id := range chunk {
			params = append(params, id)
		}

		row := l.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM written_event
			 WHERE kind = ? AND source = ? AND device = ? AND event_id IN (`+placeholders+`)`,
			params...)
		var count int
		if err := row.Scan(&count); err != nil {
			return 0, 0, fmt.Errorf("failed to check event coverage: %w", err)
		}
		covered += count
	}
	return covered, len(unique), nil
}

// Count は記録件数を返す。
func (l *Ledger) Count(ctx context.Context) (int, error) {
	row := l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM written`)

	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count ledger: %w", err)
	}
	return count, nil
}
