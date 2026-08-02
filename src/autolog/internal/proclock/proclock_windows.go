//go:build windows

package proclock

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile はファイル全体の排他ロックを試みる。取れなければ ErrBusy。
//
// LockFileEx はハンドル単位のロックなので、同じプロセスでも別のハンドルからは
// 取れない。多重起動の検査がプロセス内のテストでもそのまま成立する。
func lockFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err == nil {
		return nil
	}
	if err == windows.ERROR_LOCK_VIOLATION {
		return ErrBusy
	}
	return err
}

func unlockFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}
