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

	if err := l.Record(ctx, "p1", "timeis", "autolog_window", "Laptop", "kyou-1", []string{"e1", "e2"}); err != nil {
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
		if err := l.Record(ctx, "p1", "timeis", "autolog_window", "Laptop", "kyou-1", []string{"e1"}); err != nil {
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
		if err := l.Record(ctx, id, "urlog", "autolog_browser", "Laptop", "kyou-"+id, []string{"ev-" + id}); err != nil {
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

func TestIsCovered(t *testing.T) {
	// カーソルの引き戻しで確定済み区間を途中から読み直すと、
	// イベントの部分集合から別 id の提案が再構成される。
	// 元イベントがすべて記録済みなら断片とみなして弾けること。
	l := openTestLedger(t)
	ctx := context.Background()

	if err := l.Record(ctx, "p1", "timeis", "autolog_window", "Phone", "kyou-1", []string{"a1", "a2"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	tests := []struct {
		name     string
		kind     string
		source   string
		device   string
		eventIDs []string
		want     bool
	}{
		{name: "部分集合は断片", kind: "timeis", source: "autolog_window", device: "Phone",
			eventIDs: []string{"a2"}, want: true},
		{name: "全集合も断片", kind: "timeis", source: "autolog_window", device: "Phone",
			eventIDs: []string{"a1", "a2"}, want: true},
		{name: "重複したIDは1つとして数える", kind: "timeis", source: "autolog_window", device: "Phone",
			eventIDs: []string{"a2", "a2"}, want: true},
		{name: "新しい観測が混ざれば通す", kind: "timeis", source: "autolog_window", device: "Phone",
			eventIDs: []string{"a2", "a3"}, want: false},
		{name: "種類が違えば別物", kind: "urlog", source: "autolog_window", device: "Phone",
			eventIDs: []string{"a1"}, want: false},
		{name: "収集元が違えば別物", kind: "timeis", source: "autolog_media", device: "Phone",
			eventIDs: []string{"a1"}, want: false},
		{name: "端末が違えば別物", kind: "timeis", source: "autolog_window", device: "Tablet",
			eventIDs: []string{"a1"}, want: false},
		{name: "空のイベント集合は断片扱いしない", kind: "timeis", source: "autolog_window", device: "Phone",
			eventIDs: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			covered, err := l.IsCovered(ctx, tt.kind, tt.source, tt.device, tt.eventIDs)
			if err != nil {
				t.Fatalf("IsCovered: %v", err)
			}
			if covered != tt.want {
				t.Errorf("IsCovered = %v, want %v", covered, tt.want)
			}
		})
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
	if err := first.Record(ctx, "p1", "kmemo", "autolog_notification", "Phone", "kyou-1", []string{"n1"}); err != nil {
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
