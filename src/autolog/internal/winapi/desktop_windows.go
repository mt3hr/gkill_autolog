//go:build windows

package winapi

var (
	procOpenInputDesktop = user32.NewProc("OpenInputDesktop")
	procCloseDesktop     = user32.NewProc("CloseDesktop")
)

// desktopSwitchDesktop は OpenInputDesktop に渡すアクセス権。
const desktopSwitchDesktop = 0x0100

// IsSessionLocked は画面がロックされているか（安全なデスクトップが前面か）を返す。
//
// ロック中は入力デスクトップが Winlogon の安全なデスクトップへ切り替わり、
// 対話ユーザとして動くプロセスからは OpenInputDesktop が失敗する。
// スクリーンショットを撮らない判定に使う（要件 §10）。
//
// セッションの開始・終了そのものは SessionWatcher の通知を正とする。
// こちらは撮影直前の確認用。
func IsSessionLocked() bool {
	desktop, _, _ := procOpenInputDesktop.Call(0, 0, desktopSwitchDesktop)
	if desktop == 0 {
		return true
	}
	procCloseDesktop.Call(desktop)
	return false
}
