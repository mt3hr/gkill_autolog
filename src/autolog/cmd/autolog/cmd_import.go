package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/gkillclient"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/inbox"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/ledger"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/normalize"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/proclock"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	var (
		dryRun     bool
		untilNow   bool
		logLevel   string
		cutoffFlag string
		sinceFlag  string
		dumpDir    string
	)

	cmd := &cobra.Command{
		Use:   "import",
		Short: "生ログを整理してローカルの gkill へ取り込む",
		Long: "生ログを Kyou へ変換し、この端末の gkill サーバへ直接書き込む。\n" +
			"normalize と write を1回で行うため、中間ファイルを介さない。\n" +
			"書き込みに成功した分だけ台帳へ記録し、処理カーソルを進める。\n" +
			"URL の取捨は閾値と $AUTOLOG_HOME/url_denylist.txt だけで決まる。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.EnsureDirs(); err != nil {
				return err
			}

			// 取り込みの多重起動を防ぐ。
			// 台帳の既書き込み判定は起動時のスナップショットで、Kyou の ID は
			// 書き込みごとに採番される。並行して走ると両方が「未書き込み」と
			// 判定して同じ区間の Kyou を二重に登録してしまう。
			// (午前4時のタスクと手動実行・同期スクリプトの重なりが現実に起きる)
			lock, err := proclock.Acquire(filepath.Join(cfg.Home, "import.lock"))
			if errors.Is(err, proclock.ErrBusy) {
				return fmt.Errorf("別の取り込みが実行中のため中止した。並行して走らせると同じ Kyou が二重に登録される: %w", err)
			}
			if err != nil {
				return err
			}
			defer func() { _ = lock.Release() }()

			logger, err := newLogger(logLevel)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			store, err := rawlog.OpenStore(cfg.RawDBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			// 収集アプリが共有ディレクトリへ置いた分を先に取り込む（Android）。
			// Windows では共有ディレクトリを使わないので何もしない。
			inboxStats, err := inbox.Ingest(ctx, store, inbox.Options{
				Dir:            cfg.InboxDir(),
				AllowedDevices: cfg.AllowedDevices,
				DefaultDevice:  cfg.Device,
				// dry-run では受け口のファイルを消さない。
				// 生ログの取り込み自体は冪等なので消しても失われはしないが、
				// 「試しただけ」のつもりで手元のファイルが消えるのは意図に反する。
				KeepFiles: dryRun,
			}, logger)
			if err != nil {
				return err
			}
			if inboxStats.Files > 0 {
				fmt.Fprintf(out, "受け口:   %d ファイル / %d 件読み込み / %d 件追加 / %d 件除外\n",
					inboxStats.Files, inboxStats.Read, inboxStats.Inserted, inboxStats.Skipped)
			}

			cutoff, err := resolveImportCutoff(cutoffFlag, untilNow)
			if err != nil {
				return err
			}
			from, err := resolveFrom(ctx, store, sinceFlag)
			if err != nil {
				return err
			}

			events, err := store.Range(ctx, rawlog.RangeQuery{From: from, To: cutoff})
			if err != nil {
				return err
			}

			denyList, denyPath, err := loadDenyList(cfg)
			if err != nil {
				return err
			}
			notificationDenyList, notificationDenyPath, err := loadNotificationDenyList(cfg)
			if err != nil {
				return err
			}

			// 前回から持ち越した区間を渡す。
			// これが無いと、開始イベントが窓の外に出た時点で接続区間を作れなくなり、
			// アプリ利用やメディア再生は窓の切れ目で細切れになる。
			openStates, err := store.LoadOpenStates(ctx)
			if err != nil {
				return err
			}
			// 前回までに記録した通知の内容を渡す。
			// これが無いと、窓の切れ目をまたいだ同内容の再通知がすり抜ける。
			notificationSeen, err := store.LoadNotificationSeen(ctx)
			if err != nil {
				return err
			}

			result, err := normalize.Run(events, normalize.Options{
				Cutoff:               cutoff,
				DenyList:             denyList,
				NotificationDenyList: notificationDenyList,
				OpenStates:           openStates,
				NotificationSeen:     notificationSeen,
				UsageTitle:           cfg.UsageTitle,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "対象期間: %s 〜 %s\n", rawlog.FormatTime(from), rawlog.FormatTime(cutoff))
			fmt.Fprintf(out, "生ログ:   %d 件\n", len(events))
			fmt.Fprintf(out, "提案:     %d 件\n", len(result.Proposals))
			fmt.Fprintf(out, "  URL の除外パターン:  %d 個 (%s)\n", denyList.Len(), denyPath)
			fmt.Fprintf(out, "  通知の除外パターン:  %d 個 (%s)\n", notificationDenyList.Len(), notificationDenyPath)
			fmt.Fprintln(out)

			// 中身を確かめたいときのために、提案をそのまま書き出せるようにしておく。
			if dumpDir != "" {
				path := filepath.Join(dumpDir, proposalsFileName)
				if err := writeJSONL(path, result.Proposals); err != nil {
					return err
				}
				fmt.Fprintf(out, "提案を書き出した: %s\n\n", path)
			}

			ledgerDB, err := ledger.Open(cfg.LedgerDBPath())
			if err != nil {
				return err
			}
			defer ledgerDB.Close()

			var resolve gkillclient.ClientResolver
			if !dryRun {
				resolve = newClientResolver(ctx, cfg, logger)
			}

			writer := gkillclient.NewWriter(resolve, ledgerDB, logger, gkillclient.WriteOptions{
				DryRun:       dryRun,
				DescribeUser: cfg.GkillUserFor,
			})
			stats, err := writer.WriteAll(ctx, result.Proposals, nil)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "  書き込み:   %d 件\n", stats.Written)
			fmt.Fprintf(out, "  台帳で除外: %d 件\n", stats.Skipped)
			fmt.Fprintf(out, "  失敗:       %d 件\n", stats.Failed)
			fmt.Fprintf(out, "付与したタグ: %d 件\n", stats.TagsAdded)

			if dryRun {
				fmt.Fprintln(out, "\n(dry-run のため実際には書き込んでいない。カーソルも進めない)")
				return nil
			}

			cursor := pullBackCursor(result.SafeCursor, result.Proposals, stats.FailedProposalIDs)

			// まだ確定していない区間を次回へ渡す。
			// カーソルの更新より先に保存する。ここで落ちても、次回は
			// 同じ範囲を読み直すだけで区間を失わない。
			if err := store.SaveOpenStates(ctx, result.OpenStates); err != nil {
				return err
			}
			// 通知の重複判定に使う記録も次回へ渡す。
			// 判定の窓を過ぎたものは捨てる。残しても使わないので溜める意味がない。
			// ただし基準は cutoff ではなくカーソルにする。カーソルより後の通知は
			// 引き戻しで読み直されるので、その再判定に使う分まで捨てると
			// 一度抑えた再通知が Kmemo として復活してしまう。
			expireBefore := cursor
			if expireBefore.IsZero() {
				expireBefore = result.SafeCursor
			}
			if err := store.SaveNotificationSeen(ctx, result.NotificationSeen, expireBefore.Add(-normalize.NotificationDedupeWindow)); err != nil {
				return err
			}

			if cursor.IsZero() {
				fmt.Fprintln(out, "\nカーソルは更新しない")
				return nil
			}
			if err := store.SetCursor(ctx, rawlog.CursorNormalize, cursor); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nカーソルを %s まで進めた\n", rawlog.FormatTime(cursor))
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "gkill を呼ばず、書き込む予定の内容だけを表示する")
	cmd.Flags().BoolVar(&untilNow, "until-now", false,
		"既定の午前4時ではなく、いまの2分手前までを処理する (結合窓を跨ぐ確定を防ぐための猶予)")
	cmd.Flags().StringVar(&logLevel, "log", "info", "ログレベル (debug, info, warn, error)")
	cmd.Flags().StringVar(&cutoffFlag, "cutoff", "", "この時刻までを処理する (RFC3339)。既定は直近の午前4時")
	cmd.Flags().StringVar(&sinceFlag, "since", "", "開始位置 (RFC3339)。既定は前回の処理カーソル")
	cmd.Flags().StringVar(&dumpDir, "dump-proposals", "", "提案を jsonl として書き出すディレクトリ")
	return cmd
}

// newClientResolver は端末ごとの gkill 接続を返す関数を作る。
//
// 端末別ユーザー（<接頭辞>+<端末名>）へ書き分けるため、接続も端末ごとに要る。
// 実際に書き込む端末の分だけ遅延して作る。
//
// 接続を作った時点でログインまで済ませる。ログインの成否を最初の1件で確定させ、
// 失敗を覚えておくことで、提案ごとにログインを繰り返さないようにする
// （gkill のログイン試行は IP ごとに 15 分で 10 回まで）。
func newClientResolver(ctx context.Context, cfg *config.Config, logger *slog.Logger) gkillclient.ClientResolver {
	type entry struct {
		client *gkillclient.Client
		err    error
	}
	entries := map[rawlog.Device]entry{}

	return func(device rawlog.Device) (*gkillclient.Client, error) {
		if e, ok := entries[device]; ok {
			return e.client, e.err
		}

		user := cfg.GkillUserFor(device)
		if user == "" {
			err := fmt.Errorf("端末 %q の書き込み先ユーザーが決まらない", device)
			entries[device] = entry{err: err}
			return nil, err
		}

		client, err := gkillclient.New(gkillclient.Options{
			BaseURL:        cfg.GkillBaseURL,
			User:           user,
			PasswordSHA256: cfg.GkillPasswordFor(device),
			Device:         device,
			Insecure:       cfg.GkillInsecure,
		})
		if err != nil {
			err = fmt.Errorf("端末 %q 用の接続を作れない (gkill user %s): %w", device, user, err)
			entries[device] = entry{err: err}
			return nil, err
		}

		if err := client.Login(ctx); err != nil {
			err = fmt.Errorf("端末 %q の書き込み先 (gkill user %s) へログインできない: %w", device, user, err)
			logger.Error("ログインできないため、この端末の分は書き込まない",
				"device", device, "gkill_user", user, "error", err)
			entries[device] = entry{err: err}
			return nil, err
		}

		logger.Info("gkill へ書き込む", "device", device, "gkill_user", user, "base_url", cfg.GkillBaseURL)
		entries[device] = entry{client: client}
		return client, nil
	}
}

// untilNowCutoffLag は --until-now のときに「いま」から引く猶予。
//
// 接続系 (Wi-Fi・Bluetooth・充電) やウィンドウの結合は「次のイベント」を見て
// 初めて判定される。直近ちょうどまで処理すると、切断・切替の直後が
// 結合相手を待たずに確定しうる。最長のマージ窓 (1分) を確実に超える値にし、
// run_import.ps1 が自前で持つ 2 分と揃える。直近 2 分は次回にまわるだけで失われない。
const untilNowCutoffLag = 2 * time.Minute

// resolveImportCutoff は取り込みの上限時刻を決める。
func resolveImportCutoff(flag string, untilNow bool) (time.Time, error) {
	if untilNow {
		if flag != "" {
			return time.Time{}, fmt.Errorf("--until-now と --cutoff は同時に指定できない")
		}
		return time.Now().Add(-untilNowCutoffLag), nil
	}
	return resolveCutoff(flag)
}

// pullBackCursor は次回の開始位置を決める。
//
// normalize が出した安全な位置を上限としつつ、
// 書き込めなかったものが再処理されるよう、その手前まで引き戻す。
func pullBackCursor(safeCursor time.Time, proposals []normalize.Proposal, failedProposalIDs []string) time.Time {
	if len(failedProposalIDs) == 0 {
		return safeCursor
	}

	failed := map[string]struct{}{}
	for _, id := range failedProposalIDs {
		failed[id] = struct{}{}
	}

	cursor := safeCursor
	for _, proposal := range proposals {
		if _, ok := failed[proposal.ID]; !ok {
			continue
		}
		if at := proposalTime(proposal); !at.IsZero() && at.Before(cursor) {
			cursor = at
		}
	}
	return cursor
}
