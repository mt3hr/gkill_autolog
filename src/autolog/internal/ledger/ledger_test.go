package ledger

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestRecordAndIsWritten(t *testing.T) {
	l := openTestLedger(t)
	ctx := context.Background()

	written, err := l.IsWritten(ctx, "p1")
	if err != nil {
		t.Fatalf("IsWritten: %v", err)
	}
	if written {
		t.Error("記録前に書き込み済みと判定された")
	}

	if err := l.Record(ctx, "p1", "timeis", "kyou-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	written, err = l.IsWritten(ctx, "p1")
	if err != nil {
		t.Fatalf("IsWritten: %v", err)
	}
	if !written {
		t.Error("記録後に書き込み済みと判定されない")
	}
}

func TestRecordIsIdempotent(t *testing.T) {
	// バッチが途中で失敗して再実行されても、同じ提案を二重に記録しない。
	l := openTestLedger(t)
	ctx := context.Background()

	for range 3 {
		if err := l.Record(ctx, "p1", "timeis", "kyou-1"); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	count, err := l.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Errorf("件数 = %d, want 1", count)
	}
}

func TestLoadWritten(t *testing.T) {
	l := openTestLedger(t)
	ctx := context.Background()

	for _, id := range []string{"p1", "p2", "p3"} {
		if err := l.Record(ctx, id, "urlog", "kyou-"+id); err != nil {
			t.Fatalf("Record(%s): %v", id, err)
		}
	}

	written, err := l.LoadWritten(ctx)
	if err != nil {
		t.Fatalf("LoadWritten: %v", err)
	}
	if len(written) != 3 {
		t.Fatalf("件数 = %d, want 3", len(written))
	}
	for _, id := range []string{"p1", "p2", "p3"} {
		if _, ok := written[id]; !ok {
			t.Errorf("%s が含まれていない", id)
		}
	}
	if _, ok := written["p4"]; ok {
		t.Error("記録していない p4 が含まれている")
	}
}

func TestLedgerPersistsAcrossReopen(t *testing.T) {
	// 台帳はバッチをまたいで効かなければ意味がない。
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	ctx := context.Background()

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.Record(ctx, "p1", "kmemo", "kyou-1"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open(2回目): %v", err)
	}
	defer second.Close()

	written, err := second.IsWritten(ctx, "p1")
	if err != nil {
		t.Fatalf("IsWritten: %v", err)
	}
	if !written {
		t.Error("開き直したら記録が消えている")
	}
}
