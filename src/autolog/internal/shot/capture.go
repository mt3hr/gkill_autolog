package shot

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
)

// ErrCaptureUnsupported はこの環境では原理的に撮影できないことを表す。
//
// 一時的な失敗 (画面に繋がらない、撮影コマンドが落ちた) とは区別する。
// こちらは何度やり直しても変わらないので、Run は常駐を諦めて待つだけにする。
var ErrCaptureUnsupported = errors.New("screenshot is not supported on this platform")

// capturer は1枚撮る手段。platform ごとの実装を newCapturer が返す。
type capturer interface {
	// Locked は画面がロックされているかを返す。
	Locked() bool
	// Capture は全モニターを結合した1枚を返す（要件 §10）。
	Capture() (*image.RGBA, error)
	// Close は掴んだ接続を離す。
	Close() error
}

// newCapturer は撮影の手段を用意する。capture_*.go が設定する。
//
// テストで差し替えられるよう変数にしてある。
var newCapturer func(cfg *config.Config, logger *slog.Logger) (capturer, error)

// Capture は1枚撮って保存する。
//
// 画面がロックされている場合は撮らずに ErrLocked を返す（要件 §10）。
// スリープ中はそもそもこの関数が呼ばれない（タイマーが動かない、
// あるいは復帰後の次の区切りまで待つ）ため、撮り逃した分の補完は行わない。
func Capture(cfg *config.Config, capturedAt time.Time) (*Result, error) {
	return captureWith(cfg, capturedAt, slog.New(slog.DiscardHandler))
}

func captureWith(cfg *config.Config, capturedAt time.Time, logger *slog.Logger) (*Result, error) {
	c, err := newCapturer(cfg, logger)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	return captureOnce(c, cfg, capturedAt)
}

// captureOnce は用意済みの撮影手段で1枚撮って保存する。
func captureOnce(c capturer, cfg *config.Config, capturedAt time.Time) (*Result, error) {
	if c.Locked() {
		return nil, ErrLocked
	}

	img, err := c.Capture()
	if err != nil {
		return nil, fmt.Errorf("failed to capture screen: %w", err)
	}

	return Save(img, cfg.ScreenshotDirFor(capturedAt), cfg.ScreenshotFileName(capturedAt, Extension), capturedAt)
}
