//go:build windows

package winapi

import "unsafe"

var (
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procShowWindow            = user32.NewProc("ShowWindow")
)

// swHide は ShowWindow に渡す「隠す」指定。
const swHide = 0

// HideOwnConsoleWindow は、このプロセスが自分だけで持っているコンソールを隠す。
// 隠したときに true を返す。
//
// タスクスケジューラからログオン時に起動すると、対話セッションで動くために
// コンソールウィンドウ（黒い窓）が出たままになる。これを消したくて
// 「ユーザーがログオンしているかどうかにかかわらず実行する」にすると、
// 今度はセッション0で動くことになり、入力デスクトップを開けなくなる。
// その結果 IsSessionLocked が常に true を返し、スクリーンショットも
// 前面ウィンドウも記録されなくなる。実際にこれで2日ぶん記録が止まった。
//
// 対話セッションのまま窓だけ消すのが正しいので、ここで自分の窓を隠す。
//
// **既にあるターミナルから起動されたときは隠さない。**
// その場合 GetConsoleWindow が返すのは呼び出した側のターミナルの窓で、
// 隠すと利用者のターミナルごと消えてしまう。
// GetConsoleProcessList が返す接続プロセス数が 1 のとき、
// つまりこのコンソールを使っているのが自分だけのときに限って隠す。
func HideOwnConsoleWindow() bool {
	window, _, _ := procGetConsoleWindow.Call()
	if window == 0 {
		// コンソールを持っていない。窓も無いので何もしない。
		return false
	}

	// 1個ぶんで足りるが、実際の数を知りたいので少し多めに渡す。
	// 戻り値は必要な数なので、配列に収まらなくても本当の数が分かる。
	var pids [4]uint32
	count, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)),
	)
	if count != 1 {
		// 他のプロセスも同じコンソールを使っている。
		// ターミナルから起動されたということなので隠さない。
		return false
	}

	procShowWindow.Call(window, swHide)
	return true
}
