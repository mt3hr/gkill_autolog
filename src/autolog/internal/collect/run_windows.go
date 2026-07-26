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

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		return emitter.Run(groupCtx)
	})
	group.Go(func() error {
		return newWindowCollector(emitter, logger).run(groupCtx)
	})
	group.Go(func() error {
		return newSessionCollector(emitter, logger).run(groupCtx)
	})
	group.Go(func() error {
		return newNetPowerCollector(emitter, logger).run(groupCtx)
	})

	logger.Info("収集を開始した", "device", device, "raw_db", store.Path())

	err := group.Wait()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
