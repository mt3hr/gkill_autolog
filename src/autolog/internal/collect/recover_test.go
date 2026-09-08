package collect

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

var recoverBase = time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))

// newRecoverStore は生ログストアと、そこへ書く Emitter を用意する。
func newRecoverStore(t *testing.T) (*rawlog.Store, *Emitter) {
	t.Helper()
	store, err := rawlog.OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, NewEmitter(store, rawlog.Device("TestDevice"), slog.New(slog.DiscardHandler))
}

// putEvent は生ログを1件直接書き込む。
func putEvent(t *testing.T, store *rawlog.Store, id string, eventType rawlog.EventType, at time.Time, payload any) {
	t.Helper()
	event, err := rawlog.NewEvent(id, rawlog.Device("TestDevice"), eventType, at, nil, payload)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if _, err := store.Put(t.Context(), event); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// recoveredLocks は補完された lock を集める。
func recoveredLocks(t *testing.T, store *rawlog.Store) []*rawlog.Event {
	t.Helper()
	events, err := store.Range(t.Context(), rawlog.RangeQuery{
		Devices: []rawlog.Device{rawlog.Device("TestDevice")},
		Types:   []rawlog.EventType{rawlog.EventSession},
	})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}

	var recovered []*rawlog.Event
	for _, event := range events {
		if sessionPayload(t, event).Recovered {
			recovered = append(recovered, event)
		}
	}
	return recovered
}

func TestRecoverClosesUnfinishedSessionAtLastInput(t *testing.T) {
	// 直前が「開始側」のまま終わっていたら、その後の最終入力時刻で閉じる（要件 §6.1）。
	store, emitter := newRecoverStore(t)

	putEvent(t, store, "s1", rawlog.EventSession, recoverBase,
		rawlog.SessionPayload{Action: rawlog.SessionCollectorStart})
	lastInput := recoverBase.Add(30 * time.Minute)
	putEvent(t, store, "i1", rawlog.EventInput, lastInput,
		rawlog.InputPayload{AppName: "app", WindowTitle: "題名"})

	if err := recoverUnfinishedSession(t.Context(), store, emitter, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("recoverUnfinishedSession: %v", err)
	}

	recovered := recoveredLocks(t, store)
	if len(recovered) != 1 {
		t.Fatalf("補完された lock = %d 件, want 1", len(recovered))
	}
	if !recovered[0].StartTime.Equal(lastInput) {
		t.Errorf("閉じた時刻 = %s, want %s（アイドル判定時刻ではなく最終入力時刻）",
			rawlog.FormatTime(recovered[0].StartTime), rawlog.FormatTime(lastInput))
	}
	if action := sessionPayload(t, recovered[0]).Action; action != rawlog.SessionLock {
		t.Errorf("補完の種別 = %q, want lock", action)
	}
}

func TestRecoverFallsBackToSessionStart(t *testing.T) {
	// 入力が無い、あるいはセッション開始より前の入力しか無ければ開始時刻で閉じる。
	// Linux でウィンドウを観測できない環境では入力イベントが出ないので、
	// この経路がそのまま常用になる。
	store, emitter := newRecoverStore(t)

	putEvent(t, store, "i0", rawlog.EventInput, recoverBase.Add(-time.Hour),
		rawlog.InputPayload{AppName: "app", WindowTitle: "題名"})
	putEvent(t, store, "s1", rawlog.EventSession, recoverBase,
		rawlog.SessionPayload{Action: rawlog.SessionUnlock})

	if err := recoverUnfinishedSession(t.Context(), store, emitter, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("recoverUnfinishedSession: %v", err)
	}

	recovered := recoveredLocks(t, store)
	if len(recovered) != 1 {
		t.Fatalf("補完された lock = %d 件, want 1", len(recovered))
	}
	if !recovered[0].StartTime.Equal(recoverBase) {
		t.Errorf("閉じた時刻 = %s, want %s（セッション開始時刻）",
			rawlog.FormatTime(recovered[0].StartTime), rawlog.FormatTime(recoverBase))
	}
}

func TestRecoverSkipsClosedSession(t *testing.T) {
	// 直前が「終了側」なら何もしない。正常終了に偽の lock を足さない。
	for _, action := range []rawlog.SessionAction{
		rawlog.SessionCollectorStop,
		rawlog.SessionLock,
		rawlog.SessionShutdown,
		rawlog.SessionLogoff,
	} {
		t.Run(string(action), func(t *testing.T) {
			store, emitter := newRecoverStore(t)
			putEvent(t, store, "s1", rawlog.EventSession, recoverBase,
				rawlog.SessionPayload{Action: rawlog.SessionCollectorStart})
			putEvent(t, store, "s2", rawlog.EventSession, recoverBase.Add(time.Hour),
				rawlog.SessionPayload{Action: action})

			if err := recoverUnfinishedSession(t.Context(), store, emitter, slog.New(slog.DiscardHandler)); err != nil {
				t.Fatalf("recoverUnfinishedSession: %v", err)
			}
			if recovered := recoveredLocks(t, store); len(recovered) != 0 {
				t.Errorf("直前が %q なのに %d 件補完した, want 0", action, len(recovered))
			}
		})
	}
}

func TestRecoverSkipsEmptyStore(t *testing.T) {
	// 初回起動。セッションイベントが1件も無ければ何もしない。
	store, emitter := newRecoverStore(t)

	if err := recoverUnfinishedSession(t.Context(), store, emitter, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("recoverUnfinishedSession: %v", err)
	}
	if recovered := recoveredLocks(t, store); len(recovered) != 0 {
		t.Errorf("初回起動で %d 件補完した, want 0", len(recovered))
	}
}

func TestRecoverIsIdempotent(t *testing.T) {
	// 補完した lock は「終了側」なので、二度目の起動では何も足さない。
	store, emitter := newRecoverStore(t)
	putEvent(t, store, "s1", rawlog.EventSession, recoverBase,
		rawlog.SessionPayload{Action: rawlog.SessionCollectorStart})

	for range 2 {
		if err := recoverUnfinishedSession(t.Context(), store, emitter, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatalf("recoverUnfinishedSession: %v", err)
		}
	}

	if recovered := recoveredLocks(t, store); len(recovered) != 1 {
		t.Errorf("2回呼んで %d 件, want 1", len(recovered))
	}
}
