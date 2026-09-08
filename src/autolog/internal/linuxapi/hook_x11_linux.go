//go:build linux && !android

package linuxapi

import (
	"context"
	"time"

	"github.com/jezek/xgb/xproto"
)

// x11PollInterval は入力の状態を見に行く間隔。
//
// キーを押している時間は普通 80ms 以上あるので、この間隔なら取りこぼしにくい。
const x11PollInterval = 50 * time.Millisecond

// X11InputHook は X11 の入力の状態を短い間隔で見て、押された瞬間を数える。
//
// evdev を読めないとき (input グループに入っていないとき) の退避手段。
// 追加の権限が要らない代わりに、次の弱点がある。
//
//   - ホイールの回転は押されている時間が非常に短く、多くを取りこぼす。
//     スクロールだけでページを読み続けた時間は「操作が無かった」ことになる。
//   - 間隔より短い打鍵は数えられない。
//
// **マウス移動は数えない**（要件 §6.2）。ポインタの座標は見ず、
// ボタンの状態とキーの状態だけを見る。
type X11InputHook struct {
	events chan InputKind
	conn   *x11Conn
}

// NewX11InputHook は X11 の入力の観測を用意する。
func NewX11InputHook(display string) (*X11InputHook, error) {
	if display == "" {
		return nil, x11DisplayRequired()
	}
	conn, err := newX11Conn(display)
	if err != nil {
		return nil, err
	}
	return &X11InputHook{events: make(chan InputKind, 256), conn: conn}, nil
}

// Backend は選ばれた手段の名前を返す。
func (h *X11InputHook) Backend() string { return "x11-poll" }

// Events は観測した入力の種別を流す。Run が終わるときに閉じる。
func (h *X11InputHook) Events() <-chan InputKind { return h.events }

// Run は入力の観測を回す。ctx が終わるまで返らない。
func (h *X11InputHook) Run(ctx context.Context) error {
	defer close(h.events)
	defer h.conn.Close()

	ticker := time.NewTicker(x11PollInterval)
	defer ticker.Stop()

	var (
		lastKeys    [32]byte
		lastButtons uint16
		seeded      bool
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			keys, buttons, ok := h.poll()
			if !ok {
				continue
			}
			if !seeded {
				// 最初の観測は「いま押されているもの」なので、
				// 押した瞬間として数えない。
				lastKeys, lastButtons, seeded = keys, buttons, true
				continue
			}

			for kind := range pressedSince(lastKeys, keys, lastButtons, buttons) {
				h.emit(kind)
			}
			lastKeys, lastButtons = keys, buttons
		}
	}
}

// poll はキーとボタンの状態を読む。
func (h *X11InputHook) poll() ([32]byte, uint16, bool) {
	var keys [32]byte

	keymap, err := xproto.QueryKeymap(h.conn.conn).Reply()
	if err != nil || keymap == nil {
		return keys, 0, false
	}
	copy(keys[:], keymap.Keys)

	pointer, err := xproto.QueryPointer(h.conn.conn, h.conn.root).Reply()
	if err != nil || pointer == nil {
		return keys, 0, false
	}
	return keys, pointer.Mask, true
}

// emit は観測した種別を流す。詰まっていたら捨てる。
func (h *X11InputHook) emit(kind InputKind) {
	select {
	case h.events <- kind:
	default:
	}
}
