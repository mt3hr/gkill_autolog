package shot

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// fakeCapturer は撮影手段の差し替え。
type fakeCapturer struct {
	mu       sync.Mutex
	locked   bool
	captures int
	closed   int
}

func (c *fakeCapturer) Locked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.locked
}

func (c *fakeCapturer) Capture() (*image.RGBA, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captures++
	return image.NewRGBA(image.Rect(0, 0, 2, 2)), nil
}

func (c *fakeCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func (c *fakeCapturer) captureCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.captures
}

// useCapturer は撮影手段を差し替え、テストが終わったら戻す。
func useCapturer(t *testing.T, fn func(*config.Config, *slog.Logger) (capturer, error)) {
	t.Helper()
	saved := newCapturer
	newCapturer = fn
	t.Cleanup(func() { newCapturer = saved })
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Device:             rawlog.Device("TestDevice"),
		ScreenshotDir:      t.TempDir(),
		ScreenshotInterval: time.Hour,
	}
}

func TestCaptureSkipsWhenLocked(t *testing.T) {
	// ロック中は撮らない（要件 §10）。撮影そのものも呼ばない。
	fake := &fakeCapturer{locked: true}
	useCapturer(t, func(*config.Config, *slog.Logger) (capturer, error) { return fake, nil })

	cfg := testConfig(t)
	_, err := Capture(cfg, time.Now())
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Capture のエラー = %v, want ErrLocked", err)
	}
	if fake.captureCount() != 0 {
		t.Errorf("ロック中に %d 回撮影した, want 0", fake.captureCount())
	}

	entries, err := os.ReadDir(cfg.ScreenshotDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ロック中なのに %d 件保存された, want 0", len(entries))
	}
}

func TestCaptureSetsModTimeToCapturedAt(t *testing.T) {
	// gkill の IDF は登録時のファイル mtime を RelatedTime にするため、
	// 保存後に必ず mtime を撮影時刻へ合わせる。
	fake := &fakeCapturer{}
	useCapturer(t, func(*config.Config, *slog.Logger) (capturer, error) { return fake, nil })

	cfg := testConfig(t)
	capturedAt := time.Date(2026, 7, 25, 21, 0, 0, 0, time.FixedZone("JST", 9*60*60))

	result, err := Capture(cfg, capturedAt)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if filepath.Base(result.Path) != "TestDevice_2026-07-25_21-00-00.webp" {
		t.Errorf("ファイル名 = %q", filepath.Base(result.Path))
	}

	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(capturedAt) {
		t.Errorf("mtime = %s, want %s（撮影時刻に合わせる）", info.ModTime(), capturedAt)
	}
}

func TestRunWaitsWhenCaptureUnsupported(t *testing.T) {
	// 撮る手段が原理的に無い環境では、間隔ごとに失敗を繰り返さず待つだけ。
	// Android の定期スクリーンショットは収集アプリの役目で、
	// Termux の autolog は撮らない。
	calls := 0
	useCapturer(t, func(*config.Config, *slog.Logger) (capturer, error) {
		calls++
		return nil, ErrCaptureUnsupported
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, testConfig(t), slog.New(slog.DiscardHandler)) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("撮れない環境で Run が返らない")
	}

	if calls != 1 {
		t.Errorf("撮影手段の用意を %d 回試した, want 1（撮れないと分かったら繰り返さない）", calls)
	}
}

func TestRunKeepsGoingWhenCaptureTemporarilyFails(t *testing.T) {
	// 一時的な失敗（画面にまだ繋がらない等）では常駐を諦めない。
	useCapturer(t, func(*config.Config, *slog.Logger) (capturer, error) {
		return nil, errors.New("まだ画面に繋がらない")
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, testConfig(t), slog.New(slog.DiscardHandler)) }()

	// 撮影の区切りを待たずに止める。ここで返らなければ常駐している。
	time.Sleep(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("一時的な失敗で Run が終わってしまった")
	default:
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}
