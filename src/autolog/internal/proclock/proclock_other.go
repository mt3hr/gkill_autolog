//go:build !windows

package proclock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile はファイル全体の排他ロックを試みる。取れなければ ErrBusy。
//
// flock は open file description 単位のロックなので、同じプロセスでも別に
// 開いたファイルからは取れない。多重起動の検査がプロセス内のテストでもそのまま成立する。
func lockFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrBusy
	}
	return err
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
