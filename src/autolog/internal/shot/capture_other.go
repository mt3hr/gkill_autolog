//go:build !windows

package shot

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
)

// Capture は Windows 以外では使えない。
// Android の定期スクリーンショットは収集アプリ (ScreenshotCollector) の役目で、
// Termux の autolog は撮らない。
func Capture(_ *config.Config, _ time.Time) (*Result, error) {
	return nil, fmt.Errorf("screenshot is only supported on windows (running on %s)", runtime.GOOS)
}

// Run は Windows 以外では何もしない。
func Run(ctx context.Context, _ *config.Config, logger *slog.Logger) error {
	logger.Warn("このプラットフォームではスクリーンショットを撮らない", "goos", runtime.GOOS)
	<-ctx.Done()
	return nil
}
