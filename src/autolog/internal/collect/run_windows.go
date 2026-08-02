//go:build windows

package collect

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"golang.org/x/sync/errgroup"
)

// Run は常駐して Windows の操作ログを収集する。ctx が終わるまで返らない。
//
// 収集するもの:
//   - 操作を伴うアクティブウィンドウ（クリック・ホイール・キー入力のみ）
//   - ロック・解除・ログオン・ログオフ・電源
//   - Wi-Fi・Bluetooth・充電の接続状態
//
// Chrome 拡張と Android からの受信は ingest パッケージが担当する。
func Run(ctx context.Context, store *rawlog.Store, device rawlog.Device, logger *slog.Logger) error {
	if err := validateDevice(device); err != nil {
		return err
	}

	emitter := NewEmitter(store, device, logger)

	// 前回が異常終了していれば、先に未終了セッションを閉じておく。
	if err := recoverUnfinishedSession(ctx, store, emitter, logger); err != nil {
		// 復元に失敗しても収集は続ける。生ログは残っているので後から手当てできる。
		logger.Error("未終了セッションの補完に失敗した", "error", err)
	}

	// 終了は2段階にする。コレクタと emitter を同じ ctx で止めると、
	// emitter が残りを書き出して返った**あと**にコレクタが collector_stop や
	// 最終入力を Emit することがあり、それらがキューに残ったまま消える。
	// コレクタが全員終わってから emitter を止めれば取りこぼさない。
	emitterCtx, stopEmitter := context.WithCancel(context.WithoutCancel(ctx))
	defer stopEmitter()
	emitterDone := make(chan error, 1)
	go func() {
		emitterDone <- emitter.Run(emitterCtx)
	}()

	// スリープ復帰をセッションの収集から接続の収集へ伝える。
	// suspend で normalize が接続区間を閉じるため、復帰後は状態を取り直して
	// 「つながっているもの」を記録し直す必要がある。ポーリング間隔からの
	// 自前検知 (sleepGapThreshold) では30秒未満のスリープを取りこぼす。
	netPower := newNetPowerCollector(emitter, logger)
	session := newSessionCollector(emitter, logger)
	session.onResume = netPower.requestReobserve

	collectors, collectorsCtx := errgroup.WithContext(ctx)
	collectors.Go(func() error {
		return newWindowCollector(emitter, logger).run(collectorsCtx)
	})
	collectors.Go(func() error {
		return session.run(collectorsCtx)
	})
	collectors.Go(func() error {
		return netPower.run(collectorsCtx)
	})

	logger.Info("収集を開始した", "device", device, "raw_db", store.Path())

	err := collectors.Wait()
	stopEmitter()
	if emitterErr := <-emitterDone; emitterErr != nil && !errors.Is(emitterErr, context.Canceled) {
		err = errors.Join(err, emitterErr)
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
