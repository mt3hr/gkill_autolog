package normalize

import (
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// windowSegment は同じアプリを操作し続けた1区間。
type windowSegment struct {
	// title は TimeIs のタイトルであり、区間の同一判定にも使う。
	title    string
	start    time.Time
	end      time.Time
	eventIDs []string
}

// duration は区間の長さ。
func (s windowSegment) duration() time.Duration {
	return s.end.Sub(s.start)
}

// sameWindow は同じアプリの区間かを返す。
//
// 判定はアプリ名だけで行う。ウィンドウタイトルは見ない。
// タブやファイルを切り替えただけで区間が切れると、同じタイトルの TimeIs が
// 延々と並び、1分未満の断片は MinWindowDuration で落ちて間に穴が空く。
func (s windowSegment) sameWindow(other windowSegment) bool {
	return s.title == other.title
}

// segmentTitle は区間のタイトルを決める（要件 §6.4）。
//
// gkill へ出すのは表示上のアプリ名だけで、ウィンドウタイトルは使わない。
// 開いているページ名やファイル名がタイトルへ混ざらないようにするため。
// 生ログには window_title を残してあるので、観測できた事実そのものは失われない。
//
// アプリ名がどうしても取れなかったときだけ、タイトルを空にしないための
// 保険としてウィンドウタイトルを使う。
func segmentTitle(payload rawlog.InputPayload) string {
	switch {
	case payload.AppDisplayName != "":
		return payload.AppDisplayName
	case payload.AppName != "":
		return payload.AppName
	default:
		return payload.WindowTitle
	}
}

// windowSessions は input イベントをアプリごとの TimeIs セッションへ変換する。
//
// タイトルには表示上のアプリ名だけを載せる。Android の appUsages と同じ粒度になる。
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
	pendingFrom := len(segments)
	if opts.Cutoff.Sub(segments[pendingFrom-1].end) < IdleTimeout {
		pendingFrom--
	}
	// 続いていなくても、結合幅の内側で終わった区間はまだ確定できない。
	// 割り込みの最中に Cutoff が来た場合、直後に元のアプリへ戻れば前後を
	// 結合すべきで、ここで確定させると取り込む間隔しだいで1本にも2本にもなる。
	// アプリ利用・メディア再生の持ち越し（mergeIntervals）と同じ考え方だが、
	// 窓のセッションは生の入力から組み直すので、区間ではなくカーソルを引き戻す。
	for pendingFrom > 0 && opts.Cutoff.Sub(segments[pendingFrom-1].end) <= WindowMergeWindow {
		pendingFrom--
	}
	var pendingStart time.Time
	if pendingFrom < len(segments) {
		pendingStart = segments[pendingFrom].start
		segments = segments[:pendingFrom]
	}

	proposals := make([]Proposal, 0, len(segments))
	for _, segment := range segments {
		// 一瞬触っただけのアプリは記録しない。
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
			Title:          segment.title,
			StartTime:      timePtr(segment.start),
			EndTime:        timePtr(segment.end),
		})
	}
	return proposals, pendingStart, nil
}

// buildWindowSegments は入力イベントを連続した区間へまとめる。
//
// 区間が切れるのは次の2つ。
//   - アプリが変わったとき（同じアプリのままタブやファイルが変わっても切れない）
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
			title:    segmentTitle(payload),
			start:    event.StartTime,
			end:      event.StartTime,
			eventIDs: []string{event.EventID},
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
// アプリを切り替えたあと WindowMergeWindow 以内に元のアプリへ戻った場合、
// 前後を1つの TimeIs にする。あいだに挟まった割り込み側のアプリは TimeIs にしない。
//
// 割り込みは1つとは限らない。短時間のうちに複数のアプリを経由して戻ることもあるため、
// 戻るまでが WindowMergeWindow 以内であれば、間に何個挟まっていても結合する。
// 間の区間は必ずその時間内に収まっているので、いずれも「1分以内だけ操作された
// 割り込み側のアプリ」にあたり、TimeIs にしない（要件 §6.3）。
func mergeShortInterruptions(segments []windowSegment) []windowSegment {
	for {
		merged := false
		for i := 0; i < len(segments) && !merged; i++ {
			for j := i + 2; j < len(segments); j++ {
				// 元のアプリを離れてから戻るまでの時間で判定する。
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
