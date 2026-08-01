package rawlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// 処理カーソルの名前。
const (
	// CursorNormalize は normalize が処理し終えた位置。
	// この時刻より前のイベントは Kyou 提案へ変換済みとみなす。
	CursorNormalize = "normalize"
)

// Store は生ログの追記専用ストア。
// 同じ raw.db を収集プログラム（書き込み）と normalize（読み取り）が同時に開くため WAL を使う。
type Store struct {
	db   *sql.DB
	path string
}

const createTableSQL = `
CREATE TABLE IF NOT EXISTS raw_event (
  schema_version INTEGER NOT NULL,
  event_id       TEXT NOT NULL,
  device         TEXT NOT NULL,
  event_type     TEXT NOT NULL,
  start_time     TEXT NOT NULL,
  end_time       TEXT,
  captured_at    TEXT NOT NULL,
  payload        TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_raw_event_id ON raw_event(device, event_id);
CREATE INDEX IF NOT EXISTS idx_raw_event_start ON raw_event(start_time);
CREATE INDEX IF NOT EXISTS idx_raw_event_type_start ON raw_event(event_type, start_time);

CREATE TABLE IF NOT EXISTS process_cursor (
  name       TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS open_state_interval (
  device     TEXT NOT NULL,
  source     TEXT NOT NULL,
  state_key  TEXT NOT NULL,
  title      TEXT NOT NULL,
  start_time TEXT NOT NULL,
  event_ids  TEXT NOT NULL,
  PRIMARY KEY (device, source, state_key)
);

CREATE TABLE IF NOT EXISTS notification_seen (
  device       TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  last_at      TEXT NOT NULL,
  PRIMARY KEY (device, content_hash)
);
`

// migrateSQL は既存の raw.db へ後から足した列。
//
// CREATE TABLE IF NOT EXISTS は既にある表を作り替えないので、
// 列の追加はここで行う。既に列があれば実行しない。
var migrateSQL = []struct {
	table  string
	column string
	sql    string
}{
	// 接続区間だけでなく、アプリ利用やメディア再生の末尾も持ち越すようになった。
	// それらは「まだ終わっていない」のではなく「もっと延びるかもしれない」区間なので、
	// 暫定の終了時刻を持つ。接続区間ではこの列は NULL のまま。
	{table: "open_state_interval", column: "end_time", sql: `ALTER TABLE open_state_interval ADD COLUMN end_time TEXT`},
}

// OpenStore は raw.db を開き、必要ならスキーマを作成する。
func OpenStore(path string) (*Store, error) {
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
	if err := migrate(db); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to migrate schema in %s: %w", path, err), db.Close())
	}
	return &Store{db: db, path: path}, nil
}

// migrate は後から足した列を必要なら追加する。
func migrate(db *sql.DB) error {
	for _, m := range migrateSQL {
		exists, err := hasColumn(db, m.table, m.column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := db.Exec(m.sql); err != nil {
			return fmt.Errorf("failed to add column %s.%s: %w", m.table, m.column, err)
		}
	}
	return nil
}

// hasColumn は表に列があるかを返す。
func hasColumn(db *sql.DB, table string, column string) (bool, error) {
	// table はコード内の定数なので、プレースホルダを使えない pragma へ直接埋めてよい。
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("failed to read columns of %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("failed to scan column of %s: %w", table, err)
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// Close はストアを閉じる。
func (s *Store) Close() error {
	return s.db.Close()
}

// Path は raw.db のパスを返す。
func (s *Store) Path() string {
	return s.path
}

const insertEventSQL = `
INSERT INTO raw_event (schema_version, event_id, device, event_type, start_time, end_time, captured_at, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device, event_id) DO NOTHING
`

// Put は1件のイベントを追記する。
// (device, event_id) が既に存在する場合は何もしない（Android や Chrome 拡張の再送を冪等にするため）。
// 追加されたときだけ true を返す。
func (s *Store) Put(ctx context.Context, e *Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("invalid event: %w", err)
	}
	result, err := s.db.ExecContext(ctx, insertEventSQL, args(e)...)
	if err != nil {
		return false, fmt.Errorf("failed to insert event %s: %w", e.EventID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to read affected rows for event %s: %w", e.EventID, err)
	}
	return affected > 0, nil
}

// PutBatch は複数イベントをひとつのトランザクションで追記し、実際に追加された件数を返す。
// 1件でも不正なイベントがあればトランザクション全体を中止する。
func (s *Store) PutBatch(ctx context.Context, events []*Event) (int, error) {
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return 0, fmt.Errorf("invalid event: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, insertEventSQL)
	if err != nil {
		return 0, errors.Join(fmt.Errorf("failed to prepare insert: %w", err), tx.Rollback())
	}
	defer stmt.Close()

	inserted := 0
	for _, e := range events {
		result, err := stmt.ExecContext(ctx, args(e)...)
		if err != nil {
			return 0, errors.Join(fmt.Errorf("failed to insert event %s: %w", e.EventID, err), tx.Rollback())
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, errors.Join(fmt.Errorf("failed to read affected rows for event %s: %w", e.EventID, err), tx.Rollback())
		}
		inserted += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}
	return inserted, nil
}

// storageTime は保存用の時刻文字列を返す。
//
// raw_event の時刻は文字列で持ち ORDER BY start_time で並べるため、
// オフセットが混ざると辞書順が時刻順と一致しなくなる。
// Chrome 拡張や Android は UTC (末尾 Z) で送ってくることがあるので、
// 保存する直前に必ずこの機械のローカル時刻へ揃える。
func storageTime(t time.Time) string {
	return FormatTime(t.In(time.Local))
}

func args(e *Event) []any {
	var endTime any
	if e.EndTime != nil {
		endTime = storageTime(*e.EndTime)
	}
	return []any{
		e.SchemaVersion,
		e.EventID,
		string(e.Device),
		string(e.EventType),
		storageTime(e.StartTime),
		endTime,
		storageTime(e.CapturedAt),
		string(e.Payload),
	}
}

// RangeQuery は Range の抽出条件。
type RangeQuery struct {
	// From は下限（この時刻を含む）。ゼロ値なら下限なし。
	From time.Time
	// To は上限（この時刻を含まない）。ゼロ値なら上限なし。
	To time.Time
	// Devices が空でなければ、その端末のイベントだけを返す。
	Devices []Device
	// Types が空でなければ、その種別のイベントだけを返す。
	Types []EventType
}

// Range は start_time が条件に合うイベントを時刻順に返す。
func (s *Store) Range(ctx context.Context, q RangeQuery) ([]*Event, error) {
	sqlStr := `SELECT schema_version, event_id, device, event_type, start_time, end_time, captured_at, payload FROM raw_event WHERE 1=1`
	var params []any

	if !q.From.IsZero() {
		sqlStr += ` AND start_time >= ?`
		params = append(params, storageTime(q.From))
	}
	if !q.To.IsZero() {
		sqlStr += ` AND start_time < ?`
		params = append(params, storageTime(q.To))
	}
	if len(q.Devices) > 0 {
		sqlStr += ` AND device IN (` + placeholders(len(q.Devices)) + `)`
		for _, d := range q.Devices {
			params = append(params, string(d))
		}
	}
	if len(q.Types) > 0 {
		sqlStr += ` AND event_type IN (` + placeholders(len(q.Types)) + `)`
		for _, t := range q.Types {
			params = append(params, string(t))
		}
	}
	sqlStr += ` ORDER BY start_time, event_id`

	rows, err := s.db.QueryContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, fmt.Errorf("failed to query raw_event: %w", err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate raw_event: %w", err)
	}
	return events, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	s := "?"
	for range n - 1 {
		s += ",?"
	}
	return s
}

func scanEvent(rows *sql.Rows) (*Event, error) {
	var (
		e            Event
		device       string
		eventType    string
		startTimeStr string
		endTimeStr   sql.NullString
		capturedStr  string
		payloadStr   string
	)
	if err := rows.Scan(&e.SchemaVersion, &e.EventID, &device, &eventType, &startTimeStr, &endTimeStr, &capturedStr, &payloadStr); err != nil {
		return nil, fmt.Errorf("failed to scan raw_event row: %w", err)
	}
	e.Device = Device(device)
	e.EventType = EventType(eventType)
	e.Payload = []byte(payloadStr)

	startTime, err := ParseTime(startTimeStr)
	if err != nil {
		return nil, fmt.Errorf("event %s: %w", e.EventID, err)
	}
	e.StartTime = startTime

	if endTimeStr.Valid && endTimeStr.String != "" {
		endTime, err := ParseTime(endTimeStr.String)
		if err != nil {
			return nil, fmt.Errorf("event %s: %w", e.EventID, err)
		}
		e.EndTime = &endTime
	}

	capturedAt, err := ParseTime(capturedStr)
	if err != nil {
		return nil, fmt.Errorf("event %s: %w", e.EventID, err)
	}
	e.CapturedAt = capturedAt

	return &e, nil
}

// LastEventTime は指定した端末・種別で最後に記録されたイベントの start_time を返す。
// 該当がなければゼロ値と false を返す。
// 収集プログラムが異常終了した後、未終了セッションを最終入力時刻で閉じるために使う。
func (s *Store) LastEventTime(ctx context.Context, device Device, eventType EventType) (time.Time, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT start_time FROM raw_event WHERE device = ? AND event_type = ? ORDER BY start_time DESC LIMIT 1`,
		string(device), string(eventType))

	var startTimeStr string
	switch err := row.Scan(&startTimeStr); {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("failed to query last event time for %s/%s: %w", device, eventType, err)
	}

	t, err := ParseTime(startTimeStr)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// Count は条件に合うイベント件数を返す。status サブコマンドの表示に使う。
func (s *Store) Count(ctx context.Context, q RangeQuery) (map[EventType]int, error) {
	events, err := s.Range(ctx, q)
	if err != nil {
		return nil, err
	}
	counts := map[EventType]int{}
	for _, e := range events {
		counts[e.EventType]++
	}
	return counts, nil
}

// GetCursor は処理カーソルを取得する。未設定なら false を返す。
func (s *Store) GetCursor(ctx context.Context, name string) (time.Time, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM process_cursor WHERE name = ?`, name)

	var value string
	switch err := row.Scan(&value); {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("failed to get cursor %s: %w", name, err)
	}

	t, err := ParseTime(value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("cursor %s: %w", name, err)
	}
	return t, true, nil
}

// SetCursor は処理カーソルを設定する。
// 書き込みに成功した分だけ前進させること。継続中セッションの開始時刻より先へ進めてはならない。
func (s *Store) SetCursor(ctx context.Context, name string, t time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO process_cursor (name, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		name, storageTime(t), storageTime(time.Now()))
	if err != nil {
		return fmt.Errorf("failed to set cursor %s: %w", name, err)
	}
	return nil
}

// OpenStateInterval は cutoff の時点でまだ確定していない区間。
//
// 使いみちは2つある。
//
// 1つ目は接続区間 (Wi-Fi・Bluetooth・充電) で、まだ切断を観測していないもの。
// 常時つないだままの機器 (スマートウォッチや自宅の Wi-Fi) があると、
// その区間は何日も閉じない。開始時刻までカーソルを引き戻していると
// 永久に処理位置が進まなくなるので、開いた区間だけをここへ保存して
// 次回へ持ち越し、カーソルは cutoff まで進める。
//
// 2つ目はアプリ利用やメディア再生の末尾で、結合相手が次のバッチに現れうるもの。
// こちらは終了時刻を観測済みなので End に入れる。次のバッチで結合相手が
// 現れなければ、そのまま確定して書き出す。
type OpenStateInterval struct {
	Device Device
	Source string
	Key    string
	Title  string
	Start  time.Time
	// End は観測済みの終了時刻。まだ終わりを観測していない接続区間では nil。
	End      *time.Time
	EventIDs []string
}

// LoadOpenStates は持ち越した開区間を読む。
func (s *Store) LoadOpenStates(ctx context.Context) ([]OpenStateInterval, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT device, source, state_key, title, start_time, end_time, event_ids
		 FROM open_state_interval ORDER BY device, source, state_key`)
	if err != nil {
		return nil, fmt.Errorf("failed to load open state intervals: %w", err)
	}
	defer rows.Close()

	var opens []OpenStateInterval
	for rows.Next() {
		var (
			open     OpenStateInterval
			start    string
			end      sql.NullString
			eventIDs string
		)
		if err := rows.Scan(&open.Device, &open.Source, &open.Key, &open.Title, &start, &end, &eventIDs); err != nil {
			return nil, fmt.Errorf("failed to scan open state interval: %w", err)
		}
		open.Start, err = ParseTime(start)
		if err != nil {
			return nil, fmt.Errorf("open state interval %s/%s: %w", open.Source, open.Key, err)
		}
		if end.Valid {
			endTime, err := ParseTime(end.String)
			if err != nil {
				return nil, fmt.Errorf("open state interval %s/%s end_time: %w", open.Source, open.Key, err)
			}
			open.End = &endTime
		}
		if err := json.Unmarshal([]byte(eventIDs), &open.EventIDs); err != nil {
			return nil, fmt.Errorf("open state interval %s/%s event_ids: %w", open.Source, open.Key, err)
		}
		opens = append(opens, open)
	}
	return opens, rows.Err()
}

// SaveOpenStates は開区間を保存する。以前の内容は入れ替える。
//
// 閉じた区間は opens に含まれないので、まとめて消してから入れ直す。
func (s *Store) SaveOpenStates(ctx context.Context, opens []OpenStateInterval) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx for open state intervals: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM open_state_interval`); err != nil {
		return fmt.Errorf("failed to clear open state intervals: %w", err)
	}
	for _, open := range opens {
		eventIDs, err := json.Marshal(open.EventIDs)
		if err != nil {
			return fmt.Errorf("failed to encode event_ids: %w", err)
		}
		var end any
		if open.End != nil {
			end = storageTime(*open.End)
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO open_state_interval (device, source, state_key, title, start_time, end_time, event_ids)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			open.Device, open.Source, open.Key, open.Title, storageTime(open.Start), end, string(eventIDs))
		if err != nil {
			return fmt.Errorf("failed to save open state interval %s/%s: %w", open.Source, open.Key, err)
		}
	}
	return tx.Commit()
}

// NotificationSeen は通知の重複排除に使う「直近に記録した内容」1件。
//
// 同じ内容の再通知を1件にまとめる判定は、取り込み1回のなかだけで完結しない。
// 取り込みを何分おきに走らせても結果が変わらないよう、直近の記録時刻をここへ残す。
type NotificationSeen struct {
	Device      Device
	ContentHash string
	LastAt      time.Time
}

// LoadNotificationSeen は持ち越した通知の記録時刻を読む。
func (s *Store) LoadNotificationSeen(ctx context.Context) ([]NotificationSeen, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT device, content_hash, last_at FROM notification_seen ORDER BY device, content_hash`)
	if err != nil {
		return nil, fmt.Errorf("failed to load notification seen: %w", err)
	}
	defer rows.Close()

	var seen []NotificationSeen
	for rows.Next() {
		var (
			item   NotificationSeen
			lastAt string
		)
		if err := rows.Scan(&item.Device, &item.ContentHash, &lastAt); err != nil {
			return nil, fmt.Errorf("failed to scan notification seen: %w", err)
		}
		item.LastAt, err = ParseTime(lastAt)
		if err != nil {
			return nil, fmt.Errorf("notification seen %s/%s: %w", item.Device, item.ContentHash, err)
		}
		seen = append(seen, item)
	}
	return seen, rows.Err()
}

// SaveNotificationSeen は通知の記録時刻を保存する。以前の内容は入れ替える。
//
// expireBefore より古い行は捨てる。重複判定の窓を過ぎた内容はもう使わないので、
// これを渡さないと表が増え続ける。
func (s *Store) SaveNotificationSeen(ctx context.Context, seen []NotificationSeen, expireBefore time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx for notification seen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_seen`); err != nil {
		return fmt.Errorf("failed to clear notification seen: %w", err)
	}
	for _, item := range seen {
		if item.LastAt.Before(expireBefore) {
			continue
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO notification_seen (device, content_hash, last_at) VALUES (?, ?, ?)`,
			item.Device, item.ContentHash, storageTime(item.LastAt))
		if err != nil {
			return fmt.Errorf("failed to save notification seen %s/%s: %w", item.Device, item.ContentHash, err)
		}
	}
	return tx.Commit()
}
