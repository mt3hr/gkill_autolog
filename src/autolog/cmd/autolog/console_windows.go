//go:build windows

package main

import (
	"log/slog"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

// hideConsoleIfLaunchedByScheduler は、タスクスケジューラから起動されたときに
// 出たままになるコンソールウィンドウを隠す。
//
// 窓を消したいからと「ユーザーがログオンしているかどうかにかかわらず実行する」に
// してはいけない。セッション0で動くようになり、入力デスクトップを開けなくなるので、
// スクリーンショットも前面ウィンドウも記録されなくなる。
// 対話セッションのまま窓だけ隠すのが正しい。
//
// ターミナルから手で起動したときは隠さない（winapi 側で判定している）。
func hideConsoleIfLaunchedByScheduler(logger *slog.Logger) {
	if winapi.HideOwnConsoleWindow() {
		logger.Info("コンソールウィンドウを隠した")
	}
}
