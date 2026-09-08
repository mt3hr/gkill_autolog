package shot

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
)

// Run は設定した間隔で撮影し続ける。ctx が終わるまで返らない。
//
// 撮影に失敗しても収集全体は止めない。
// 撮れなかった時間の画像を後から補完することはしない。
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	// 撮る手段が原理的に無い環境 (Android・macOS) では、間隔ごとに
	// 失敗を繰り返さずここで待つ。Android の定期スクリーンショットは
	// 収集アプリ (ScreenshotCollector) の役目で、Termux の autolog は撮らない。
	if c, err := newCapturer(cfg, logger); err != nil {
		if errors.Is(err, ErrCaptureUnsupported) {
			logger.Warn("このプラットフォームではスクリーンショットを撮らない", "goos", runtime.GOOS)
			<-ctx.Done()
			return nil
		}
		// 一時的な失敗 (画面にまだ繋がらない等) は撮影のたびにやり直す。
		logger.Warn("スクリーンショットの手段をまだ用意できない。撮影のたびにやり直す", "error", err)
	} else {
		_ = c.Close()
	}

	interval := config.ClampScreenshotInterval(cfg.ScreenshotInterval)
	logger.Info("スクリーンショットの定期撮影を始める", "interval", interval.String())

	for {
		next := NextTick(time.Now(), interval)
		timer := time.NewTimer(time.Until(next))

		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case now := <-timer.C:
			// 通常は狙った時刻を記録時刻にし、スリープ復帰などで
			// 大きく遅れて起きた場合だけ実際の時刻をそのまま使う。
			capturedAt := CaptureTime(next, now, interval)

			result, err := captureWith(cfg, capturedAt, logger)
			switch {
			case errors.Is(err, ErrLocked):
				logger.Info("画面がロックされているため撮影しない", "at", capturedAt.Format(time.RFC3339))
			case err != nil:
				logger.Error("スクリーンショットに失敗した", "at", capturedAt.Format(time.RFC3339), "error", err)
			default:
				logger.Info("スクリーンショットを保存した",
					"path", result.Path,
					"size", fmt.Sprintf("%dx%d", result.Width, result.Height),
					"bytes", result.Bytes)
			}
		}
	}
}
