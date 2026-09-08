//go:build linux && !android

package linuxapi

import (
	"fmt"
)

// windowBackend は前面ウィンドウを読む手段。
type windowBackend interface {
	Backend() string
	Foreground() (ForegroundWindow, bool, error)
	Close()
}

// WindowSource は前面ウィンドウを読む。
//
// 手段は起動時に一度だけ選ぶ。1秒ごとに選び直すと、一時的に落ちている
// だけの手段を捨ててしまう。
type WindowSource struct {
	backend windowBackend
}

// NewWindowSource は前面ウィンドウを読む手段を選ぶ。
//
// 順に:
//  1. 設定されたコマンド（標準の手段が無い環境の逃げ道）
//  2. sway / i3 の IPC
//  3. Hyprland の IPC
//  4. X11
//
// **Wayland では X11 の経路を使わない。** XWayland のために $DISPLAY が
// 入っていることが多いが、ネイティブの Wayland クライアントが前面のとき
// _NET_ACTIVE_WINDOW は古い値を返し続ける。そのまま記録すると
// 「ずっと同じアプリを使っていた」という偽の記録になる。
func NewWindowSource(command string) (*WindowSource, error) {
	env := DetectEnvironment()
	names := newDesktopNameCache()

	if command != "" {
		backend, err := newCommandWindowSource(command, names)
		if err != nil {
			return nil, err
		}
		return &WindowSource{backend: backend}, nil
	}

	if env.SwaySocket != "" {
		if backend, err := newSwayWindowSource(env.SwaySocket, names); err == nil {
			return &WindowSource{backend: backend}, nil
		}
	}
	if env.HyprlandSignature != "" {
		if backend, err := newHyprlandWindowSource(env.HyprlandSignature, names); err == nil {
			return &WindowSource{backend: backend}, nil
		}
	}

	if env.IsWayland() {
		return nil, fmt.Errorf(
			"この Wayland 環境には前面ウィンドウを取る手段が無い (compositor=%s)。"+
				"%s に前面ウィンドウを1行の JSON で出すコマンドを設定すること: %w",
			env.Compositor, "AUTOLOG_LINUX_WINDOW_COMMAND", ErrUnavailable)
	}

	if env.Display == "" {
		return nil, x11DisplayRequired()
	}
	backend, err := newX11WindowSource(env.Display, names)
	if err != nil {
		return nil, err
	}
	return &WindowSource{backend: backend}, nil
}

// Backend は選ばれた手段の名前を返す。起動時のログに載せる。
func (s *WindowSource) Backend() string { return s.backend.Backend() }

// Foreground は前面ウィンドウを返す。
// 2つ目の戻り値が false のときは前面ウィンドウが無い（ロック中など）。
func (s *WindowSource) Foreground() (ForegroundWindow, bool, error) {
	return s.backend.Foreground()
}

// Close は掴んだ接続を離す。
func (s *WindowSource) Close() { s.backend.Close() }
