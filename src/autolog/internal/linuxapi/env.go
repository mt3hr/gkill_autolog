package linuxapi

import (
	"os"
	"strings"
)

// Environment はこのプロセスが置かれているセッションの情報。
//
// 何を観測できるかはここで決まる。判定を1か所にまとめてあるのは、
// 種別ごとにばらばらに環境変数を見ると、片方だけが Wayland だと思って
// 動くような食い違いが起きるため。
type Environment struct {
	// SessionType は XDG_SESSION_TYPE。x11 / wayland / tty など。
	SessionType string
	// Display は $DISPLAY。Wayland でも XWayland があれば入っている。
	Display string
	// WaylandSocket は $WAYLAND_DISPLAY。
	WaylandSocket string
	// Compositor は環境変数から推した合成器の名前。sway / hyprland / gnome / kde / unknown。
	Compositor string
	// SwaySocket は sway / i3 の IPC ソケット。
	SwaySocket string
	// HyprlandSignature は Hyprland の実行ごとの識別子。
	HyprlandSignature string
	// LogindSession は自分が属する logind のセッションID。取れなければ空。
	LogindSession string
}

const (
	compositorSway     = "sway"
	compositorHyprland = "hyprland"
	compositorUnknown  = "unknown"
)

// DetectEnvironment は環境変数からセッションの情報を読む。
func DetectEnvironment() Environment {
	return environmentFrom(os.Getenv)
}

// environmentFrom は環境変数の読み手を差し替えられる形の DetectEnvironment。
func environmentFrom(getenv func(string) string) Environment {
	env := Environment{
		SessionType:       strings.ToLower(strings.TrimSpace(getenv("XDG_SESSION_TYPE"))),
		Display:           strings.TrimSpace(getenv("DISPLAY")),
		WaylandSocket:     strings.TrimSpace(getenv("WAYLAND_DISPLAY")),
		SwaySocket:        strings.TrimSpace(getenv("SWAYSOCK")),
		HyprlandSignature: strings.TrimSpace(getenv("HYPRLAND_INSTANCE_SIGNATURE")),
		LogindSession:     strings.TrimSpace(getenv("XDG_SESSION_ID")),
	}
	if env.SwaySocket == "" {
		env.SwaySocket = strings.TrimSpace(getenv("I3SOCK"))
	}
	env.Compositor = detectCompositor(env, getenv)
	return env
}

// detectCompositor は合成器の名前を推す。
//
// 取れた名前をそのまま返すだけで、名前から振る舞いを決め打ちしない。
// 使いみちは「どの手段が使えるか」を選ぶことと、警告に載せて
// 利用者が状況を分かるようにすること。
func detectCompositor(env Environment, getenv func(string) string) string {
	switch {
	case env.SwaySocket != "":
		return compositorSway
	case env.HyprlandSignature != "":
		return compositorHyprland
	}

	// XDG_CURRENT_DESKTOP は "GNOME" や "sway" のほか、
	// "ubuntu:GNOME" のようにコロン区切りで複数入ることがある。
	for name := range strings.SplitSeq(getenv("XDG_CURRENT_DESKTOP"), ":") {
		if trimmed := strings.ToLower(strings.TrimSpace(name)); trimmed != "" {
			return trimmed
		}
	}
	if desktop := strings.ToLower(strings.TrimSpace(getenv("DESKTOP_SESSION"))); desktop != "" {
		return desktop
	}
	return compositorUnknown
}

// IsWayland は Wayland のセッションかを返す。
//
// XDG_SESSION_TYPE を先に見る。Wayland でも XWayland のために
// $DISPLAY が入っていることが多く、$DISPLAY だけでは判別できない。
func (e Environment) IsWayland() bool {
	if e.SessionType == "wayland" {
		return true
	}
	return e.SessionType == "" && e.WaylandSocket != ""
}

// IsX11 は X11 のセッションかを返す。
func (e Environment) IsX11() bool {
	return !e.IsWayland() && e.Display != ""
}

// IsUserSession は「人が使っている画面付きのセッション」かを返す。
//
// これが false のときに収集を始めてはいけない。何も観測できていないのに
// collector_start と collector_stop だけが並び、人が使っていない時間に
// 「端末利用」の TimeIs が生えてしまう。収集できないことより、
// 事実でない記録が作られることのほうが害が大きい。
func (e Environment) IsUserSession() bool {
	if e.Display != "" || e.WaylandSocket != "" {
		return true
	}
	// 画面が無くても、logind のセッションが対話的なら人が使っている。
	switch e.SessionType {
	case "x11", "wayland", "mir", "tty":
		return e.LogindSession != ""
	}
	return false
}
