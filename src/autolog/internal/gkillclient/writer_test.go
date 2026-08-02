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
	return sourcedTimeIsProposal(id, normalize.SourceWindow, eventIDs)
}

func sourcedTimeIsProposal(id, source string, eventIDs []string) normalize.Proposal {
	start, end := fixedTime.Add(-time.Hour), fixedTime
	return normalize.Proposal{
		ID:             id,
		Kind:           normalize.KindTimeIs,
		Device:         rawlog.Device("Phone"),
		Source:         source,
		SourceEventIDs: eventIDs,
		Title:          "Chrome",
		StartTime:      &start,
		EndTime:        &end,
	}
}

func kmemoProposal(id string, eventIDs []string) normalize.Proposal {
	related := fixedTime
	return normalize.Proposal{
		ID:             id,
		Kind:           normalize.KindKmemo,
		Device:         rawlog.Device("Phone"),
		Source:         normalize.SourceNotification,
		SourceEventIDs: eventIDs,
		Content:        "アプリ：Gmail\nタイトル：件名",
		RelatedTime:    &related,
	}
}

// カーソルの引き戻しで確定済み区間の断片が別 id で再構成されても、
// 元イベントがすべて書き込み済みなら書き込まないこと。
// 一部だけが書き込み済みの区間 (遅着イベントによる延長) も、
// 区間系の収集元では重複登録になるため書き込まないこと。
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

	// 2回目: 部分再読による断片(a2 だけ)と、遅着イベントで延びた区間(a2+a3)。
	// どちらも書くと p-full と同じ時間帯の TimeIs が重複するため書かない。
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
	if stats.Overlapped != 1 {
		t.Errorf("重なりで除外 = %d, want 1 (延長は書かない)", stats.Overlapped)
	}
	if stats.Written != 0 {
		t.Errorf("書き込み = %d, want 0", stats.Written)
	}

	// まったく別のイベントからの提案は通る。
	stats, err = writer.WriteAll(ctx, []normalize.Proposal{
		timeIsProposal("p-new", []string{"b1", "b2"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(3回目): %v", err)
	}
	if stats.Written != 1 {
		t.Errorf("書き込み = %d, want 1 (新しい観測だけの提案は書く)", stats.Written)
	}

	// API 呼び出しは 2 回 (1回目の p-full と 3回目の p-new)。タグ付けを除いて数える。
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

// 一部重複の扱いは収集元で異なること。
//   - 接続系: 観測の切れ目マーカーを複数区間が共有するので、一部重複でも書く
//   - 通知 (Kmemo): 同じ通知の更新列が正当に重なるので、一部重複でも書く
func TestWriteAllPartialOverlapPolicies(t *testing.T) {
	ledgerDB, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer ledgerDB.Close()

	writer, _ := newTestWriter(t, ledgerDB)
	ctx := context.Background()

	// Wi-Fi A の区間が marker (m1) で閉じて書かれたあと、
	// Wi-Fi B の区間が同じ marker を共有して閉じる。B は書けること。
	stats, err := writer.WriteAll(ctx, []normalize.Proposal{
		sourcedTimeIsProposal("p-wifi-a", normalize.SourceWifi, []string{"c1", "m1"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(Wi-Fi A): %v", err)
	}
	if stats.Written != 1 {
		t.Fatalf("Wi-Fi A の書き込み = %d, want 1", stats.Written)
	}
	stats, err = writer.WriteAll(ctx, []normalize.Proposal{
		sourcedTimeIsProposal("p-wifi-b", normalize.SourceWifi, []string{"c2", "m1"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(Wi-Fi B): %v", err)
	}
	if stats.Written != 1 || stats.Overlapped != 0 {
		t.Errorf("Wi-Fi B: written=%d overlapped=%d, want 1/0 (marker の共有は正常)", stats.Written, stats.Overlapped)
	}

	// 通知: 更新列の前半が書かれたあと、続きの更新を含む提案も書けること。
	stats, err = writer.WriteAll(ctx, []normalize.Proposal{
		kmemoProposal("p-notif-first", []string{"n1", "n2"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(通知1): %v", err)
	}
	if stats.Written != 1 {
		t.Fatalf("通知1の書き込み = %d, want 1", stats.Written)
	}
	stats, err = writer.WriteAll(ctx, []normalize.Proposal{
		kmemoProposal("p-notif-updated", []string{"n1", "n2", "n3"}),
	}, nil)
	if err != nil {
		t.Fatalf("WriteAll(通知2): %v", err)
	}
	if stats.Written != 1 || stats.Overlapped != 0 {
		t.Errorf("通知2: written=%d overlapped=%d, want 1/0 (更新列の続きは書く)", stats.Written, stats.Overlapped)
	}
}
