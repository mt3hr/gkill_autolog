package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"log/slog"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
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
// 入力の検出はフック、ウィンドウの取得は 1 秒ごとのサンプリングで行う。
//
// 入力とウィンドウの**両方**が観測できるときだけ起動すること。
// アプリ名が空の入力イベントを溜めると、normalize の segmentTitle が
// ウィンドウタイトルへフォールバックし、要件 §6.4 が禁じている
// 「ウィンドウタイトルが TimeIs のタイトルになる」状態を量産する。
type windowCollector[K ~string] struct {
	emitter    *Emitter
	logger     *slog.Logger
	hook       eventSource[K]
	foreground foregroundWindowFunc
}

func newWindowCollector[K ~string](emitter *Emitter, logger *slog.Logger, hook eventSource[K], foreground foregroundWindowFunc) *windowCollector[K] {
	return &windowCollector[K]{emitter: emitter, logger: logger, hook: hook, foreground: foreground}
}

func (c *windowCollector[K]) run(ctx context.Context) error {
	hookDone := make(chan error, 1)
	go func() {
		hookDone <- c.hook.Run(ctx)
	}()

	ticker := time.NewTicker(windowSampleInterval)
	defer ticker.Stop()

	var (
		// 直近のサンプリング間隔で入力があったか。
		sawInput bool
		lastKind K

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

		case kind, ok := <-c.hook.Events():
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

			info, ok, err := c.foreground()
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
