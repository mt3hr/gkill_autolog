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
	wtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procWTSRegisterSessionNotification   = wtsapi32.NewProc("WTSRegisterSessionNotification")
	procWTSUnRegisterSessionNotification = wtsapi32.NewProc("WTSUnRegisterSessionNotification")
)

const (
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmQueryEndSession  = 0x0011
	wmEndSession       = 0x0016
	wmPowerBroadcast   = 0x0218
	wmWTSSessionChange = 0x02B1

	// WTS_SESSION_* — WTSRegisterSessionNotification で受け取る wParam。
	wtsSessionLogon  = 0x5
	wtsSessionLogoff = 0x6
	wtsSessionLock   = 0x7
	wtsSessionUnlock = 0x8

	notifyForThisSession = 0x0

	// PBT_* — WM_POWERBROADCAST の wParam。
	pbtAPMSuspend         = 0x4
	pbtAPMResumeSuspend   = 0x7
	pbtAPMResumeAutomatic = 0x12

	// ENDSESSION_LOGOFF が立っていればログオフ、立っていなければシャットダウン。
	endSessionLogoff = 0x80000000

	wsOverlapped = 0x00000000
)

// SessionEventKind は端末の利用状態の変化。
type SessionEventKind string

const (
	SessionLogon    SessionEventKind = "logon"
	SessionUnlock   SessionEventKind = "unlock"
	SessionLock     SessionEventKind = "lock"
	SessionLogoff   SessionEventKind = "logoff"
	SessionShutdown SessionEventKind = "shutdown"
	SessionSuspend  SessionEventKind = "suspend"
	SessionResume   SessionEventKind = "resume"
)

// SessionWatcher は非表示のトップレベルウィンドウを作り、
// セッション状態と電源状態の変化を受け取る。
//
// メッセージ専用ウィンドウ (HWND_MESSAGE) はブロードキャストを受け取れないため、
// WM_POWERBROADCAST と WM_ENDSESSION を拾えるように通常のトップレベルウィンドウを使う。
// ShowWindow を呼ばないので画面には出ない。
type SessionWatcher struct {
	events chan SessionEventKind
}

// NewSessionWatcher はウォッチャを作る。Run を呼ぶまで何もしない。
func NewSessionWatcher() *SessionWatcher {
	return &SessionWatcher{events: make(chan SessionEventKind, 32)}
}

// Events は検出したセッション変化を流すチャネル。Run の終了時に閉じる。
func (w *SessionWatcher) Events() <-chan SessionEventKind {
	return w.events
}

// Run はウィンドウを作ってメッセージループを回す。ctx が終わるまで返らない。
// ウィンドウはこのスレッドが所有するため、専用のゴルーチンで実行すること。
func (w *SessionWatcher) Run(ctx context.Context) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.events)

	className, err := windows.UTF16PtrFromString("GkillAutologSessionWatcher")
	if err != nil {
		return fmt.Errorf("failed to convert class name: %w", err)
	}

	instance, _, _ := procGetModuleHandleW.Call(0)

	wndProc := windows.NewCallback(func(hwnd uintptr, message uint32, wParam uintptr, lParam uintptr) uintptr {
		switch message {
		case wmWTSSessionChange:
			switch wParam {
			case wtsSessionLogon:
				w.emit(SessionLogon)
			case wtsSessionUnlock:
				w.emit(SessionUnlock)
			case wtsSessionLock:
				w.emit(SessionLock)
			case wtsSessionLogoff:
				w.emit(SessionLogoff)
			}
			return 0

		case wmPowerBroadcast:
			switch wParam {
			case pbtAPMSuspend:
				w.emit(SessionSuspend)
			case pbtAPMResumeSuspend, pbtAPMResumeAutomatic:
				w.emit(SessionResume)
			}
			return 1

		case wmQueryEndSession:
			// 終了を妨げない。
			return 1

		case wmEndSession:
			if wParam != 0 {
				if lParam&endSessionLogoff != 0 {
					w.emit(SessionLogoff)
				} else {
					w.emit(SessionShutdown)
				}
			}
			return 0

		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		}

		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	})

	class := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProc,
		hInstance:     windows.Handle(instance),
		lpszClassName: className,
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		return fmt.Errorf("failed to register window class: %w", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		wsOverlapped,
		0, 0, 0, 0,
		0, 0,
		instance,
		0,
	)
	if hwnd == 0 {
		return fmt.Errorf("failed to create window: %w", err)
	}
	defer procDestroyWindow.Call(hwnd)

	if ok, _, err := procWTSRegisterSessionNotification.Call(hwnd, notifyForThisSession); ok == 0 {
		return fmt.Errorf("failed to register session notification: %w", err)
	}
	defer procWTSUnRegisterSessionNotification.Call(hwnd)

	// ctx が終わったらウィンドウを閉じてメッセージループを抜けさせる。
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			procPostMessageW.Call(hwnd, wmClose, 0, 0)
		case <-stopped:
		}
	}()

	return runMessageLoop()
}

func (w *SessionWatcher) emit(kind SessionEventKind) {
	select {
	case w.events <- kind:
	default:
	}
}

// wndClassExW は WNDCLASSEXW。使わないフィールドもレイアウトを合わせるために残す。
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}
