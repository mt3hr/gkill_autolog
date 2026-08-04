package normalize

import (
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// DefaultUsageTitle は端末利用 TimeIs の既定のタイトル。
//
// 端末ごとに変えたい場合は Options.UsageTitle（設定では AUTOLOG_USAGE_TITLE）で上書きする。
const DefaultUsageTitle = "端末利用"

// opensUsage は端末の利用を開始する側のイベントかを返す。
//
// 画面を点灯しただけ（ロック解除していない）では開始しない。
// collector_start は、収集プログラムが動き始めた＝対話セッションが使われていることを表す。
func opensUsage(action rawlog.SessionAction) bool {
	switch action {
	case rawlog.SessionLogon, rawlog.SessionUnlock, rawlog.SessionResume, rawlog.SessionCollectorStart:
		return true
	default:
		return false
	}
}

// closesUsage は端末の利用を終える側のイベントかを返す。
func closesUsage(action rawlog.SessionAction) bool {
	switch action {
	case rawlog.SessionLock, rawlog.SessionLogoff, rawlog.SessionShutdown,
		rawlog.SessionSuspend, rawlog.SessionScreenOff, rawlog.SessionCollectorStop:
		return true
	default:
		return false
	}
}

// usageSessions は session イベントを端末利用の TimeIs へ変換する。
//
// 開始はログイン・ロック解除・復帰、終了はロック・ログオフ・シャットダウン・スリープ。
// 再起動をまたぐセッションは結合しない（シャットダウンで必ず閉じるため自然にそうなる）。
//
// 返す2つ目の値は継続中セッションの開始時刻。
func usageSessions(device rawlog.Device, events []*rawlog.Event, opts Options) ([]Proposal, time.Time, error) {
	sessions := filterType(events, rawlog.EventSession)
	if len(sessions) == 0 {
		return nil, time.Time{}, nil
	}

	var (
		proposals []Proposal
		openStart time.Time
		openIDs   []string
	)

	for _, event := range sessions {
		payload, err := rawlog.DecodePayload[rawlog.SessionPayload](event)
		if err != nil {
			return nil, time.Time{}, err
		}

		switch {
		case opensUsage(payload.Action):
			if !openStart.IsZero() {
				// 閉じないまま次の開始が来た場合は、後から来た方を採用する。
				// 直前の開始は観測漏れなので TimeIs にしない。
				openIDs = nil
			}
			openStart = event.StartTime
			openIDs = append(openIDs, event.EventID)

		case closesUsage(payload.Action):
			if openStart.IsZero() {
				// 対応する開始が無い終了。区間を作れないので捨てる。
				continue
			}
			if event.StartTime.After(openStart) {
				ids := append(openIDs, event.EventID)
				proposals = append(proposals, Proposal{
					ID:             makeID(KindTimeIs, SourceDevice, device, ids),
					Kind:           KindTimeIs,
					Device:         device,
					Source:         SourceDevice,
					SourceEventIDs: ids,
					Title:          opts.usageTitle(),
					StartTime:      timePtr(openStart),
					EndTime:        timePtr(event.StartTime),
				})
			}
			openStart = time.Time{}
			openIDs = nil
		}
	}

	// 閉じていないセッションは継続中。処理せず次回へ持ち越す（要件 §17）。
	return proposals, openStart, nil
}
