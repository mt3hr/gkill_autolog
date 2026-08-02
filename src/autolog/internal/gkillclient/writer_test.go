package gkillclient

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/ledger"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/normalize"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// newTestWriter はテスト用サーバへ書き込む Writer と、呼ばれた API のパス記録を作る。
func newTestWriter(t *testing.T, ledgerDB *ledger.Ledger) (*Writer, *[]string) {
	t.Helper()

	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			writeJSON(t, w, map[string]any{"session_id": "session-1"})
			return
		}
		calls = append(calls, r.URL.Path)
		writeJSON(t, w, map[string]any{})
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	resolve := func(rawlog.Device) (*Client, error) { return client, nil }
	writer := NewWriter(resolve, ledgerDB, slog.New(slog.DiscardHandler), WriteOptions{})
	return writer, &calls
}

func timeIsProposal(id string, eventIDs []string) normalize.Proposal {
	start, end := fixedTime.Add(-time.Hour), fixedTime
	return normalize.Proposal{
		ID:             id,
		Kind:           normalize.KindTimeIs,
		Device:         rawlog.Device("Phone"),
		Source:         normalize.SourceWindow,
		SourceEventIDs: eventIDs,
		Title:          "Chrome",
		StartTime:      &start,
		EndTime:        &end,
	}
}

// カーソルの引き戻しで確定済み区間の断片が別 id で再構成されても、
// 元イベントがすべて書き込み済みなら書き込まないこと。
// 新しい観測が混ざった提案は通ること。
func TestWriteAllSkipsFragmentsOfWrittenProposals(t *testing.T) {
	ledgerDB, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer ledgerDB.Close()

	writer, calls := newTestWriter(t, ledgerDB)
	ctx := context.Background()

	// 1回目: 全体の区間を書き込む。
	stats, err := writer.WriteAll(ctx, []normalize.Proposal{
		timeIsProposal("p-full", []string{"a1", "a2"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(1回目): %v", err)
	}
	if stats.Written != 1 {
		t.Fatalf("1回目の書き込み = %d, want 1", stats.Written)
	}

	// 2回目: 部分再読による断片(a2 だけ)と、新しい観測を含む提案。
	stats, err = writer.WriteAll(ctx, []normalize.Proposal{
		timeIsProposal("p-fragment", []string{"a2"}),
		timeIsProposal("p-extended", []string{"a2", "a3"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(2回目): %v", err)
	}
	if stats.Skipped != 1 {
		t.Errorf("台帳で除外 = %d, want 1 (断片は書かない)", stats.Skipped)
	}
	if stats.Written != 1 {
		t.Errorf("書き込み = %d, want 1 (新しい観測を含む提案は書く)", stats.Written)
	}

	// API 呼び出しは 2 回 (1回目の p-full と 2回目の p-extended)。タグ付けを除いて数える。
	timeisCalls := 0
	for _, path := range *calls {
		if path == "/api/add_timeis" {
			timeisCalls++
		}
	}
	if timeisCalls != 2 {
		t.Errorf("add_timeis の回数 = %d, want 2", timeisCalls)
	}
}
