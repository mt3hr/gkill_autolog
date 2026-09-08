package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"errors"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// ErrUnsupportedPlatform はこの環境では収集できないことを表す。
//
// 一時的な失敗 (画面に繋がらない、D-Bus が落ちている) とは区別する。
// こちらは何度やり直しても変わらないので、呼び出し側は常駐を諦めてよい。
var ErrUnsupportedPlatform = errors.New("autolog collect is not supported on this platform")

// Options は収集の設定。
//
// config パッケージを import せずに済ませるため、必要な値だけを受け取る。
// collect のテストが config.Load の環境解決に巻き込まれないようにする狙いもある。
type Options struct {
	// Device は収集元の端末名。
	Device rawlog.Device

	// Linux は Linux でだけ使う設定。他のプラットフォームでは無視される。
	Linux LinuxOptions
}

// LinuxOptions は Linux の収集手段に関する設定。
//
// どれも既定は空で、空なら環境から手段を導く。特定環境の決め打ちは置かない。
type LinuxOptions struct {
	// WindowCommand は前面ウィンドウを JSON で出すコマンド。
	// 標準の手段でウィンドウを取れないデスクトップ環境のための逃げ道。
	WindowCommand string

	// InputMethod は入力の観測手段。auto / evdev / x11 / none。
	InputMethod string

	// InputDevices は使う /dev/input/event* を固定するときに指定する。
	InputDevices []string

	// SSIDCommand は SSID を1行で出すコマンド。
	SSIDCommand string
}

// collectorFunc は1つの収集を回す。ctx が終わるまで返らない。
type collectorFunc func(context.Context) error

// foregroundWindow は前面ウィンドウの情報。
//
// winapi.ForegroundWindow / linuxapi.ForegroundWindow を collect 側へ翻訳したもの。
// 翻訳は platform_*.go が行い、collect のロジックはどちらも知らない。
type foregroundWindow struct {
	AppName        string
	AppDisplayName string
	WindowTitle    string
	ProcessPath    string
	PID            uint32
}

// foregroundWindowFunc は前面ウィンドウを取得する。
// 2つ目の戻り値が false のときは前面ウィンドウが無い (ロック中など)。
type foregroundWindowFunc func() (foregroundWindow, bool, error)

// eventSource は入力フックとセッション監視に共通の形。
//
// 要素型を型パラメータにしてあるのは、winapi と linuxapi が
// それぞれ自分の列挙型でチャネルを持つため。変換用のゴルーチンを挟むと
// 「Events() が閉じたことの検出」と「Run の終了待ち」という
// 既存の停止契約が1段ぶん遠くなるので、型で吸収する。
type eventSource[K ~string] interface {
	// Events は観測したものを流す。Run が終わるときに閉じる。
	Events() <-chan K
	// Run は観測を回す。ctx が終わるまで返らない。
	Run(ctx context.Context) error
}

// netPowerProbes は接続状態の観測関数。
//
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
// 「取れなかった」を「切断」として扱わないための約束で、
// 失敗したときのログの文言はプラットフォームごとに違うため、
// ここへ渡す時点で警告まで済ませてある関数を受け取る。
type netPowerProbes struct {
	ssid      func() (string, bool)
	bluetooth func() ([]string, bool)
	charging  func() (bool, bool)
}
