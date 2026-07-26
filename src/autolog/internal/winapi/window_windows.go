//go:build windows

package winapi

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

// processQueryLimitedInformation は他プロセスの実行ファイル名を取得するのに必要な最小権限。
const processQueryLimitedInformation = 0x1000

// ForegroundWindow は現在の前面ウィンドウの情報。
type ForegroundWindow struct {
	// AppName は実行ファイル名から拡張子を除いたもの（例: chrome, Code）。
	AppName string
	// WindowTitle は前面ウィンドウの完全なタイトル。整形も切り詰めもしない。
	WindowTitle string
	// ProcessPath は実行ファイルの絶対パス。取得できなければ空。
	ProcessPath string
	// PID はプロセスID。
	PID uint32
}

// GetForegroundWindow は前面ウィンドウのアプリ名とタイトルを取得する。
// 前面ウィンドウが無い場合（ロック中など）は ok = false を返す。
func GetForegroundWindow() (ForegroundWindow, bool, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ForegroundWindow{}, false, nil
	}

	title, err := windowText(hwnd)
	if err != nil {
		return ForegroundWindow{}, false, err
	}

	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ForegroundWindow{WindowTitle: title}, true, nil
	}

	info := ForegroundWindow{WindowTitle: title, PID: pid}
	path, err := processImagePath(pid)
	if err != nil {
		// 権限不足や既に終了したプロセスでは取得できない。タイトルだけでも返す。
		return info, true, nil
	}
	info.ProcessPath = path
	info.AppName = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return info, true, nil
}

// windowText はウィンドウタイトルを取得する。長さ制限を設けず完全な値を返す。
func windowText(hwnd uintptr) (string, error) {
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return "", nil
	}
	// 終端の NUL 分を足す。取得中にタイトルが伸びる場合に備えて少し余裕を持たせる。
	buf := make([]uint16, length+2)
	copied, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if copied == 0 {
		return "", nil
	}
	return windows.UTF16ToString(buf[:copied]), nil
}

// processImagePath は PID から実行ファイルの絶対パスを取得する。
func processImagePath(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return "", fmt.Errorf("failed to open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("failed to query image name of process %d: %w", pid, err)
	}
	return windows.UTF16ToString(buf[:size]), nil
}
