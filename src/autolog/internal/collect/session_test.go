package collect

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// testSessionKind はセッションの変化。winapi.SessionEventKind の代わり。
type testSessionKind string

// fakeSessionWatcher はセッション監視の差し替え。
type fakeSessionWatcher struct {
	events chan testSessionKind
}

func newFakeSessionWatcher() *fakeSessionWatcher {
	return &fakeSessionWatcher{events: make(chan testSessionKind, 16)}
}

func (w *fakeSessionWatcher) Events() <-chan testSessionKind { return w.events }

func (w *fakeSessionWatcher) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func sessionPayload(t *testing.T, event *rawlog.Event) rawlog.SessionPayload {
	t.Helper()
	payload, err := rawlog.DecodePayload[rawlog.SessionPayload](event)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	return payload
}

func sessionActions(t *testing.T, events []*rawlog.Event) []rawlog.SessionAction {
	t.Helper()
	actions := make([]rawlog.SessionAction, 0, len(events))
	for _, event := range events {
		actions = append(actions, sessionPayload(t, event).Action)
	}
	return actions
}

func TestSessionCollectorMarksStartAndStop(t *testing.T) {
	// collector_start と collector_stop は必ず1件ずつ出す。
	// normalize がこの2つを「観測の切れ目」として使っており
	// (internal/normalize/state.go)、出ないと Wi-Fi・Bluetooth・充電の
	// 開いた区間が永久に閉じず、電源が入っていなかった時間までつながる。
	emitter := newTestEmitter(t)
	watcher := newFakeSessionWatcher()
	c := newSessionCollector(emitter, slog.New(slog.DiscardHandler), watcher)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()

	// collector_start が出るまで待つ。
	waitForEvents(t, emitter, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	got := sessionActions(t, drainEmitter(emitter))
	want := []rawlog.SessionAction{rawlog.SessionCollectorStart, rawlog.SessionCollectorStop}
	if len(got) != len(want) {
		t.Fatalf("記録 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("記録[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSessionCollectorRecordsObservedChanges(t *testing.T) {
	// 観測した変化をそのまま記録し、復帰では取り直しを求める。
	emitter := newTestEmitter(t)
	watcher := newFakeSessionWatcher()
	c := newSessionCollector(emitter, slog.New(slog.DiscardHandler), watcher)

	resumed := make(chan struct{}, 1)
	c.onResume = func() { resumed <- struct{}{} }

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()

	watcher.events <- testSessionKind("lock")
	watcher.events <- testSessionKind("unlock")
	watcher.events <- testSessionKind("suspend")
	watcher.events <- testSessionKind("resume")
	// 知らない種別は捨てる。取り込み側が知らないものを記録しない。
	watcher.events <- testSessionKind("screen_dimmed")

	select {
	case <-resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("復帰を観測したのに接続状態の取り直しが求められない")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	got := sessionActions(t, drainEmitter(emitter))
	want := []rawlog.SessionAction{
		rawlog.SessionCollectorStart,
		rawlog.SessionLock,
		rawlog.SessionUnlock,
		rawlog.SessionSuspend,
		rawlog.SessionResume,
		rawlog.SessionCollectorStop,
	}
	if len(got) != len(want) {
		t.Fatalf("記録 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("記録[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMapSessionAction(t *testing.T) {
	// 収集側の種別と生ログの記録種別の対応表。
	// 収集側が増やした種別を、取り込み側が知らないまま記録しないこと。
	tests := []struct {
		in     testSessionKind
		want   rawlog.SessionAction
		mapped bool
	}{
		{in: "logon", want: rawlog.SessionLogon, mapped: true},
		{in: "unlock", want: rawlog.SessionUnlock, mapped: true},
		{in: "lock", want: rawlog.SessionLock, mapped: true},
		{in: "logoff", want: rawlog.SessionLogoff, mapped: true},
		{in: "shutdown", want: rawlog.SessionShutdown, mapped: true},
		{in: "suspend", want: rawlog.SessionSuspend, mapped: true},
		{in: "resume", want: rawlog.SessionResume, mapped: true},
		// 収集プログラム自身の起動・停止は収集側が直接記録する。
		{in: "collector_start", mapped: false},
		{in: "collector_stop", mapped: false},
		// Android だけが出す種別。PC の収集からは出てこない。
		{in: "screen_off", mapped: false},
		{in: "", mapped: false},
		{in: "unknown", mapped: false},
	}

	for _, tt := range tests {
		got, mapped := mapSessionAction(tt.in)
		if mapped != tt.mapped {
			t.Errorf("mapSessionAction(%q) の可否 = %v, want %v", tt.in, mapped, tt.mapped)
			continue
		}
		if mapped && got != tt.want {
			t.Errorf("mapSessionAction(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// waitForEvents はキューに指定の件数が溜まるまで待つ。取り出しはしない。
func waitForEvents(t *testing.T, emitter *Emitter, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(emitter.events) >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("イベントが %d 件溜まらなかった (いま %d 件)", count, len(emitter.events))
}
