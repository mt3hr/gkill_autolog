//go:build linux && !android

package linuxapi

import (
	"fmt"
	"net"
	"time"
)

// swayIPCWait は IPC の応答を待つ上限。
// 前面ウィンドウの取得は1秒ごとに走るので、詰まったら諦める。
const swayIPCWait = 2 * time.Second

// swayWindowSource は sway / i3 の IPC から前面ウィンドウを読む。
//
// プロトコルが単純なので外部コマンド (swaymsg) を呼ばずに直接話す。
type swayWindowSource struct {
	socket string
	names  *desktopNameCache
}

func newSwayWindowSource(socket string, names *desktopNameCache) (*swayWindowSource, error) {
	source := &swayWindowSource{socket: socket, names: names}
	// 繋がることを起動時に確かめる。
	if _, _, err := source.Foreground(); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *swayWindowSource) Backend() string { return "sway-ipc" }

func (s *swayWindowSource) Close() {}

// Foreground はウィンドウの木を取り、焦点のあるものを返す。
func (s *swayWindowSource) Foreground() (ForegroundWindow, bool, error) {
	payload, err := s.request(i3MsgGetTree)
	if err != nil {
		return ForegroundWindow{}, false, err
	}

	info, ok, err := parseSwayTree(payload)
	if err != nil || !ok {
		return ForegroundWindow{}, false, err
	}

	if info.ProcessPath == "" {
		info.ProcessPath = processPath(info.PID)
	}
	if name := processName(info.PID); name != "" {
		// アプリ名は実行ファイル名を先に使う。取れなければ app_id / WM_CLASS。
		keys := swayNodeKeys(info)
		info.AppDisplayName = s.names.lookup(desktopKeys{
			AppID:    keys.AppID,
			Class:    keys.Class,
			ExecName: name,
		})
		info.AppName = name
	} else {
		info.AppDisplayName = s.names.lookup(swayNodeKeys(info))
	}
	return info, true, nil
}

// request は IPC へ1件の要求を出して応答を読む。
//
// 接続は毎回張り直す。sway の IPC は要求ごとに応答を返す作りで、
// 常時つないでおくと購読との区別が要るため。
func (s *swayWindowSource) request(msgType uint32) ([]byte, error) {
	conn, err := net.DialTimeout("unix", s.socket, swayIPCWait)
	if err != nil {
		return nil, fmt.Errorf("sway の IPC へ繋げない: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(swayIPCWait)); err != nil {
		return nil, err
	}
	if _, err := conn.Write(encodeI3Message(msgType, nil)); err != nil {
		return nil, fmt.Errorf("sway の IPC へ送れない: %w", err)
	}

	replyType, payload, err := decodeI3Message(conn)
	if err != nil {
		return nil, fmt.Errorf("sway の IPC の応答を読めない: %w", err)
	}
	if replyType != msgType {
		return nil, fmt.Errorf("sway の IPC が別の種別を返した: %d", replyType)
	}
	return payload, nil
}
