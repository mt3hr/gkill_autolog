package linuxapi

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ssidFromBytes は SSID のバイト列を文字列にする。
//
// NetworkManager も wpa_supplicant も SSID を生のバイト列で返す。
// 規格上は任意のバイト列なので、文字として表せないものが来る。
//
// 空、あるいは文字として表せないものは ErrSSIDUnknown を返す。
// 化けた文字列をそのまま記録すると、同じアクセスポイントが
// 実行のたびに違う名前で TimeIs になりうるため。
// 「未接続」として返さないこと。つながったままなのに切断イベントが出る。
func ssidFromBytes(b []byte) (string, error) {
	// 末尾の NUL 埋めを落とす。長さ固定で渡してくる実装がある。
	trimmed := strings.TrimSpace(string(bytes.TrimRight(b, "\x00")))
	if trimmed == "" {
		return "", ErrSSIDUnknown
	}
	if !utf8.ValidString(trimmed) {
		return "", fmt.Errorf("SSIDを文字として読めない: %w", ErrSSIDUnknown)
	}
	return trimmed, nil
}

// parseSSIDCommandOutput は SSID を出すコマンドの出力から SSID を取り出す。
//
// 最初の空でない行をそのまま SSID として扱う（iwgetid -r と同じ形）。
// 2つ目の戻り値が false のときは、つながっていない。
//
// 出力の解釈をここで凝らないのは、コマンドを決め打ちしないため。
// 複数の項目を出すコマンド (nmcli など) を使いたい場合は、
// SSID だけを1行で出すラッパを書いて設定で指すこと。
func parseSSIDCommandOutput(out []byte) (string, bool) {
	for line := range strings.Lines(string(out)) {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed, true
		}
	}
	return "", false
}
