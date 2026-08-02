package collect

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	_ "modernc.org/sqlite"
)

// lockStore は import のトランザクションに相当する書き込みロックを別接続で握る。
// 返す関数で手放す。
func lockStore(t *testing.T, path string) func() {
	t.Helper()
	locker, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		conn.Close()
		locker.Close()
	}
	t.Cleanup(release)
	return release
}

func TestFinalFlushDoesNotWaitFullBusyTimeoutWhileLocked(t *testing.T) {
	// コンソールのクローズやログオフでは Windows が既定5秒でプロセスを殺す。
	// import のトランザクションが raw.db を掴んでいるとき、停止時の書き出しが
	// 通常のロック待ち (10秒) を待ち続けると、その前にプロセスごと消える。
	// modernc のドライバは busy_timeout の待ちの間 ctx を見ない (実測) ため、
	// 期限は短い busy_timeout の接続で付けている。それが効いていることを確かめる。
	path := filepath.Join(t.TempDir(), "raw.db")
	store, err := rawlog.OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	release := lockStore(t, path)

	emitter := NewEmitter(store, rawlog.Device("TestDevice"), slog.New(slog.DiscardHandler))
	event, err := rawlog.NewEvent("ev-1", "TestDevice", rawlog.EventSession, time.Now(), nil,
		rawlog.SessionPayload{Action: rawlog.SessionCollectorStop})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	started := time.Now()
	emitter.finalFlush(context.Background(), []*rawlog.Event{event})
	elapsed := time.Since(started)

	// finalFlushBusyWait (3秒) + 余裕。10秒の busy_timeout を待っていたら失敗する。
	if elapsed > 5*time.Second {
		t.Fatalf("停止時の書き出しが %v 待った。5秒制限に間に合わない", elapsed)
	}

	// ロックが無ければ普通に書ける。
	release()
	emitter.finalFlush(context.Background(), []*rawlog.Event{event})
	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("件数 = %d, want 1 (ロック解除後は書ける)", len(events))
	}
}
