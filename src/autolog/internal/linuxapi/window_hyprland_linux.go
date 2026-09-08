//go:build linux && !android

package linuxapi

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// hyprlandIPCWait は IPC の応答を待つ上限。
const hyprlandIPCWait = 2 * time.Second

// hyprlandWindowSource は Hyprland の IPC から前面ウィンドウを読む。
type hyprlandWindowSource struct {
	socket string
	names  *desktopNameCache
}

func newHyprlandWindowSource(signature string, names *desktopNameCache) (*hyprlandWindowSource, error) {
	socket, err := hyprlandSocketPath(signature)
	if err != nil {
		return nil, err
	}

	source := &hyprlandWindowSource{socket: socket, names: names}
	if _, _, err := source.Foreground(); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *hyprlandWindowSource) Backend() string { return "hyprland-ipc" }

func (s *hyprlandWindowSource) Close() {}

// Foreground は前面ウィンドウを読む。
func (s *hyprlandWindowSource) Foreground() (ForegroundWindow, bool, error) {
	conn, err := net.DialTimeout("unix", s.socket, hyprlandIPCWait)
	if err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("Hyprland の IPC へ繋げない: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(hyprlandIPCWait)); err != nil {
		return ForegroundWindow{}, false, err
	}
	// j/ を付けると JSON で返る。
	if _, err := conn.Write([]byte("j/activewindow")); err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("Hyprland の IPC へ送れない: %w", err)
	}

	payload, err := io.ReadAll(conn)
	if err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("Hyprland の IPC の応答を読めない: %w", err)
	}

	info, ok, err := parseHyprlandActiveWindow(payload)
	if err != nil || !ok {
		return ForegroundWindow{}, false, err
	}

	info.ProcessPath = processPath(info.PID)
	class := info.AppName
	if name := processName(info.PID); name != "" {
		info.AppName = name
	}
	info.AppDisplayName = s.names.lookup(desktopKeys{
		AppID:    class,
		Class:    class,
		ExecName: info.AppName,
	})
	return info, true, nil
}

// hyprlandSocketPath は IPC のソケットの場所を返す。
func hyprlandSocketPath(signature string) (string, error) {
	if signature == "" {
		return "", fmt.Errorf("Hyprland の識別子が無い: %w", ErrUnavailable)
	}

	// 新しい版は $XDG_RUNTIME_DIR/hypr の下、古い版は /tmp/hypr の下。
	var candidates []string
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, "hypr", signature, ".socket.sock"))
	}
	candidates = append(candidates, filepath.Join("/tmp", "hypr", signature, ".socket.sock"))

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("Hyprland の IPC のソケットが見つからない: %w", ErrUnavailable)
}
