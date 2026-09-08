package linuxapi

import "errors"

// InputKind は操作として扱う入力の種別。
//
// マウス移動は含めない（要件 §6.2）。値は winapi.InputKind と同じ文字列で、
// そのまま生ログの kind になる。
type InputKind string

const (
	InputClick InputKind = "click"
	InputWheel InputKind = "wheel"
	InputKey   InputKind = "key"
)

// SessionEventKind はセッションの変化。
//
// 値は rawlog.SessionAction と同じ文字列にする。collect 側の mapSessionAction が
// 文字列として写すため、ここがずれると黙って記録されなくなる。
//
// Linux では logon / logoff を出さない。ログインの開始と終了に対応するのは
// collector_start / collector_stop で、そちらは sessionCollector が自前で出す。
// logind の Active プロパティを logon/logoff へ写すと、仮想端末を切り替える
// たびに偽のセッション断が生まれる。
type SessionEventKind string

const (
	SessionLock     SessionEventKind = "lock"
	SessionUnlock   SessionEventKind = "unlock"
	SessionSuspend  SessionEventKind = "suspend"
	SessionResume   SessionEventKind = "resume"
	SessionShutdown SessionEventKind = "shutdown"
)

// ForegroundWindow は前面ウィンドウの情報。winapi.ForegroundWindow と同じ形。
type ForegroundWindow struct {
	// AppName は実行ファイル名、または app_id / WM_CLASS。
	AppName string
	// AppDisplayName は .desktop の Name=。取れなければ空。
	AppDisplayName string
	// WindowTitle はウィンドウの完全なタイトル。
	// gkill へは出さないが生ログには残す（要件 §6.4）。
	WindowTitle string
	// ProcessPath は実行ファイルの絶対パス。取れなければ空。
	ProcessPath string
	// PID はプロセスID。取れなければ 0。
	PID uint32
}

// ErrWlanPermissionDenied は Wi-Fi の情報を見る権限が無いことを表す。
//
// winapi と同じ名前・同じ意味にしてある。呼び出し側は「取得に失敗した」と
// 「つながっていない」を区別する必要があり、前者を切断として扱うと
// 偽の切断イベントが生ログに残る。
var ErrWlanPermissionDenied = errors.New("wlan access denied")

// ErrSSIDUnknown は Wi-Fi につながってはいるが SSID を読めないことを表す。
//
// 隠し SSID や、接続直後でアクセスポイントの情報がまだ埋まっていないときに起きる。
// これを「未接続」として返してはいけない。つながったままなのに
// 切断イベントが出て、接続区間が途中で割れる。
var ErrSSIDUnknown = errors.New("connected but ssid is unknown")

// ErrUnavailable はその情報を取る手段がこの環境に無いことを表す。
//
// 一時的な失敗と違い、何度やり直しても変わらない。呼び出し側は
// 起動時に1度だけ警告し、以後その種別の観測をやめてよい。
var ErrUnavailable = errors.New("no backend available")
