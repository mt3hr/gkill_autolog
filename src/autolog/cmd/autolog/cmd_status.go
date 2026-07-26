package main

import (
	"fmt"
	"slices"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var (
		since string
		dump  bool
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "生ログの蓄積状況と処理カーソルを表示する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			store, err := rawlog.OpenStore(cfg.RawDBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			from, err := parseSince(since)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			counts, err := store.Count(ctx, rawlog.RangeQuery{From: from})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "home:   %s\n", cfg.Home)
			fmt.Fprintf(out, "raw.db: %s\n", store.Path())
			fmt.Fprintf(out, "device: %s\n", cfg.Device)
			fmt.Fprintf(out, "since:  %s\n\n", rawlog.FormatTime(from))

			types := make([]rawlog.EventType, 0, len(counts))
			for t := range counts {
				types = append(types, t)
			}
			slices.SortFunc(types, func(a, b rawlog.EventType) int {
				if counts[a] != counts[b] {
					return counts[b] - counts[a]
				}
				if a < b {
					return -1
				}
				if a > b {
					return 1
				}
				return 0
			})

			total := 0
			for _, t := range types {
				fmt.Fprintf(out, "  %-14s %6d\n", t, counts[t])
				total += counts[t]
			}
			fmt.Fprintf(out, "  %-14s %6d\n", "(合計)", total)

			if dump {
				events, err := store.Range(ctx, rawlog.RangeQuery{From: from})
				if err != nil {
					return err
				}
				fmt.Fprintln(out, "\n--- イベント ---")
				for _, e := range events {
					endTime := "-"
					if e.EndTime != nil {
						endTime = rawlog.FormatTime(*e.EndTime)
					}
					fmt.Fprintf(out, "%s  %-14s %-20s %s\n",
						rawlog.FormatTime(e.StartTime), e.EventType, endTime, e.Payload)
				}
			}

			cursor, ok, err := store.GetCursor(ctx, rawlog.CursorNormalize)
			if err != nil {
				return err
			}
			fmt.Fprintln(out)
			if ok {
				fmt.Fprintf(out, "cursor(%s): %s\n", rawlog.CursorNormalize, rawlog.FormatTime(cursor))
			} else {
				fmt.Fprintf(out, "cursor(%s): (未設定)\n", rawlog.CursorNormalize)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "24h", "集計開始位置。期間 (24h, 7d) か RFC3339 の時刻")
	cmd.Flags().BoolVar(&dump, "dump", false, "件数だけでなくイベントの中身も表示する")
	return cmd
}

// parseSince は --since の値を絶対時刻へ変換する。
func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := rawlog.ParseTime(s); err == nil {
		return t, nil
	}
	// 日数指定は time.ParseDuration が受け付けないため先に展開する。
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &days); err == nil {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse --since %q: %w", s, err)
	}
	return time.Now().Add(-d), nil
}
