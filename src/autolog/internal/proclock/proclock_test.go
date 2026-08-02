package proclock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestSecondAcquireIsRejected(t *testing.T) {
	// 多重起動の2個目は待たずに失敗する。
	// LockFileEx / flock はハンドル(open file description)単位のロックなので、
	// 同一プロセス内の2回目の Acquire で多重起動と同じ状況を再現できる。
	path := filepath.Join(t.TempDir(), "test.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire(1回目): %v", err)
	}
	defer first.Release()

	second, err := Acquire(path)
	if err == nil {
		second.Release()
		t.Fatal("2回目の Acquire が成功した。多重起動を防げていない")
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("ErrBusy 以外のエラーが返った: %v", err)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	// 手放したロックは取り直せる。
	path := filepath.Join(t.TempDir(), "test.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire(1回目): %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire(解放後): %v", err)
	}
	defer second.Release()
}

func TestStaleLockFileDoesNotBlock(t *testing.T) {
	// ロックファイルが残っていても、誰も保持していなければ取得できる。
	// (プロセスが死ねば OS がロックを解放する。ファイルは消さない設計)
	path := filepath.Join(t.TempDir(), "test.lock")
	if err := os.WriteFile(path, []byte("12345\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire(残骸あり): %v", err)
	}
	defer lock.Release()
}

func TestReleaseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release(1回目): %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release(2回目): %v", err)
	}
}
