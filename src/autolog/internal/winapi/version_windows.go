//go:build windows

package winapi

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 実行ファイルごとの説明を覚えておく。
//
// 前面ウィンドウは1秒ごとに見に行くので、そのたびに実行ファイルを読み直すと無駄が大きい。
// 引けなかった結果（空文字）も覚えて、同じファイルを何度も読みに行かないようにする。
var (
	fileDescriptionMu    sync.Mutex
	fileDescriptionCache = map[string]string{}
)

// FileDescription は実行ファイルのバージョン情報から説明を取り出す。
//
// chrome.exe なら「Google Chrome」、Code.exe なら「Visual Studio Code」が返る。
// Android の PackageManager が返す表示名にあたるもので、gkill のタイトルにはこれを使う。
//
// バージョン情報を持たない実行ファイルもあるため、取れないことは珍しくない。
// その場合は空文字を返す。取れなかったことは失敗ではないのでエラーにはしない。
func FileDescription(path string) string {
	if path == "" {
		return ""
	}

	fileDescriptionMu.Lock()
	defer fileDescriptionMu.Unlock()

	if description, cached := fileDescriptionCache[path]; cached {
		return description
	}
	description := readFileDescription(path)
	fileDescriptionCache[path] = description
	return description
}

// readFileDescription は実行ファイルを読んで説明を取り出す。
func readFileDescription(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}

	block := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&block[0])); err != nil {
		return ""
	}

	// 言語とコードページの組は複数入っていることがある。
	// 先頭から順に試し、最初に説明が取れたものを使う。
	for _, t := range translations(block) {
		key := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileDescription`, t.language, t.codePage)
		if description := queryString(block, key); description != "" {
			return description
		}
	}
	return ""
}

// translation は StringFileInfo を引くための言語とコードページの組。
type translation struct {
	language uint16
	codePage uint16
}

// translations はバージョン情報に入っている言語とコードページの組を返す。
func translations(block []byte) []translation {
	var (
		ptr  unsafe.Pointer
		size uint32
	)
	if err := windows.VerQueryValue(unsafe.Pointer(&block[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&ptr), &size); err != nil {
		return nil
	}

	// 1組が uint16 2つ ぶん。
	count := int(size) / 4
	if count == 0 {
		return nil
	}
	raw := unsafe.Slice((*uint16)(ptr), count*2)

	list := make([]translation, 0, count)
	for i := range count {
		list = append(list, translation{language: raw[i*2], codePage: raw[i*2+1]})
	}
	return list
}

// queryString はバージョン情報から文字列値を1つ取り出す。
func queryString(block []byte, subBlock string) string {
	var (
		ptr  unsafe.Pointer
		size uint32
	)
	if err := windows.VerQueryValue(unsafe.Pointer(&block[0]), subBlock, unsafe.Pointer(&ptr), &size); err != nil {
		return ""
	}
	if size == 0 {
		return ""
	}

	// size は終端の NUL を含む文字数。
	return strings.TrimSpace(windows.UTF16ToString(unsafe.Slice((*uint16)(ptr), size)))
}
