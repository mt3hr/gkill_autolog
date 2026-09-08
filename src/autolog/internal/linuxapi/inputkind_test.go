package linuxapi

import (
	"encoding/binary"
	"testing"
)

func TestClassifyInputEvent(t *testing.T) {
	const (
		keyA       uint16 = 30
		keyEsc     uint16 = 1
		btnLeft    uint16 = 0x110
		btnRight   uint16 = 0x111
		btnExtra   uint16 = 0x114
		btnGamepad uint16 = 0x130
		evAbs      uint16 = 0x03
		evMsc      uint16 = 0x04
	)

	tests := []struct {
		name   string
		evType uint16
		code   uint16
		value  int32
		want   InputKind
		ok     bool
	}{
		{name: "キーを押した", evType: evKey, code: keyA, value: 1, want: InputKey, ok: true},
		{name: "Esc も同じ", evType: evKey, code: keyEsc, value: 1, want: InputKey, ok: true},
		{name: "キーを離しただけでは数えない", evType: evKey, code: keyA, value: 0, ok: false},
		{name: "押しっぱなしの繰り返しは数えない", evType: evKey, code: keyA, value: 2, ok: false},
		{name: "左クリック", evType: evKey, code: btnLeft, value: 1, want: InputClick, ok: true},
		{name: "右クリック", evType: evKey, code: btnRight, value: 1, want: InputClick, ok: true},
		{name: "サイドボタン", evType: evKey, code: btnExtra, value: 1, want: InputClick, ok: true},
		{name: "タッチ", evType: evKey, code: btnTouch, value: 1, want: InputClick, ok: true},
		{name: "ゲームパッドは操作として数えない", evType: evKey, code: btnGamepad, value: 1, ok: false},
		{name: "縦ホイール", evType: evRel, code: relWheel, value: 1, want: InputWheel, ok: true},
		{name: "横ホイール", evType: evRel, code: relHWheel, value: -1, want: InputWheel, ok: true},
		{name: "高解像度ホイール", evType: evRel, code: relWheelHiRes, value: 15, want: InputWheel, ok: true},
		// 要件 §6.2: マウス移動だけでは操作扱いにしない。
		{name: "マウスの横移動は操作ではない", evType: evRel, code: relX, value: 5, ok: false},
		{name: "マウスの縦移動は操作ではない", evType: evRel, code: relY, value: -3, ok: false},
		{name: "タッチパッドの絶対座標は操作ではない", evType: evAbs, code: 0, value: 100, ok: false},
		{name: "EV_SYN は操作ではない", evType: evSyn, code: 0, value: 0, ok: false},
		{name: "EV_MSC は操作ではない", evType: evMsc, code: 4, value: 458792, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := classifyInputEvent(tt.evType, tt.code, tt.value)
			if ok != tt.ok {
				t.Fatalf("操作として数えるか = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("種別 = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInputRecordSize(t *testing.T) {
	// struct input_event の大きさは long の幅で変わる。
	// 取り違えると読んだバイト列が1件ずつずれ、でたらめな種別が延々と出る。
	if got := inputRecordSizeFor(64); got != 24 {
		t.Errorf("64bit の input_event = %d バイト, want 24", got)
	}
	if got := inputRecordSizeFor(32); got != 16 {
		t.Errorf("32bit の input_event = %d バイト, want 16（linux/arm 向けの配布がある）", got)
	}
	if got := inputRecordSize(); got != 24 && got != 16 {
		t.Errorf("この環境の input_event = %d バイト, want 24 か 16", got)
	}
}

// appendInputEvent は input_event 1件をバイト列へ組み立てる。
func appendInputEvent(buf []byte, recordSize int, evType, code uint16, value int32) []byte {
	record := make([]byte, recordSize)
	tail := record[recordSize-inputEventTailSize:]
	binary.NativeEndian.PutUint16(tail[0:2], evType)
	binary.NativeEndian.PutUint16(tail[2:4], code)
	binary.NativeEndian.PutUint32(tail[4:8], uint32(value))
	return append(buf, record...)
}

func TestParseInputEvents(t *testing.T) {
	for _, recordSize := range []int{24, 16} {
		t.Run(map[int]string{24: "64bit", 16: "32bit"}[recordSize], func(t *testing.T) {
			var buf []byte
			buf = appendInputEvent(buf, recordSize, evKey, 30, 1)
			buf = appendInputEvent(buf, recordSize, evRel, relWheel, -1)

			events, consumed := parseInputEvents(buf, recordSize)
			if consumed != len(buf) {
				t.Errorf("消費したバイト数 = %d, want %d", consumed, len(buf))
			}
			if len(events) != 2 {
				t.Fatalf("読めたイベント = %d 件, want 2", len(events))
			}
			if events[0].Type != evKey || events[0].Code != 30 || events[0].Value != 1 {
				t.Errorf("1件目 = %+v", events[0])
			}
			if events[1].Type != evRel || events[1].Code != relWheel || events[1].Value != -1 {
				t.Errorf("2件目 = %+v（負の値を符号付きで読めていない可能性）", events[1])
			}
		})
	}
}

func TestParseInputEventsKeepsPartialTail(t *testing.T) {
	// 1件に満たない末尾は消費せず、次の読み取りへ持ち越す。
	// ここで捨てると、境界にかかったイベントが毎回1件ずつ失われる。
	recordSize := 24
	buf := appendInputEvent(nil, recordSize, evKey, 30, 1)
	buf = append(buf, 1, 2, 3)

	events, consumed := parseInputEvents(buf, recordSize)
	if len(events) != 1 {
		t.Fatalf("読めたイベント = %d 件, want 1", len(events))
	}
	if consumed != recordSize {
		t.Errorf("消費したバイト数 = %d, want %d（半端な末尾は残す）", consumed, recordSize)
	}
}

func TestParseInputEventsEmpty(t *testing.T) {
	events, consumed := parseInputEvents(nil, 24)
	if len(events) != 0 || consumed != 0 {
		t.Errorf("空の入力で %d 件 / %d バイト, want 0 / 0", len(events), consumed)
	}
}
