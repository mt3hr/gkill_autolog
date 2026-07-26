//go:build windows

package winapi

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")

	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
)

// フック種別。
const (
	whKeyboardLL = 13
	whMouseLL    = 14
)

// ウィンドウメッセージ。
const (
	wmQuit = 0x0012

	wmKeyDown    = 0x0100
	wmSysKeyDown = 0x0104

	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmRButtonDown = 0x0204
	wmMButtonDown = 0x0207
	wmXButtonDown = 0x020B
	wmMouseWheel  = 0x020A
	wmMouseHWheel = 0x020E
)

// InputKind は操作の種類。マウス移動は含まない。
type InputKind string

const (
	InputClick InputKind = "click"
	InputWheel InputKind = "wheel"
	InputKey   InputKind = "key"
)

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

// InputHook は低レベルフックでクリック・ホイール・キー入力だけを検出する。
// マウス移動は操作として扱わない（要件 §6.2）。
type InputHook struct {
	events chan InputKind
}

// NewInputHook は入力フックを作る。Run を呼ぶまで何もしない。
func NewInputHook() *InputHook {
	// フック内では詰まらせたくないので、取りこぼしても良い程度のバッファを持たせる。
	return &InputHook{events: make(chan InputKind, 256)}
}

// Events は検出した操作を流すチャネル。Run の終了時に閉じる。
func (h *InputHook) Events() <-chan InputKind {
	return h.events
}

// Run はフックを設置してメッセージループを回す。ctx が終わるまで返らない。
//
// 低レベルフックは設置したスレッドがメッセージを処理し続けることを要求するため、
// このゴルーチンを OS スレッドへ固定する。呼び出し側は専用のゴルーチンで実行すること。
func (h *InputHook) Run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(h.events)

	threadID, _, _ := procGetCurrentThreadID.Call()

	mouseCallback := windows.NewCallback(func(code int32, wParam uintptr, lParam uintptr) uintptr {
		if code >= 0 {
			switch wParam {
			case wmLButtonDown, wmRButtonDown, wmMButtonDown, wmXButtonDown:
				h.emit(InputClick)
			case wmMouseWheel, wmMouseHWheel:
				h.emit(InputWheel)
			case wmMouseMove:
				// マウス移動だけでは操作扱いにしない。
			}
		}
		ret, _, _ := procCallNextHookEx.Call(0, uintptr(code), wParam, lParam)
		return ret
	})

	keyboardCallback := windows.NewCallback(func(code int32, wParam uintptr, lParam uintptr) uintptr {
		if code >= 0 && (wParam == wmKeyDown || wParam == wmSysKeyDown) {
			h.emit(InputKey)
		}
		ret, _, _ := procCallNextHookEx.Call(0, uintptr(code), wParam, lParam)
		return ret
	})

	mouseHook, err := setHook(whMouseLL, mouseCallback)
	if err != nil {
		return err
	}
	defer procUnhookWindowsHookEx.Call(mouseHook)

	keyboardHook, err := setHook(whKeyboardLL, keyboardCallback)
	if err != nil {
		return err
	}
	defer procUnhookWindowsHookEx.Call(keyboardHook)

	// ctx が終わったらメッセージループを抜けさせる。
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			procPostThreadMessageW.Call(threadID, wmQuit, 0, 0)
		case <-stopped:
		}
	}()

	return runMessageLoop()
}

// emit は取りこぼしを許容してイベントを流す。
// フックの中で待たされると入力全体が遅くなるため、詰まっていたら捨てる。
func (h *InputHook) emit(kind InputKind) {
	select {
	case h.events <- kind:
	default:
	}
}

func setHook(hookID int, callback uintptr) (uintptr, error) {
	hook, _, err := procSetWindowsHookExW.Call(uintptr(hookID), callback, 0, 0)
	if hook == 0 {
		return 0, fmt.Errorf("failed to set windows hook %d: %w", hookID, err)
	}
	return hook, nil
}

// runMessageLoop は WM_QUIT を受け取るまでメッセージを処理する。
func runMessageLoop() error {
	var m msg
	for {
		ret, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			return fmt.Errorf("failed to get message: %w", err)
		case 0:
			// WM_QUIT。
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
