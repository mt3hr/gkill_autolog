// Command autolog は gkill_autolog の収集・正規化・書き込みを行う CLI。
//
// サブコマンド:
//
//	collect     常駐して Windows の操作ログを収集し、Chrome 拡張・Android からの受信も行う
//	screenshot  スクリーンショットを1枚撮影して保存する
//	import      生ログを整理してこの端末の gkill へ取り込む
//	status      生ログの蓄積状況と処理カーソルを表示する
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/spf13/cobra"
)

func main() {
	// 何より先にタイムゾーンを決める。
	// Android では Go が time.Local を UTC に固定してしまい、
	// 記録した時刻が PC 側の +09:00 と混ざる。
	if err := config.InitLocalTimezone(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	rootCmd := &cobra.Command{
		Use:           "autolog",
		Short:         "gkill 自動操作ログ収集システム",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddCommand(
		newCollectCmd(),
		newImportCmd(),
		newScreenshotCmd(),
		newStatusCmd(),
	)

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
