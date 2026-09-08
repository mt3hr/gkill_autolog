//go:build linux && !android

package linuxapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// windowCmdWait は前面ウィンドウを出すコマンドの待ち時間。
//
// 1秒ごとに呼ぶので短くする。返らないコマンドで収集を止めない。
const windowCmdWait = 2 * time.Second

// commandWindowSource は設定されたコマンドから前面ウィンドウを読む。
//
// GNOME・KDE の Wayland にはウィンドウを取る標準の手段が無い。
// 拡張やスクリプトを書いた利用者がその出力を使えるようにするための逃げ道。
type commandWindowSource struct {
	argv  []string
	names *desktopNameCache
}

// commandWindowOutput はコマンドに期待する出力。
//
// 1行の JSON。取れなかった項目は空のままでよい。
// window_title は生ログにだけ残る（要件 §6.4）。
type commandWindowOutput struct {
	AppName        string `json:"app_name"`
	AppDisplayName string `json:"app_display_name"`
	WindowTitle    string `json:"window_title"`
	ProcessPath    string `json:"process_path"`
	PID            uint32 `json:"pid"`
}

func newCommandWindowSource(command string, names *desktopNameCache) (*commandWindowSource, error) {
	argv := strings.Fields(command)
	if len(argv) == 0 {
		return nil, fmt.Errorf("前面ウィンドウを出すコマンドが設定されていない: %w", ErrUnavailable)
	}
	return &commandWindowSource{argv: argv, names: names}, nil
}

func (s *commandWindowSource) Backend() string { return "command:" + s.argv[0] }

func (s *commandWindowSource) Close() {}

// Foreground はコマンドを実行して前面ウィンドウを読む。
func (s *commandWindowSource) Foreground() (ForegroundWindow, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), windowCmdWait)
	defer cancel()

	out, err := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...).Output()
	if err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("前面ウィンドウのコマンドが失敗した: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		// 前面のウィンドウが無い。ロック中もここに来る。
		return ForegroundWindow{}, false, nil
	}

	var parsed commandWindowOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("前面ウィンドウのコマンドの出力を読めない: %w", err)
	}
	if parsed.AppName == "" && parsed.AppDisplayName == "" && parsed.WindowTitle == "" {
		return ForegroundWindow{}, false, nil
	}

	info := ForegroundWindow{
		AppName:        parsed.AppName,
		AppDisplayName: parsed.AppDisplayName,
		WindowTitle:    parsed.WindowTitle,
		ProcessPath:    parsed.ProcessPath,
		PID:            parsed.PID,
	}
	if info.AppDisplayName == "" {
		info.AppDisplayName = s.names.lookup(desktopKeys{
			AppID:    parsed.AppName,
			ExecName: parsed.AppName,
		})
	}
	return info, true, nil
}
