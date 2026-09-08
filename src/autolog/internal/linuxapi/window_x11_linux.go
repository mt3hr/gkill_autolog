//go:build linux && !android

package linuxapi

import (
	"fmt"
	"strings"

	"github.com/jezek/xgb/xproto"
)

// x11WindowSource は X11 の前面ウィンドウを読む。
type x11WindowSource struct {
	conn    *x11Conn
	display string
	names   *desktopNameCache
}

// newX11WindowSource は X11 から前面ウィンドウを読む手段を用意する。
func newX11WindowSource(display string, names *desktopNameCache) (*x11WindowSource, error) {
	conn, err := newX11Conn(display)
	if err != nil {
		return nil, err
	}
	return &x11WindowSource{conn: conn, display: display, names: names}, nil
}

func (s *x11WindowSource) Backend() string { return "x11" }

func (s *x11WindowSource) Close() {
	s.conn.Close()
}

// Foreground は前面ウィンドウを返す。
//
// 画面がロックされているときは false を返す。X11 ではロック画面
// (i3lock など) が普通のウィンドウとして前面に来るため、そのまま記録すると
// 「ロッカーを1時間使っていた」という TimeIs ができる。
// Windows の GetForegroundWindow はロック中に 0 を返すので、そこへ揃える。
func (s *x11WindowSource) Foreground() (ForegroundWindow, bool, error) {
	if IsSessionLocked() {
		return ForegroundWindow{}, false, nil
	}

	window, ok := s.conn.propertyWindow(s.conn.root, "_NET_ACTIVE_WINDOW")
	if !ok {
		// ウィンドウマネージャが _NET_ACTIVE_WINDOW を持たないか、
		// 前面のウィンドウが無い。
		return ForegroundWindow{}, false, nil
	}

	instance, class := s.windowClass(window)
	pid, _ := s.conn.propertyCardinal(window, "_NET_WM_PID")

	info := ForegroundWindow{
		WindowTitle: s.windowTitle(window),
		PID:         pid,
		ProcessPath: processPath(pid),
	}

	// アプリ名は実行ファイル名を先に使う。取れなければ WM_CLASS。
	info.AppName = processName(pid)
	if info.AppName == "" {
		info.AppName = firstNonEmptyString(instance, class)
	}

	info.AppDisplayName = s.names.lookup(desktopKeys{
		AppID:    instance,
		Class:    class,
		ExecName: info.AppName,
	})

	if info.AppName == "" && info.AppDisplayName == "" && info.WindowTitle == "" {
		// 何も取れていない。空の記録を積むと normalize が
		// ウィンドウタイトルをタイトルに使う経路へ落ちる（要件 §6.4 違反）。
		return ForegroundWindow{}, false, nil
	}
	return info, true, nil
}

// windowTitle はウィンドウの完全なタイトルを返す。
//
// _NET_WM_NAME (UTF-8) を先に見る。WM_NAME は符号が定まらない。
func (s *x11WindowSource) windowTitle(window xproto.Window) string {
	if title := s.conn.propertyString(window, "_NET_WM_NAME"); title != "" {
		return title
	}
	return s.conn.propertyString(window, "WM_NAME")
}

// windowClass は WM_CLASS の2つの値を返す。
//
// WM_CLASS は NUL 区切りで instance と class が入る。
func (s *x11WindowSource) windowClass(window xproto.Window) (instance, class string) {
	reply := s.conn.property(window, "WM_CLASS", 256)
	if reply == nil {
		return "", ""
	}
	parts := strings.Split(strings.TrimRight(string(reply.Value), "\x00"), "\x00")
	if len(parts) > 0 {
		instance = parts[0]
	}
	if len(parts) > 1 {
		class = parts[1]
	}
	return instance, class
}

// x11DisplayRequired は X11 の手段を使えないときのエラーを作る。
func x11DisplayRequired() error {
	return fmt.Errorf("DISPLAY が設定されていない: %w", ErrUnavailable)
}
