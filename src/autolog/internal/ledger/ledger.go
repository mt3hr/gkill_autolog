// Package ledger は gkill へ書き込み済みの提案を記録する。
//
// gkill Write MCP の add 系はクライアント側で id を採番するため、
// 決定的な id を指定して冪等に書き込むことができない。
// そこで「どの提案をどの Kyou として書いたか」をローカルに持ち、
// バッチが途中で失敗して再実行されても二重登録にならないようにする。
//
// 要件 §18 は「二重登録防止」を初期スコープ外としているが、
// §17 の「失敗分を次回再処理可能にする」を満たすには最低限これが要る。
// gkill 側へ問い合わせての重複検査は行わない。
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
func (l *Ledger) Record(ctx context.Context, proposalID, kind, kyouID string) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO written (proposal_id, kind, kyou_id, written_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(proposal_id) DO NOTHING`,
		proposalID, kind, kyouID, time.Now().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("failed to record %s in ledger: %w", proposalID, err)
	}
	return nil
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
