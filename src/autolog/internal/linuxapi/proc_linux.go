//go:build linux && !android

package linuxapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// processPath は実行ファイルの絶対パスを返す。読めなければ空。
//
// 読めなくてもエラーにしない。winapi の FileDescription と同じで、
// 取れないものは空のままにして、取れた事実だけを記録する。
func processPath(pid uint32) string {
	if pid == 0 {
		return ""
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ""
	}
	// 削除済みの実行ファイルは " (deleted)" が付く。更新直後に起きる。
	return strings.TrimSuffix(path, " (deleted)")
}

// processName は実行ファイル名を返す。読めなければ空。
func processName(pid uint32) string {
	if path := processPath(pid); path != "" {
		return filepath.Base(path)
	}

	// exe を読めないことがある（他の利用者のプロセス、権限の制限）。
	// comm は 15 文字で切れるが、無いよりはよい。
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
