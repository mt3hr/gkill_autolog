//go:build windows

package collect

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

// sessionCollector はロック・解除・ログオン・ログオフ・電源の変化を記録する。
type sessionCollector struct {
	emitter *Emitter
	logger  *slog.Logger
}

func newSessionCollector(emitter *Emitter, logger *slog.Logger) *sessionCollector {
	return &sessionCollector{emitter: emitter, logger: logger}
}

func (c *sessionCollector) run(ctx context.Context) error {
	watcher := winapi.NewSessionWatcher()

	watcherDone := make(chan error, 1)
	go func() {
		watcherDone <- watcher.Run(ctx)
	}()

	c.emitter.Emit(rawlog.EventSession, time.Now(), nil, rawlog.SessionPayload{
		Action: rawlog.SessionCollectorStart,
	})

	for {
		select {
		case <-ctx.Done():
			// 正常終了したことを残す。次回起動時の未終了セッション復元で参照する。
			c.emitter.Emit(rawlog.EventSession, time.Now(), nil, rawlog.SessionPayload{
				Action: rawlog.SessionCollectorStop,
			})
			return <-watcherDone

		case err := <-watcherDone:
			return err

		case kind, ok := <-watcher.Events():
			if !ok {
				return <-watcherDone
			}
			action, mapped := mapSessionAction(kind)
			if !mapped {
				continue
			}
			c.logger.Info("セッション状態が変化した", "action", action)
			c.emitter.Emit(rawlog.EventSession, time.Now(), nil, rawlog.SessionPayload{Action: action})
		}
	}
}

func mapSessionAction(kind winapi.SessionEventKind) (rawlog.SessionAction, bool) {
	switch kind {
	case winapi.SessionLogon:
		return rawlog.SessionLogon, true
	case winapi.SessionUnlock:
		return rawlog.SessionUnlock, true
	case winapi.SessionLock:
		return rawlog.SessionLock, true
	case winapi.SessionLogoff:
		return rawlog.SessionLogoff, true
	case winapi.SessionShutdown:
		return rawlog.SessionShutdown, true
	case winapi.SessionSuspend:
		return rawlog.SessionSuspend, true
	case winapi.SessionResume:
		return rawlog.SessionResume, true
	default:
		return "", false
	}
}

// recoverUnfinishedSession は前回の収集プログラムが異常終了していた場合に、
// 最後に確認できた入力時刻で未終了セッションを閉じる（要件 §6.1）。
//
// 直前のセッションイベントが「開始側」（ログオン・解除・復帰・収集開始）のまま
// 終わっていれば、その後の最終入力時刻に lock を1件補う。
// 補ったイベントには recovered = true を付け、実際に観測したロックと区別できるようにする。
func recoverUnfinishedSession(ctx context.Context, store *rawlog.Store, emitter *Emitter, logger *slog.Logger) error {
	device := emitter.Device()

	events, err := store.Range(ctx, rawlog.RangeQuery{
		Devices: []rawlog.Device{device},
		Types:   []rawlog.EventType{rawlog.EventSession},
	})
	if err != nil {
		return fmt.Errorf("failed to load session events for recovery: %w", err)
	}
	if len(events) == 0 {
		return nil
	}

	last := events[len(events)-1]
	payload, err := rawlog.DecodePayload[rawlog.SessionPayload](last)
	if err != nil {
		return err
	}
	if !isSessionOpening(payload.Action) {
		return nil
	}

	lastInput, ok, err := store.LastEventTime(ctx, device, rawlog.EventInput)
	if err != nil {
		return err
	}
	// 入力が無い、あるいはセッション開始より前の入力しか無い場合は開始時刻で閉じる。
	closeAt := last.StartTime
	if ok && lastInput.After(closeAt) {
		closeAt = lastInput
	}

	logger.Warn("前回のセッションが閉じられていないため補完する",
		"last_action", payload.Action, "close_at", rawlog.FormatTime(closeAt))

	event, err := rawlog.NewEvent(
		"recovered-"+rawlog.FormatTime(closeAt),
		device,
		rawlog.EventSession,
		closeAt,
		nil,
		rawlog.SessionPayload{Action: rawlog.SessionLock, Recovered: true},
	)
	if err != nil {
		return err
	}
	if _, err := store.Put(ctx, event); err != nil {
		return fmt.Errorf("failed to store recovered session event: %w", err)
	}
	return nil
}

// isSessionOpening は端末の利用を開始する側のイベントかを返す。
func isSessionOpening(action rawlog.SessionAction) bool {
	switch action {
	case rawlog.SessionLogon, rawlog.SessionUnlock, rawlog.SessionResume, rawlog.SessionCollectorStart:
		return true
	default:
		return false
	}
}
