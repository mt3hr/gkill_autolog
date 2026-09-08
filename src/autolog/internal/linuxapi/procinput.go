package linuxapi

import (
	"bufio"
	"io"
	"path"
	"strconv"
	"strings"
)

// procBusInputDevices は入力デバイスの一覧が読める場所。
const procBusInputDevices = "/proc/bus/input/devices"

// devInputDir は入力デバイスのファイルが並ぶ場所。
const devInputDir = "/dev/input"

// keyboardKeyThreshold はキーボードとみなす最低のキー数。
//
// 電源ボタン (Power Button) や Video Bus も EV_KEY を持ち、
// KEY_POWER や KEY_SLEEP を数個だけ出す。これを入力デバイスとして開くと、
// ふたを閉じただけ・電源ボタンに触れただけで「操作した」ことになり、
// 使っていない時間にアプリ利用の TimeIs が生える。
// 本物のキーボードは数十個のキーを持つので、数で切り分ける。
const keyboardKeyThreshold = 10

// procInputDevice は /proc/bus/input/devices の1件。
type procInputDevice struct {
	// Name は "AT Translated Set 2 keyboard" のような機器名。
	Name string
	// EventPath は /dev/input/eventN。取れなければ空。
	EventPath string
	// Keyboard はキーボードとして扱えるか。
	Keyboard bool
	// Pointer はマウス・タッチパッドとして扱えるか。
	Pointer bool
}

// Interesting は操作の観測に使う機器かを返す。
func (d procInputDevice) Interesting() bool {
	return d.EventPath != "" && (d.Keyboard || d.Pointer)
}

// parseProcBusInputDevices は入力デバイスの一覧を読む。
//
// ioctl (EVIOCGBIT) ではなくこのファイルを読むのは、_IOC のビット配置が
// アーキテクチャ依存で、linux/arm 向けの配布を実機なしに確かめられないため。
// テキストなのでビルドタグ無しで解析でき、この判定をテストで固定できる。
func parseProcBusInputDevices(r io.Reader) []procInputDevice {
	var (
		devices []procInputDevice
		current procInputDevice
		started bool
	)

	flush := func() {
		if started {
			devices = append(devices, current)
		}
		current = procInputDevice{}
		started = false
	}

	scanner := bufio.NewScanner(r)
	// 機器によっては KEY のビットマスクが長い。既定の 64KB では足りることを
	// 前提にせず、行の上限を広げておく。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if len(line) < 3 || line[1] != ':' {
			continue
		}
		started = true

		key, value := line[0], strings.TrimSpace(line[2:])
		switch key {
		case 'I':
			// 新しい機器の始まり。空行が無い実装に備える。
			if current.Name != "" || current.EventPath != "" {
				flush()
				started = true
			}
		case 'N':
			current.Name = strings.Trim(strings.TrimPrefix(value, "Name="), `"`)
		case 'H':
			current.EventPath = eventPathFromHandlers(strings.TrimPrefix(value, "Handlers="))
		case 'B':
			if codes, ok := strings.CutPrefix(value, "KEY="); ok {
				keyboard, pointer := classifyKeyBitmap(codes)
				current.Keyboard = current.Keyboard || keyboard
				current.Pointer = current.Pointer || pointer
			}
		}
	}
	flush()

	return devices
}

// eventPathFromHandlers は Handlers 行から /dev/input/eventN を取り出す。
func eventPathFromHandlers(handlers string) string {
	for handler := range strings.FieldsSeq(handlers) {
		if strings.HasPrefix(handler, "event") {
			return path.Join(devInputDir, handler)
		}
	}
	return ""
}

// classifyKeyBitmap は KEY のビットマスクからキーボード・ポインタの別を返す。
//
// ビットマスクは long ごとの16進数を空白で区切り、**上位の語から先に**
// 並べたもの。末尾の語がコード 0 から始まる。
//
// 1語のビット数は動いているカーネルの long の幅で決まる。桁数からは
// 求められない（カーネルは %lx で書くので先頭の 0 が落ちる)。
// 32bit のユーザー空間が 64bit のカーネルの上で動くこともあるので、
// 自分のポインタ幅からも決められない。
//
// そこで **64bit と 32bit の両方で読んで、結果を足し合わせる**。
// 取り違えた側の解釈で増えるのは「開く機器」だけで、開いた機器から届く
// イベントは実際のコードで分類し直すため害がない。逆に取りこぼすと、
// マウスのクリックが丸ごと記録されなくなる。
func classifyKeyBitmap(bitmap string) (keyboard, pointer bool) {
	for _, wordBits := range []int{64, 32} {
		k, p := classifyKeyBitmapAs(bitmap, wordBits)
		keyboard = keyboard || k
		pointer = pointer || p
	}
	return keyboard, pointer
}

// classifyKeyBitmapAs は1語のビット数を決め打ってビットマスクを読む。
func classifyKeyBitmapAs(bitmap string, wordBits int) (keyboard, pointer bool) {
	words := strings.Fields(bitmap)
	if len(words) == 0 {
		return false, false
	}

	var keys int
	// 末尾から数えると、その語が受け持つコードの下限が求まる。
	for i := len(words) - 1; i >= 0; i-- {
		word, err := strconv.ParseUint(words[i], 16, 64)
		if err != nil {
			continue
		}
		base := (len(words) - 1 - i) * wordBits
		for bit := range wordBits {
			if word&(1<<uint(bit)) == 0 {
				continue
			}
			code := base + bit
			switch {
			case code < int(btnMisc):
				keys++
			case code >= int(btnMouse) && code <= int(btnTask), code == int(btnTouch):
				pointer = true
			}
		}
	}
	return keys >= keyboardKeyThreshold, pointer
}
