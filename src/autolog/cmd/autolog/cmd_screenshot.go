package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/shot"
	"github.com/spf13/cobra"
)

func newScreenshotCmd() *cobra.Command {
	var at string

	cmd := &cobra.Command{
		Use:   "screenshot",
		Short: "スクリーンショットを1枚撮影して保存する",
		Long: "スクリーンショットを1枚撮影して保存する。\n" +
			"通常は autolog collect が毎時00分に自動で撮るため、これは動作確認用。\n" +
			"タスクスケジューラから毎時実行させる構成にする場合は collect に --no-screenshot を付ける。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			capturedAt := time.Now()
			if at != "" {
				capturedAt, err = time.ParseInLocation("2006-01-02T15:04:05", at, time.Local)
				if err != nil {
					return fmt.Errorf("failed to parse --at %q: %w", at, err)
				}
			}

			result, err := shot.Capture(cfg, capturedAt)
			if errors.Is(err, shot.ErrLocked) {
				fmt.Fprintln(cmd.OutOrStdout(), "画面がロックされているため撮影しなかった")
				return nil
			}
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "保存した: %s (%dx%d, %.1f MiB, mtime=%s)\n",
				result.Path, result.Width, result.Height,
				float64(result.Bytes)/(1<<20), result.CapturedAt.Format(time.RFC3339))
			return nil
		},
	}

	cmd.Flags().StringVar(&at, "at", "",
		"撮影時刻として記録する日時 (2006-01-02T15:04:05)。既定は現在時刻。"+
			"動作確認用。実運用で過去や未来の時刻を指定しない（撮り逃した時間の補完はしない約束）")
	return cmd
}
