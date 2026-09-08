package collect

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// testInputKind は入力の種別。winapi.InputKind / linuxapi.InputKind の代わり。
type testInputKind string

// fakeInputHook は入力フックの差し替え。Run は ctx が終わるまで返らない。
type fakeInputHook struct {
	events chan testInputKind
	stop   chan struct{}
}

func newFakeInputHook() *fakeInputHook {
	return &fakeInputHook{events: make(chan testInputKind, 16), stop: make(chan struct{})}
}

func (h *fakeInputHook) Events() <-chan testInputKind { return h.events }

func (h *fakeInputHook) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		// 収集の停止に伴う終了。Events を閉じるかどうかは実装依存なので、
		// 停止時の書き出しの検証が閉じる側の経路に流れないよう閉じない。
	case <-h.stop:
		// フックだけが先に終わった場合を再現する。
		close(h.events)
	}
	return nil
}

// stopHookOnly はフックだけを終わらせる。
func (h *fakeInputHook) stopHookOnly() { close(h.stop) }

// fakeForeground は前面ウィンドウの差し替え。
// 収集のゴルーチンとテストのゴルーチンの両方から触るので排他する。
type fakeForeground struct {
	mu     sync.Mutex
	window foregroundWindow
	ok     bool
	calls  int
}

func newFakeForeground(window foregroundWindow) *fakeForeground {
	return &fakeForeground{window: window, ok: true}
}

func (f *fakeForeground) get() (foregroundWindow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.window, f.ok, nil
}

func (f *fakeForeground) set(window foregroundWindow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.window = window
}

func (f *fakeForeground) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// waitForCalls は前面ウィンドウの取得が指定の回数に達するまで待つ。
func waitForCalls(t *testing.T, foreground *fakeForeground, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if foreground.callCount() >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("前面ウィンドウの取得が %d 回に達しなかった (いま %d 回)", count, foreground.callCount())
}

// newTestEmitter は書き込みキューから直接読むための Emitter を作る。
func newTestEmitter(t *testing.T) *Emitter {
	t.Helper()
	store, err := rawlog.OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewEmitter(store, rawlog.Device("TestDevice"), slog.New(slog.DiscardHandler))
}

// drainEmitter はキューに溜まったイベントを取り出す。
func drainEmitter(e *Emitter) []*rawlog.Event {
	var events []*rawlog.Event
	for {
		select {
		case ev := <-e.events:
			events = append(events, ev)
		default:
			return events
		}
	}
}

func inputPayload(t *testing.T, event *rawlog.Event) rawlog.InputPayload {
	t.Helper()
	payload, err := rawlog.DecodePayload[rawlog.InputPayload](event)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	return payload
}

func newTestWindowCollector(t *testing.T, foreground *fakeForeground) (*windowCollector[testInputKind], *fakeInputHook, *Emitter) {
	t.Helper()
	emitter := newTestEmitter(t)
	hook := newFakeInputHook()
	return newWindowCollector(emitter, slog.New(slog.DiscardHandler), hook, foreground.get), hook, emitter
}

func TestWindowCollectorIgnoresTickWithoutInput(t *testing.T) {
	// 入力が無ければ前面ウィンドウを見に行かない。
	// マウス移動だけでは操作扱いにしない（要件 §6.2）ことの土台。
	foreground := newFakeForeground(foregroundWindow{AppName: "app.exe", WindowTitle: "題名"})
	c, _, emitter := newTestWindowCollector(t, foreground)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()

	// 入力を送らないままサンプリング間隔をいくつか越えさせる。
	time.Sleep(2 * windowSampleInterval)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := foreground.callCount(); got != 0 {
		t.Errorf("入力が無いのに前面ウィンドウを %d 回取得した, want 0", got)
	}
	if events := drainEmitter(emitter); len(events) != 0 {
		t.Errorf("入力が無いのに %d 件記録した, want 0", len(events))
	}
}

func TestWindowCollectorEmitsOnInput(t *testing.T) {
	// 入力があったサンプリングで記録する。ウィンドウタイトルは
	// gkill へは出さないが生ログには残す（要件 §6.4）。
	foreground := newFakeForeground(foregroundWindow{
		AppName:        "chrome.exe",
		AppDisplayName: "Google Chrome",
		WindowTitle:    "題名",
		ProcessPath:    "/usr/bin/chrome",
		PID:            42,
	})
	c, hook, emitter := newTestWindowCollector(t, foreground)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()

	hook.events <- testInputKind("click")
	waitForEvents(t, emitter, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	events := drainEmitter(emitter)
	if len(events) == 0 {
		t.Fatal("入力があったのに1件も記録されていない")
	}
	payload := inputPayload(t, events[0])
	if payload.AppName != "chrome.exe" || payload.AppDisplayName != "Google Chrome" {
		t.Errorf("アプリ名 = %q / %q", payload.AppName, payload.AppDisplayName)
	}
	if payload.WindowTitle != "題名" {
		t.Errorf("ウィンドウタイトル = %q, want 題名（生ログには残す）", payload.WindowTitle)
	}
	if payload.Kind != "click" {
		t.Errorf("入力の種別 = %q, want click", payload.Kind)
	}
	if payload.PID != 42 {
		t.Errorf("PID = %d, want 42", payload.PID)
	}
}

func TestWindowCollectorEmitsLastInputOnStop(t *testing.T) {
	// 停止時に、まだ書き出していない最後の入力を取りこぼさない。
	// ここを落とすとログオフ直前の操作が丸ごと消える。
	foreground := newFakeForeground(foregroundWindow{AppName: "app.exe", WindowTitle: "題名"})
	c, hook, emitter := newTestWindowCollector(t, foreground)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()

	// 1回目の入力で追跡が始まり、1件書き出される。
	hook.events <- testInputKind("key")
	waitForEvents(t, emitter, 1)
	if first := drainEmitter(emitter); len(first) != 1 {
		t.Fatalf("最初の記録 = %d 件, want 1", len(first))
	}
	sampled := foreground.callCount()

	// 同じウィンドウのまま2回目の入力。心拍の間隔 (30秒) には届かないので
	// この時点では書き出されない。
	hook.events <- testInputKind("key")
	waitForCalls(t, foreground, sampled+1)
	if mid := drainEmitter(emitter); len(mid) != 0 {
		t.Fatalf("心拍の間隔前に %d 件記録した, want 0", len(mid))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	if last := drainEmitter(emitter); len(last) != 1 {
		t.Errorf("停止時の書き出し = %d 件, want 1（最後の入力を取りこぼさない）", len(last))
	}
}

func TestWindowCollectorEmitsBeforeSwitch(t *testing.T) {
	// ウィンドウが切り替わったら、切り替わる前の最終入力時刻を残してから
	// 新しいウィンドウの記録を始める。
	foreground := newFakeForeground(foregroundWindow{AppName: "app1.exe", WindowTitle: "題名"})
	c, hook, emitter := newTestWindowCollector(t, foreground)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()

	hook.events <- testInputKind("click")
	waitForEvents(t, emitter, 1)
	drainEmitter(emitter)

	foreground.set(foregroundWindow{AppName: "app2.exe", WindowTitle: "題名"})
	hook.events <- testInputKind("click")
	waitForEvents(t, emitter, 1)

	events := drainEmitter(emitter)
	if len(events) == 0 {
		t.Fatal("切り替え後に1件も記録されていない")
	}
	if got := inputPayload(t, events[len(events)-1]).AppName; got != "app2.exe" {
		t.Errorf("切り替え後のアプリ名 = %q, want app2.exe", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestWindowCollectorStopsWhenHookCloses(t *testing.T) {
	// フックが終わったら収集も終わる。フックだけ死んだまま気づかず
	// 「入力が無い」ことになるのを防ぐ。
	foreground := newFakeForeground(foregroundWindow{})
	c, hook, _ := newTestWindowCollector(t, foreground)

	done := make(chan error, 1)
	go func() { done <- c.run(t.Context()) }()

	hook.stopHookOnly()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("フックが閉じたのに収集が終わらない")
	}
}
