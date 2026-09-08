package linuxapi

import (
	"encoding/json"
	"fmt"
)

// hyprlandActiveWindow は hyprctl の activewindow の応答。必要な項目だけ読む。
type hyprlandActiveWindow struct {
	Address string `json:"address"`
	Class   string `json:"class"`
	Title   string `json:"title"`
	PID     uint32 `json:"pid"`
}

// parseHyprlandActiveWindow は前面ウィンドウの応答を読む。
//
// 2つ目の戻り値が false のときは焦点が無い。
// Hyprland は焦点が無いと中身の無いオブジェクトを返す。
func parseHyprlandActiveWindow(data []byte) (ForegroundWindow, bool, error) {
	var window hyprlandActiveWindow
	if err := json.Unmarshal(data, &window); err != nil {
		return ForegroundWindow{}, false, fmt.Errorf("前面ウィンドウの応答を読めない: %w", err)
	}

	if window.Address == "" && window.Class == "" && window.Title == "" {
		return ForegroundWindow{}, false, nil
	}
	if window.Class == "" && window.Title == "" {
		return ForegroundWindow{}, false, nil
	}

	return ForegroundWindow{
		AppName:     window.Class,
		WindowTitle: window.Title,
		PID:         window.PID,
	}, true, nil
}
