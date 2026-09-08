//go:build !windows && (!linux || android)

package shot

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"fmt"
	"log/slog"
	"runtime"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
)

func init() {
	// Android の定期スクリーンショットは収集アプリ (ScreenshotCollector) の役目で、
	// Termux の autolog は撮らない。
	//
	// ビルドタグに !linux ではなく (!linux || android) と書いてあるのは、
	// Go では GOOS=android が linux のビルドタグも満たすため。
	// linux だけで分けると Android 向けビルドが Linux の撮影を取り込んでしまう。
	newCapturer = func(_ *config.Config, _ *slog.Logger) (capturer, error) {
		return nil, fmt.Errorf("screenshot is only supported on windows and linux (running on %s): %w",
			runtime.GOOS, ErrCaptureUnsupported)
	}
}
