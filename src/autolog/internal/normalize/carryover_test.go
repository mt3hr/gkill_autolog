package normalize

import (
	"testing"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// describeProposals は突き合わせの失敗時に読める形へ直す。
func describeProposals(proposals []Proposal) []string {
	lines := make([]string, 0, len(proposals))
	for _, p := range proposals {
		line := string(p.Kind) + " " + p.Source + " " + p.Title + p.URL + p.Content
		if p.StartTime != nil {
			line += " start=" + p.StartTime.Sub(base()).String()
		}
		if p.EndTime != nil {
			line += " end=" + p.EndTime.Sub(base()).String()
		}
		if p.RelatedTime != nil {
			line += " at=" + p.RelatedTime.Sub(base()).String()
		}
		lines = append(lines, line)
	}
	return lines
}

func TestConnectionRereadDoesNotInvertInterval(t *testing.T) {
	// カーソルの引き戻しで、既に処理した接続・切断を読み直すことがある。
	// 読み直しても終了が開始より前の区間を作らないこと。
	//
	// 持ち越した区間の開始より前にある切断を、その区間を閉じる相手にしてしまうと
	// 「13:35:03 → 13:32:33」のような区間ができる。
	carried := []rawlog.OpenStateInterval{{
		Device:   rawlog.Device("Phone"),
		Source:   SourceBluetooth,
		Key:      "Mouse",
		Title:    "Bluetooth Mouse",
		Start:    base().Add(10 * min),
		EventIDs: []string{"b3"},
	}}

	// 読み直しで、持ち越した開始より前の切断が混ざる。
	events := []*rawlog.Event{
		androidEvent(t, "b2", rawlog.EventBluetooth, 7*min, nil,
			rawlog.BluetoothPayload{DeviceName: "Mouse", Connected: false}),
		androidEvent(t, "b4", rawlog.EventBluetooth, 40*min, nil,
			rawlog.BluetoothPayload{DeviceName: "Mouse", Connected: false}),
	}

	result := runNormalizeWith(t, events, Options{
		Cutoff:     base().Add(60 * min),
		OpenStates: carried,
	})

	states := proposalsBySource(result, SourceBluetooth)
	if len(states) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(states), describeProposals(states))
	}
	if states[0].EndTime.Before(*states[0].StartTime) {
		t.Errorf("終了が開始より前: %v", describeProposals(states))
	}
	if end := states[0].EndTime.Sub(base()); end != 40*min {
		t.Errorf("終了 = %v, want %v", end, 40*min)
	}
}

func TestMakeIDIgnoresDuplicateEventIDs(t *testing.T) {
	// 読み直しで同じイベントIDが2回入っても識別子は変わらない。
	// 変わると、同じ区間が別の提案として二重に書き込まれる。
	once := makeID(KindTimeIs, SourceWindow, []string{"e1", "e2"})
	twice := makeID(KindTimeIs, SourceWindow, []string{"e1", "e2", "e1"})
	if once != twice {
		t.Errorf("識別子が変わった: %s != %s", once, twice)
	}
}
