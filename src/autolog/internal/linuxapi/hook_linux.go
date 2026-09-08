//go:build linux && !android

package linuxapi

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// inputRescanInterval は入力機器を探し直す間隔。
	//
	// 後から挿したキーボードを取り込むために要る。サンプリングが1秒なので、
	// 挿してから数えられるまで少し遅れても記録の粒度には響かない。
	inputRescanInterval = 10 * time.Second

	// inputReadBufferSize は1回の読み取りで受ける大きさ。
	// 高解像度ホイールは細かい刻みで大量に来るので、多めに取る。
	inputReadBufferSize = 64 * 24
)

// InputHook は evdev から操作の有無を観測する。
//
// X11 と Wayland のどちらでも動く唯一の手段。読むのは
// 「どの種別の入力があったか」だけで、どのキーが押されたかは持たない。
type InputHook struct {
	events  chan InputKind
	devices []string

	mu   sync.Mutex
	open map[string]*os.File
}

// NewInputHook は evdev の観測を用意する。
//
// devices が空なら /proc/bus/input/devices から選ぶ。
// 1台も開けなければエラーを返す。呼び出し側は警告してウィンドウの収集を
// あきらめ、ほかの収集は続ける。
func NewInputHook(devices []string) (*InputHook, error) {
	hook := &InputHook{
		events:  make(chan InputKind, 256),
		devices: devices,
		open:    map[string]*os.File{},
	}

	paths := hook.devicePaths()
	if len(paths) == 0 {
		return nil, fmt.Errorf("入力機器が見つからない (%s): %w", procBusInputDevices, ErrUnavailable)
	}

	// 開けることを先に確かめる。input グループへ入っていないと開けない。
	var opened bool
	for _, path := range paths {
		file, err := openInputDevice(path)
		if err != nil {
			continue
		}
		_ = file.Close()
		opened = true
		break
	}
	if !opened {
		return nil, fmt.Errorf(
			"%s を読めない。利用者を input グループへ入れて入り直すこと (sudo usermod -aG input $USER): %w",
			devInputDir, os.ErrPermission)
	}

	return hook, nil
}

// Backend は選ばれた手段の名前を返す。
func (h *InputHook) Backend() string { return "evdev" }

// Events は観測した入力の種別を流す。Run が終わるときに閉じる。
func (h *InputHook) Events() <-chan InputKind { return h.events }

// Run は入力の観測を回す。ctx が終わるまで返らない。
func (h *InputHook) Run(ctx context.Context) error {
	defer close(h.events)

	var wg sync.WaitGroup
	defer func() {
		h.closeAll()
		wg.Wait()
	}()

	h.scan(&wg)

	ticker := time.NewTicker(inputRescanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			h.scan(&wg)
		}
	}
}

// scan はまだ開いていない機器を開いて読み始める。
func (h *InputHook) scan(wg *sync.WaitGroup) {
	for _, path := range h.devicePaths() {
		h.mu.Lock()
		_, already := h.open[path]
		h.mu.Unlock()
		if already {
			continue
		}

		file, err := openInputDevice(path)
		if err != nil {
			continue
		}

		h.mu.Lock()
		h.open[path] = file
		h.mu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			h.read(file)
			h.forget(path)
		}()
	}
}

// read は1つの機器から読み続ける。閉じられるか外されるまで返らない。
func (h *InputHook) read(file *os.File) {
	recordSize := inputRecordSize()
	buf := make([]byte, inputReadBufferSize)
	var pending []byte

	for {
		n, err := file.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			events, consumed := parseInputEvents(pending, recordSize)
			// 1件に満たない末尾は次の読み取りへ持ち越す。
			pending = append(pending[:0], pending[consumed:]...)
			for _, event := range events {
				if kind, ok := classifyInputEvent(event.Type, event.Code, event.Value); ok {
					h.emit(kind)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// emit は観測した種別を流す。
//
// **詰まっていたら捨てる。** 読み取り自体は止めない。止めるとカーネル側の
// バッファが溢れて SYN_DROPPED になり、以後のイベントの解釈が狂う。
// 捨てるのは観測した側の都合で済ませる（winapi のフックと同じ約束）。
func (h *InputHook) emit(kind InputKind) {
	select {
	case h.events <- kind:
	default:
	}
}

// forget は読み終わった機器を一覧から外す。外した機器は次の探索で開き直せる。
func (h *InputHook) forget(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if file, ok := h.open[path]; ok {
		_ = file.Close()
		delete(h.open, path)
	}
}

// closeAll は開いている機器をすべて閉じる。読み取りは閉じることで止める。
func (h *InputHook) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for path, file := range h.open {
		_ = file.Close()
		delete(h.open, path)
	}
}

// devicePaths は読む機器のファイルを返す。
func (h *InputHook) devicePaths() []string {
	if len(h.devices) > 0 {
		return h.devices
	}

	file, err := os.Open(procBusInputDevices)
	if err != nil {
		return nil
	}
	defer file.Close()

	var paths []string
	for _, device := range parseProcBusInputDevices(file) {
		if device.Interesting() {
			paths = append(paths, device.EventPath)
		}
	}
	return paths
}

// openInputDevice は入力機器を読み取り専用で開く。
//
// O_NONBLOCK を付けて開くと Go が実行時のポーラへ登録するため、
// Close で読み取りを止められる。付けないと読み取りが返らず、
// 収集を止めても機器ごとにゴルーチンが残る。
func openInputDevice(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
