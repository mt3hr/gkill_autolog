package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// sessionCollector はロック・解除・ログオン・ログオフ・電源の変化を記録する。
//
// 観測できるものが1つも無い環境でも起動すること。collector_start と
// collector_stop は normalize が「観測の切れ目」として使う唯一のマーカーで
// （internal/normalize/state.go）、これが出ないと Wi-Fi・Bluetooth・充電の
// 開いた区間が永久に閉じず、電源が入っていなかった時間までつながる。
type sessionCollector[K ~string] struct {
	emitter *Emitter
	logger  *slog.Logger
	watcher eventSource[K]

	// onResume はスリープ復帰を観測したときに呼ぶ。run より前に設定すること。
	// 接続の収集 (netPowerCollector) が suspend で閉じた区間を取り直すために使う。
	onResume func()
}

func newSessionCollector[K ~string](emitter *Emitter, logger *slog.Logger, watcher eventSource[K]) *sessionCollector[K] {
	return &sessionCollector[K]{emitter: emitter, logger: logger, watcher: watcher}
}

func (c *sessionCollector[K]) run(ctx context.Context) error {
	watcherDone := make(chan error, 1)
	go func() {
		watcherDone <- c.watcher.Run(ctx)
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

		case kind, ok := <-c.watcher.Events():
			if !ok {
				return <-watcherDone
			}
			action, mapped := mapSessionAction(kind)
			if !mapped {
				continue
			}
			c.logger.Info("セッション状態が変化した", "action", action)
			c.emitter.Emit(rawlog.EventSession, time.Now(), nil, rawlog.SessionPayload{Action: action})
			if action == rawlog.SessionResume && c.onResume != nil {
				c.onResume()
			}
		}
	}
}

// mapSessionAction は観測した種別を生ログの記録種別へ写す。
//
// winapi・linuxapi の列挙はどちらも rawlog.SessionAction と同じ文字列を使う。
// 知らない種別は false を返して捨てる。収集側が増えた種別を、
// 取り込み側が知らないまま記録してしまうのを防ぐ。
func mapSessionAction[K ~string](kind K) (rawlog.SessionAction, bool) {
	action := rawlog.SessionAction(kind)
	switch action {
	case rawlog.SessionLogon, rawlog.SessionUnlock, rawlog.SessionLock,
		rawlog.SessionLogoff, rawlog.SessionShutdown, rawlog.SessionSuspend, rawlog.SessionResume:
		return action, true
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
