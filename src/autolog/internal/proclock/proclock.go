// Package proclock はプロセスの多重起動を防ぐファイルロックを提供する。
//
// collect と import はどちらも多重起動すると実害がある。
//   - collect: 2個目が recoverUnfinishedSession で偽の recovered lock を書き、
//     collector_start / collector_stop で稼働中の利用セッションを分断する
//   - import: 台帳の既書き込み判定が起動時のスナップショットなので、
//     並行実行すると同じ提案が両方から gkill へ書かれ Kyou が重複する
//
// OS のファイルロック (Windows: LockFileEx / それ以外: flock) を使う。
// プロセスが死ねば OS がロックを解放するので、消し忘れたロックファイルが
// 残っていても次の起動を妨げない。ロックファイル自体は消さない。
package proclock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// ErrBusy は別のプロセスがロックを保持していることを表す。
var ErrBusy = errors.New("別のプロセスが実行中")

// Handle は取得済みのロック。Release で手放す。
type Handle struct {
	file *os.File
}

// Acquire は path のロックを取得する。
// 別のプロセスが保持していれば ErrBusy を包んだエラーを返し、待たない。
func Acquire(path string) (*Handle, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory for %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %s: %w", path, err)
	}

	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrBusy) {
			return nil, fmt.Errorf("%w (lock: %s)", ErrBusy, path)
		}
		return nil, fmt.Errorf("failed to lock %s: %w", path, err)
	}

	// 誰が持っているかを調べられるように PID を残す。ロックの判定には使わない。
	_ = file.Truncate(0)
	_, _ = file.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)

	return &Handle{file: file}, nil
}

// Release はロックを手放す。
// プロセス終了でも OS が解放するので、呼び忘れても次の起動は妨げない。
func (h *Handle) Release() error {
	if h == nil || h.file == nil {
		return nil
	}
	err := unlockFile(h.file)
	closeErr := h.file.Close()
	h.file = nil
	return errors.Join(err, closeErr)
}
