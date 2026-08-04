package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/collect"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/ingest"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/proclock"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/shot"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func newCollectCmd() *cobra.Command {
	var (
		logLevel           string
		noScreenshot       bool
		screenshotInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "collect",
		Short: "常駐して操作ログを収集する",
		Long: "常駐して Windows の操作ログを収集する。\n" +
			"Ctrl-C またはログオフで停止し、停止時にはセッション終了を生ログへ記録する。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.EnsureDirs(); err != nil {
				return err
			}

			// 収集の多重起動を防ぐ。raw.db に触る前に取ること。
			// 2個目のインスタンスは、稼働中インスタンスの開きっぱなしセッションを
			// 「前回の異常終了」と誤認して偽の recovered lock を書き、
			// さらに collector_start / collector_stop を書いてから
			// Chrome の受け口のポート衝突で死ぬ。利用セッションが分断され、
			// 接続区間も観測の切れ目として閉じられてしまう。
			lock, err := proclock.Acquire(filepath.Join(cfg.Home, "collect.lock"))
			if errors.Is(err, proclock.ErrBusy) {
				return fmt.Errorf("収集が既に常駐しているため起動しない。2個目を動かすと稼働中の記録が壊れる: %w", err)
			}
			if err != nil {
				return err
			}
			defer func() { _ = lock.Release() }()

			// フラグでの指定は設定より優先する。
			// 一度だけ間隔を変えて試したいときに設定ファイルを書き換えずに済む。
			if cmd.Flags().Changed("screenshot-interval") {
				cfg.ScreenshotInterval = config.ClampScreenshotInterval(screenshotInterval)
			}

			logger, err := newLogger(logLevel)
			if err != nil {
				return err
			}

			// ログオン時のタスクから起動されると黒い窓が出たままになる。
			// 窓を消すためにセッション0で動かすと画面が取れなくなるので、
			// 対話セッションのまま窓だけ隠す。
			hideConsoleIfLaunchedByScheduler(logger)

			store, err := rawlog.OpenStore(cfg.RawDBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			ingestToken, err := cfg.ResolveIngestToken()
			if err != nil {
				return err
			}
			if cfg.IngestToken != "" {
				// 環境変数や config.env で指定されている。ファイルは読まれないので、
				// 古い ingest_token.txt を案内すると Chrome 拡張の設定を誤らせる。
				logger.Info("共有トークンは設定 (" + config.EnvIngestToken + ") の値を使う")
			} else {
				logger.Info("共有トークンの保存先", "chrome", cfg.TokenPath("ingest_token.txt"))
			}

			// Ctrl-C とログオフで後片付けをしてから終わる。
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			group, groupCtx := errgroup.WithContext(ctx)
			group.Go(func() error {
				return collect.Run(groupCtx, store, cfg.Device, logger)
			})
			if noScreenshot {
				logger.Info("スクリーンショットの定期撮影を行わない")
			} else {
				group.Go(func() error {
					return shot.Run(groupCtx, cfg, logger)
				})
			}
			group.Go(func() error {
				return ingest.Run(groupCtx, store, ingest.Options{
					LocalDevice: cfg.Device,
					ChromeAddr:  cfg.IngestAddr,
					ChromeToken: ingestToken,
				}, logger)
			})
			return group.Wait()
		},
	}

	cmd.Flags().StringVar(&logLevel, "log", "info", "ログレベル (debug, info, warn, error)")
	cmd.Flags().BoolVar(&noScreenshot, "no-screenshot", false,
		"定期スクリーンショットを撮らない。タスクスケジューラから autolog screenshot を回す場合に使う")
	cmd.Flags().DurationVar(&screenshotInterval, "screenshot-interval", config.DefaultScreenshotInterval,
		"スクリーンショットの撮影間隔。指定すると "+config.EnvScreenshotInterval+" より優先する")
	return cmd
}

// newLogger は標準エラーへ出力するロガーを作る。
func newLogger(level string) (*slog.Logger, error) {
	var slogLevel slog.Level
	if err := slogLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, err
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel})
	return slog.New(handler), nil
}
