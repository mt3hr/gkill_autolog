//go:build linux && !android

package shot

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"image"
	"log/slog"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/linuxapi"
)

func init() {
	// ビルドタグに linux ではなく linux && !android と書いてあるのは、
	// Go では GOOS=android が linux のビルドタグも満たすため。
	// Android の定期スクリーンショットは収集アプリの役目で、
	// Termux の autolog は撮らない。
	newCapturer = func(cfg *config.Config, logger *slog.Logger) (capturer, error) {
		inner, err := linuxapi.NewCapturer(cfg.Linux.ScreenshotMethod, cfg.Linux.ScreenshotCommand)
		if err != nil {
			return nil, err
		}
		logger.Info("スクリーンショットの手段", "backend", inner.Backend())
		return linuxCapturer{inner: inner}, nil
	}
}

// linuxCapturer は linuxapi の撮影手段を shot の形へ写す。
type linuxCapturer struct {
	inner *linuxapi.Capturer
}

// Locked は画面がロックされているかを返す。
//
// collect が動いていれば SessionWatcher が状態を更新し続けているので、
// D-Bus を叩かずに即答する。どの手段でも分からなければ
// 「ロックされていない」として撮る（撮らない側に倒すと撮影が丸ごと止まる）。
func (c linuxCapturer) Locked() bool { return linuxapi.IsSessionLocked() }

// Capture は全モニターを結合した1枚を返す。
func (c linuxCapturer) Capture() (*image.RGBA, error) { return c.inner.Capture() }

func (c linuxCapturer) Close() error { return c.inner.Close() }
