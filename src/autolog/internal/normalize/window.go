package normalize

import (
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// windowSegment は同じウィンドウを操作し続けた1区間。
type windowSegment struct {
	appName     string
	windowTitle string
	start       time.Time
	end         time.Time
	eventIDs    []string
}

// duration は区間の長さ。
func (s windowSegment) duration() time.Duration {
	return s.end.Sub(s.start)
}

// sameWindow は同じウィンドウかを返す。
// アプリ名とウィンドウタイトルの両方が一致したときだけ同一とみなす。
func (s windowSegment) sameWindow(other windowSegment) bool {
	return s.appName == other.appName && s.windowTitle == other.windowTitle
}

// title は TimeIs のタイトルを返す。
//
// アプリ名とウィンドウタイトルを生の値に近い状態で使う。
// 要約や整形はしない（要件 §6.4）。
func (s windowSegment) title() string {
	switch {
	case s.windowTitle == "":
		return s.appName
	case s.appName == "":
		return s.windowTitle
	default:
		return s.windowTitle
	}
}

// windowSessions は input イベントをウィンドウごとの TimeIs セッションへ変換する。
//
// 返す2つ目の値は継続中セッションの開始時刻。
// 継続中のものがなければゼロ値を返す。
func windowSessions(device rawlog.Device, events []*rawlog.Event, opts Options) ([]Proposal, time.Time, error) {
	inputs := filterType(events, rawlog.EventInput)
	if len(inputs) == 0 {
		return nil, time.Time{}, nil
	}

	segments, err := buildWindowSegments(inputs)
	if err != nil {
		return nil, time.Time{}, err
	}
	segments = mergeShortInterruptions(segments)

	// 末尾のセッションがまだ続いているかを判定する。
	// 最後の入力から IdleTimeout 経っていなければ、Cutoff 後も続いている可能性がある。
	// 継続中のものは処理せず、終了後の次回バッチに回す（要件 §6.3）。
	var pendingStart time.Time
	if last := segments[len(segments)-1]; opts.Cutoff.Sub(last.end) < IdleTimeout {
		pendingStart = last.start
		segments = segments[:len(segments)-1]
	}

	proposals := make([]Proposal, 0, len(segments))
	for _, segment := range segments {
		// 一瞬触っただけのウィンドウは記録しない。
		// 結合を済ませたあとの長さで判定する。
		if segment.duration() < MinWindowDuration {
			continue
		}
		proposals = append(proposals, Proposal{
			ID:             makeID(KindTimeIs, SourceWindow, segment.eventIDs),
			Kind:           KindTimeIs,
			Device:         device,
			Source:         SourceWindow,
			SourceEventIDs: segment.eventIDs,
			Title:          segment.title(),
			StartTime:      timePtr(segment.start),
			EndTime:        timePtr(segment.end),
		})
	}
	return proposals, pendingStart, nil
}

// buildWindowSegments は入力イベントを連続した区間へまとめる。
//
// 区間が切れるのは次の2つ。
//   - ウィンドウが変わったとき
//   - 入力が IdleTimeout 以上途切れたとき（終了時刻はアイドル判定時刻ではなく最後の入力時刻）
//
// 日付をまたいでも入力が続いていれば1つの区間として扱う（要件 §6.3）。
func buildWindowSegments(inputs []*rawlog.Event) ([]windowSegment, error) {
	var segments []windowSegment
	var current *windowSegment

	for _, event := range inputs {
		payload, err := rawlog.DecodePayload[rawlog.InputPayload](event)
		if err != nil {
			return nil, err
		}

		candidate := windowSegment{
			appName:     payload.AppName,
			windowTitle: payload.WindowTitle,
			start:       event.StartTime,
			end:         event.StartTime,
			eventIDs:    []string{event.EventID},
		}

		if current != nil &&
			current.sameWindow(candidate) &&
			event.StartTime.Sub(current.end) < IdleTimeout {
			current.end = event.StartTime
			current.eventIDs = append(current.eventIDs, event.EventID)
			continue
		}

		if current != nil {
			segments = append(segments, *current)
		}
		current = &candidate
	}

	if current != nil {
		segments = append(segments, *current)
	}
	return segments, nil
}

// mergeShortInterruptions は短時間の割り込みをはさんだ前後の区間を結合する。
//
// ウィンドウを切り替えたあと WindowMergeWindow 以内に元のウィンドウへ戻った場合、
// 前後を1つの TimeIs にする。あいだに挟まった割り込み側のウィンドウは TimeIs にしない。
//
// 割り込みは1つとは限らない。短時間のうちに複数のウィンドウを経由して戻ることもあるため、
// 戻るまでが WindowMergeWindow 以内であれば、間に何個挟まっていても結合する。
// 間の区間は必ずその時間内に収まっているので、いずれも「1分以内だけ操作された
// 割り込み側ウィンドウ」にあたり、TimeIs にしない（要件 §6.3）。
func mergeShortInterruptions(segments []windowSegment) []windowSegment {
	for {
		merged := false
		for i := 0; i < len(segments) && !merged; i++ {
			for j := i + 2; j < len(segments); j++ {
				// 元のウィンドウを離れてから戻るまでの時間で判定する。
				if segments[j].start.Sub(segments[i].end) > WindowMergeWindow {
					break
				}
				if !segments[i].sameWindow(segments[j]) {
					continue
				}

				combined := segments[i]
				combined.end = segments[j].end
				combined.eventIDs = append(combined.eventIDs, segments[j].eventIDs...)
				segments[i] = combined
				segments = append(segments[:i+1], segments[j+1:]...)
				merged = true
				break
			}
		}
		if !merged {
			return segments
		}
	}
}
