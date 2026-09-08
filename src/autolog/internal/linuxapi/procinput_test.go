package linuxapi

import (
	"strings"
	"testing"
)

// procInputFixture は実機の /proc/bus/input/devices を写したもの。
// キーボード・マウス・タッチパッド・電源ボタン・Video Bus を含む。
const procInputFixture = `I: Bus=0019 Vendor=0000 Product=0001 Version=0000
N: Name="Power Button"
P: Phys=PNP0C0C/button/input0
S: Sysfs=/devices/LNXSYSTM:00/LNXSYBUS:00/PNP0C0C:00/input/input0
U: Uniq=
H: Handlers=kbd event0
B: PROP=0
B: EV=3
B: KEY=10000000000000 0

I: Bus=0019 Vendor=0000 Product=0006 Version=0000
N: Name="Video Bus"
P: Phys=LNXVIDEO/video/input0
S: Sysfs=/devices/LNXSYSTM:00/LNXSYBUS:00/PNP0A08:00/LNXVIDEO:00/input/input5
U: Uniq=
H: Handlers=kbd event2
B: PROP=0
B: EV=3
B: KEY=3e000b00000000 0 0 0

I: Bus=0011 Vendor=0001 Product=0001 Version=ab41
N: Name="AT Translated Set 2 keyboard"
P: Phys=isa0060/serio0/input0
S: Sysfs=/devices/platform/i8042/serio0/input/input3
U: Uniq=
H: Handlers=sysrq kbd event3 leds
B: PROP=0
B: EV=120013
B: KEY=402000000 3803078f800d001 feffffdfffefffff fffffffffffffffe
B: MSC=10
B: LED=7

I: Bus=0003 Vendor=046d Product=c52b Version=0111
N: Name="Logitech USB Receiver Mouse"
P: Phys=usb-0000:00:14.0-3/input0
S: Sysfs=/devices/pci0000:00/0000:00:14.0/usb1/1-3/1-3:1.0/0003:046D:C52B.0001/input/input7
U: Uniq=
H: Handlers=mouse0 event7
B: PROP=0
B: EV=17
B: KEY=70000 0 0 0 0
B: REL=903
B: MSC=10

I: Bus=0018 Vendor=06cb Product=7e7e Version=0100
N: Name="SynPS/2 Synaptics TouchPad"
P: Phys=isa0060/serio1/input0
S: Sysfs=/devices/platform/i8042/serio1/input/input9
U: Uniq=
H: Handlers=mouse1 event9
B: PROP=5
B: EV=b
B: KEY=e520 10000 0 0 0 0
B: ABS=660800011000003
`

func devicesByName(devices []procInputDevice) map[string]procInputDevice {
	byName := make(map[string]procInputDevice, len(devices))
	for _, device := range devices {
		byName[device.Name] = device
	}
	return byName
}

func TestParseProcBusInputDevices(t *testing.T) {
	devices := parseProcBusInputDevices(strings.NewReader(procInputFixture))
	if len(devices) != 5 {
		t.Fatalf("読めた機器 = %d 件, want 5", len(devices))
	}

	byName := devicesByName(devices)

	// 電源ボタンと Video Bus も EV_KEY を持つ。これを開くと、ふたを閉じただけ・
	// 電源ボタンに触れただけで「操作した」ことになり、使っていない時間に
	// アプリ利用の TimeIs が生える。
	for _, name := range []string{"Power Button", "Video Bus"} {
		device, ok := byName[name]
		if !ok {
			t.Fatalf("%q が読めていない", name)
		}
		if device.Interesting() {
			t.Errorf("%q を入力機器として扱っている（偽の操作の元になる）", name)
		}
	}

	keyboard, ok := byName["AT Translated Set 2 keyboard"]
	if !ok {
		t.Fatal("キーボードが読めていない")
	}
	if !keyboard.Keyboard {
		t.Error("キーボードをキーボードとして扱えていない")
	}
	if keyboard.EventPath != "/dev/input/event3" {
		t.Errorf("キーボードのイベントファイル = %q, want /dev/input/event3", keyboard.EventPath)
	}

	mouse, ok := byName["Logitech USB Receiver Mouse"]
	if !ok {
		t.Fatal("マウスが読めていない")
	}
	if !mouse.Pointer {
		t.Error("マウスをポインタとして扱えていない（クリックが丸ごと記録されなくなる）")
	}
	if mouse.EventPath != "/dev/input/event7" {
		t.Errorf("マウスのイベントファイル = %q, want /dev/input/event7", mouse.EventPath)
	}

	touchpad, ok := byName["SynPS/2 Synaptics TouchPad"]
	if !ok {
		t.Fatal("タッチパッドが読めていない")
	}
	if !touchpad.Interesting() {
		t.Error("タッチパッドを入力機器として扱えていない")
	}
}

func TestParseProcBusInputDevicesEmpty(t *testing.T) {
	if devices := parseProcBusInputDevices(strings.NewReader("")); len(devices) != 0 {
		t.Errorf("空の入力で %d 件, want 0", len(devices))
	}
}

func TestParseProcBusInputDevicesWithoutEventHandler(t *testing.T) {
	// event ハンドラを持たない機器は開けない。
	const fixture = `I: Bus=0019 Vendor=0000 Product=0001 Version=0000
N: Name="変な機器"
H: Handlers=kbd leds
B: EV=3
B: KEY=402000000 3803078f800d001 feffffdfffefffff fffffffffffffffe
`
	devices := parseProcBusInputDevices(strings.NewReader(fixture))
	if len(devices) != 1 {
		t.Fatalf("読めた機器 = %d 件, want 1", len(devices))
	}
	if devices[0].Interesting() {
		t.Error("開けない機器を入力機器として扱っている")
	}
}

func TestClassifyKeyBitmapReadsBothWordWidths(t *testing.T) {
	// 1語のビット数はカーネルの long の幅で決まり、桁数からは求められない
	// （%lx で書かれるので先頭の 0 が落ちる）。32bit のユーザー空間が
	// 64bit のカーネルの上で動くこともある。両方で読んで足し合わせる。
	if _, pointer := classifyKeyBitmap("70000 0 0 0 0"); !pointer {
		t.Error("64bit カーネルのマウスをポインタとして読めていない")
	}
	if _, pointer := classifyKeyBitmap("70000 0 0 0 0 0 0 0 0"); !pointer {
		t.Error("32bit カーネルのマウスをポインタとして読めていない")
	}
	if keyboard, _ := classifyKeyBitmap("10000000000000 0"); keyboard {
		t.Error("電源ボタンをキーボードとして読んでいる")
	}
}
