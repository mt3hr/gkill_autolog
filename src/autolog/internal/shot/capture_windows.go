//go:build windows

package shot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

// Capture は1枚撮って保存する。
//
// 画面がロックされている場合は撮らずに ErrLocked を返す（要件 §10）。
// スリープ中はそもそもこの関数が呼ばれない（タイマーが動かない、
// あるいは復帰後の次の正時まで待つ）ため、撮り逃した分の補完は行わない。
func Capture(cfg *config.Config, capturedAt time.Time) (*Result, error) {
	if winapi.IsSessionLocked() {
		return nil, ErrLocked
	}

	img, err := winapi.CaptureVirtualScreen()
	if err != nil {
		return nil, fmt.Errorf("failed to capture screen: %w", err)
	}

	return Save(img, cfg.ScreenshotDirFor(capturedAt), cfg.ScreenshotFileName(capturedAt, Extension), capturedAt)
}

// Run は設定した間隔で撮影し続ける。ctx が終わるまで返らない。
//
// 撮影に失敗しても収集全体は止めない。
// 撮れなかった時間の画像を後から補完することはしない。
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
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
			// タイマーは狙った時刻より少し前に起きることがあるため、
			// 最寄りの撮影時刻へ丸める。
			capturedAt := RoundToTick(now, interval)

			result, err := Capture(cfg, capturedAt)
			switch {
			case err == ErrLocked:
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
