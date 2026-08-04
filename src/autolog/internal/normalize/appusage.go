package normalize

import (
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// appUsages は Android の app_usage イベントを TimeIs へ変換する。
//
// 同じアプリの利用が WindowMergeWindow 以内に再開したら1本にまとめる。
// UsageStatsManager は画面遷移のたびに区間を切るので、まとめないと
// 同じアプリを使い続けただけで数十秒の TimeIs が延々と並ぶ。
// PC 側の windowSessions が割り込みを結合しているのと同じ考え方。
//
// MinAppUsage 以上使われたものだけを記録する。長さの判定は結合を済ませたあとに行う。
// 短い断片どうしが結合して下限を超えることがあるため。
//
// gkill へは表示上のアプリ名だけを出し、パッケージ名は出さない（要件 §11.2）。
//
// 2つ目の戻り値は次回へ持ち越す末尾の区間。
func appUsages(device rawlog.Device, events []*rawlog.Event, opts Options, carried []rawlog.OpenStateInterval) ([]Proposal, []rawlog.OpenStateInterval, error) {
	// 同じアプリの同じ区間が複数回届くことがある。
	// UsageStatsManager は前面のままのアプリがあると読み出し位置が戻るため、
	// 収集側が同じ区間を再送しうる。収集側でも決定的な event_id で防いでいるが、
	// 過去に集めた分にも効くようここでも落とす。
	type usageKey struct {
		label string
		start int64
		end   int64
	}
	seen := map[usageKey]struct{}{}

	var intervals []timedInterval
	for _, event := range filterType(events, rawlog.EventAppUsage) {
		payload, err := rawlog.DecodePayload[rawlog.AppUsagePayload](event)
		if err != nil {
			return nil, nil, err
		}

		if event.EndTime == nil || payload.AppLabel == "" {
			continue
		}

		key := usageKey{
			label: payload.AppLabel,
			start: event.StartTime.Unix(),
			end:   event.EndTime.Unix(),
		}
		if _, duplicated := seen[key]; duplicated {
			continue
		}
		seen[key] = struct{}{}

		intervals = append(intervals, timedInterval{
			key:      payload.AppLabel,
			title:    payload.AppLabel,
			start:    event.StartTime,
			end:      *event.EndTime,
			eventIDs: []string{event.EventID},
		})
	}

	merged, pending := mergeIntervals(device, SourceWindow, intervals, carried, WindowMergeWindow, opts.Cutoff)

	proposals := make([]Proposal, 0, len(merged))
	for _, interval := range merged {
		if interval.duration() < MinAppUsage {
			continue
		}
		proposals = append(proposals, Proposal{
			ID:             makeID(KindTimeIs, SourceWindow, device, interval.eventIDs),
			Kind:           KindTimeIs,
			Device:         device,
			Source:         SourceWindow,
			SourceEventIDs: interval.eventIDs,
			Title:          interval.title,
			StartTime:      timePtr(interval.start),
			EndTime:        timePtr(interval.end),
		})
	}
	return proposals, pending, nil
}
