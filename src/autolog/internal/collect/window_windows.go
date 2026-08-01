//go:build windows

package collect

import (
	"context"
	"log/slog"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

const (
	// windowSampleInterval は前面ウィンドウを見に行く間隔。
	// 入力のたびにプロセス情報を取ると重いので、入力の有無だけをフックで拾い、
	// ウィンドウの取得はこの間隔でまとめて行う。
	windowSampleInterval = time.Second

	// windowHeartbeatInterval は同じウィンドウを操作し続けているときに
	// イベントを刻む間隔。normalize 側が最終入力時刻を復元できるだけの粒度があればよい。
	windowHeartbeatInterval = 30 * time.Second
)

// windowCollector は操作を伴うアクティブウィンドウを記録する。
//
// 記録する入力はクリック・ホイール・キー入力だけで、マウス移動は含めない（要件 §6.2）。
// 入力の検出は低レベルフック、ウィンドウの取得は 1 秒ごとのサンプリングで行う。
type windowCollector struct {
	emitter *Emitter
	logger  *slog.Logger
}

func newWindowCollector(emitter *Emitter, logger *slog.Logger) *windowCollector {
	return &windowCollector{emitter: emitter, logger: logger}
}

func (c *windowCollector) run(ctx context.Context) error {
	hook := winapi.NewInputHook()

	hookDone := make(chan error, 1)
	go func() {
		hookDone <- hook.Run(ctx)
	}()

	ticker := time.NewTicker(windowSampleInterval)
	defer ticker.Stop()

	var (
		// 直近のサンプリング間隔で入力があったか。
		sawInput bool
		lastKind winapi.InputKind

		// いま記録しているウィンドウと、そこで最後に入力があった時刻。
		currentApp     string
		currentDisplay string
		currentTitle   string
		currentPID     uint32
		currentPath    string
		lastInput      time.Time
		lastEmitted    time.Time
		tracking       bool
	)

	// emit は現在追跡しているウィンドウの入力イベントを1件書き出す。
	emit := func() {
		if !tracking {
			return
		}
		c.emitter.Emit(rawlog.EventInput, lastInput, nil, rawlog.InputPayload{
			AppName:        currentApp,
			AppDisplayName: currentDisplay,
			WindowTitle:    currentTitle,
			ProcessPath:    currentPath,
			PID:            int(currentPID),
			Kind:           string(lastKind),
		})
		lastEmitted = lastInput
	}

	for {
		select {
		case <-ctx.Done():
			// 最後の入力を取りこぼさないよう、追跡中のものを書き出してから終わる。
			if tracking && lastInput.After(lastEmitted) {
				emit()
			}
			return <-hookDone

		case err := <-hookDone:
			return err

		case kind, ok := <-hook.Events():
			if !ok {
				return <-hookDone
			}
			sawInput = true
			lastKind = kind

		case now := <-ticker.C:
			if !sawInput {
				continue
			}
			sawInput = false

			info, ok, err := winapi.GetForegroundWindow()
			if err != nil {
				c.logger.Warn("前面ウィンドウを取得できなかった", "error", err)
				continue
			}
			if !ok {
				continue
			}

			switched := !tracking || info.AppName != currentApp || info.WindowTitle != currentTitle
			if switched {
				// 切り替わる前のウィンドウで最後に入力のあった時刻を残す。
				if tracking && lastInput.After(lastEmitted) {
					emit()
				}
				currentApp = info.AppName
				currentDisplay = info.AppDisplayName
				currentTitle = info.WindowTitle
				currentPID = info.PID
				currentPath = info.ProcessPath
				tracking = true
				lastInput = now
				emit()
				continue
			}

			lastInput = now
			if now.Sub(lastEmitted) >= windowHeartbeatInterval {
				emit()
			}
		}
	}
}
