package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/collect"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/ingest"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/shot"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

func newCollectCmd() *cobra.Command {
	var (
		logLevel     string
		noScreenshot bool
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

			logger, err := newLogger(logLevel)
			if err != nil {
				return err
			}

			store, err := rawlog.OpenStore(cfg.RawDBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			ingestToken, err := cfg.ResolveIngestToken()
			if err != nil {
				return err
			}
			logger.Info("共有トークンの保存先", "chrome", cfg.TokenPath("ingest_token.txt"))

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
					return shot.RunHourly(groupCtx, cfg, logger)
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
		"毎時00分のスクリーンショットを撮らない。タスクスケジューラから autolog screenshot を回す場合に使う")
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
