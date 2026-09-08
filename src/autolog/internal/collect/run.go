package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"errors"
	"log/slog"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"golang.org/x/sync/errgroup"
)

// Run は常駐して端末の操作ログを収集する。ctx が終わるまで返らない。
//
// 収集するもの:
//   - 操作を伴うアクティブウィンドウ（クリック・ホイール・キー入力のみ）
//   - ロック・解除・ログオン・ログオフ・電源
//   - Wi-Fi・Bluetooth・充電の接続状態
//
// 何をどう観測するかは platform_*.go が決める。この関数は
// 「どのコレクタを回すか」を受け取って回すだけで、OS を知らない。
//
// Chrome 拡張と Android からの受信は ingest パッケージが担当する。
func Run(ctx context.Context, store *rawlog.Store, opts Options, logger *slog.Logger) error {
	if err := validateDevice(opts.Device); err != nil {
		return err
	}

	emitter := NewEmitter(store, opts.Device, logger)

	// プラットフォームの判定を recoverUnfinishedSession より先に行う。
	// 逆にすると、収集できない環境 (Android・macOS) の autolog collect が
	// 偽の recovered lock を1件書いてからエラー終了する。
	collectorFns, err := newPlatformCollectors(emitter, opts, logger)
	if err != nil {
		return err
	}

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

	collectors, collectorsCtx := errgroup.WithContext(ctx)
	for _, fn := range collectorFns {
		collectors.Go(func() error {
			return fn(collectorsCtx)
		})
	}

	logger.Info("収集を開始した", "device", opts.Device, "raw_db", store.Path())

	err = collectors.Wait()
	stopEmitter()
	if emitterErr := <-emitterDone; emitterErr != nil && !errors.Is(emitterErr, context.Canceled) {
		err = errors.Join(err, emitterErr)
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
