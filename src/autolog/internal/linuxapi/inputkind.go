package linuxapi

import (
	"encoding/binary"
	"strconv"
)

// evdev のイベント種別。linux/input-event-codes.h より。
const (
	evSyn uint16 = 0x00
	evKey uint16 = 0x01
	evRel uint16 = 0x02
)

// ボタンのコード。これより小さい EV_KEY はキーボードのキー。
const (
	btnMisc  uint16 = 0x100
	btnMouse uint16 = 0x110
	btnTask  uint16 = 0x117
	btnTouch uint16 = 0x14a
)

// 相対移動のコード。ホイールだけを操作として扱う。
const (
	relX           uint16 = 0x00
	relY           uint16 = 0x01
	relHWheel      uint16 = 0x06
	relWheel       uint16 = 0x08
	relWheelHiRes  uint16 = 0x0b
	relHWheelHiRes uint16 = 0x0c
)

// inputEventTailSize は input_event の後半 (type・code・value) の大きさ。
// __u16 + __u16 + __s32 で 8 バイト。
const inputEventTailSize = 8

// rawInputEvent は input_event から必要な3つだけを取り出したもの。
//
// 時刻は使わない。カーネルが積んだ時刻とこちらの時計がずれることがあり、
// 生ログの時刻は観測した側で付けるため。
type rawInputEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

// inputRecordSize は input_event 1件のバイト数を返す。
func inputRecordSize() int {
	return inputRecordSizeFor(strconv.IntSize)
}

// inputRecordSizeFor は long のビット数から input_event の大きさを返す。
//
//	struct input_event { struct timeval time; __u16 type; __u16 code; __s32 value; }
//
// timeval は long 2つ。64bit で 16 + 8 = 24 バイト、
// 32bit (linux/arm・linux/386) で 8 + 8 = 16 バイト。
// ここを取り違えると、読んだバイト列が1件ずつずれて
// でたらめな種別のイベントが延々と出る。
func inputRecordSizeFor(wordBits int) int {
	return (wordBits/8)*2 + inputEventTailSize
}

// parseInputEvents はバイト列から読めるだけのイベントを取り出す。
//
// 2つ目の戻り値は消費したバイト数。1件に満たない末尾は次の読み取りへ持ち越す。
func parseInputEvents(buf []byte, recordSize int) ([]rawInputEvent, int) {
	if recordSize < inputEventTailSize {
		return nil, len(buf)
	}

	var (
		events   []rawInputEvent
		consumed int
	)
	for len(buf)-consumed >= recordSize {
		record := buf[consumed : consumed+recordSize]
		tail := record[recordSize-inputEventTailSize:]
		events = append(events, rawInputEvent{
			Type:  binary.NativeEndian.Uint16(tail[0:2]),
			Code:  binary.NativeEndian.Uint16(tail[2:4]),
			Value: int32(binary.NativeEndian.Uint32(tail[4:8])),
		})
		consumed += recordSize
	}
	return events, consumed
}

// classifyInputEvent はイベントを操作の種別へ振り分ける。
//
// 操作として扱うのはクリック・ホイール・キー入力だけで、
// **マウス移動は含めない**（要件 §6.2）。
// どのキーが押されたかは持たない。入力があった事実と種別だけを記録する。
func classifyInputEvent(evType, code uint16, value int32) (InputKind, bool) {
	switch evType {
	case evKey:
		// 押した瞬間だけを数える。0 は離した、2 は押しっぱなしの繰り返し。
		if value != 1 {
			return "", false
		}
		switch {
		case code < btnMisc:
			return InputKey, true
		case code >= btnMouse && code <= btnTask, code == btnTouch:
			return InputClick, true
		default:
			// ゲームパッドやスタイラスのボタン。操作としては数えない。
			return "", false
		}

	case evRel:
		switch code {
		case relWheel, relHWheel, relWheelHiRes, relHWheelHiRes:
			// 高解像度ホイールは細かい刻みで大量に来るが、
			// 種別を決めるだけなので数は問題にならない。
			return InputWheel, true
		case relX, relY:
			// マウス移動。操作扱いにしない。
			return "", false
		}
		return "", false
	}

	// EV_SYN・EV_ABS・EV_MSC・EV_SW など。触っていない。
	return "", false
}
