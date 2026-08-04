package normalize

import (
	"slices"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// timedInterval は結合の対象になる区間1つ。
type timedInterval struct {
	// key は結合の単位。アプリ利用ならアプリ名、メディア再生ならタイトル。
	key string
	// title は TimeIs に載せる文字列。key と同じこともある。
	title    string
	start    time.Time
	end      time.Time
	eventIDs []string
}

// duration は区間の長さを返す。
func (i timedInterval) duration() time.Duration {
	return i.end.Sub(i.start)
}

// mergeIntervals は同じキーの区間を mergeWindow 以内で結合する。
//
// 生ログは「同じアプリを使い続けているのに、画面遷移のたびに区切られる」形で
// 届くことがある。そのまま Kyou にすると数十秒の TimeIs が延々と並ぶので、
// 途切れが mergeWindow 以内なら1本にまとめる。
//
// 結合は normalize の1回の呼び出しのなかだけでは完結しない。
// 末尾の区間は次のバッチの先頭とつながるかもしれないからで、
// そのままだと取り込む間隔を変えただけで結果が変わってしまう。
// そこで末尾は確定させず、次回へ持ち越す。
//
// 持ち越すのは cutoff から mergeWindow 以内に終わった区間だけにする。
// それより前に終わっていれば、結合相手になりうるイベントは
// cutoff より前に始まっているはずで、今回のバッチに入っている。
// つまりもう結合相手は現れないので確定してよい。
// この条件があるおかげで、持ち越しが溜まり続けることはなく、
// 遅くとも次の取り込みで必ず書き出される。
//
// carried は前回の呼び出しが返した持ち越し。device と source が一致するものだけ使う。
func mergeIntervals(
	device rawlog.Device,
	source string,
	intervals []timedInterval,
	carried []rawlog.OpenStateInterval,
	mergeWindow time.Duration,
	cutoff time.Time,
) ([]timedInterval, []rawlog.OpenStateInterval) {
	byKey := map[string][]timedInterval{}
	var order []string
	for _, interval := range intervals {
		if _, seen := byKey[interval.key]; !seen {
			order = append(order, interval.key)
		}
		byKey[interval.key] = append(byKey[interval.key], interval)
	}

	// 前回から持ち越した区間を続きとして扱えるようにする。
	carriedByKey := map[string]timedInterval{}
	for _, open := range carried {
		if open.Device != device || open.Source != source || open.End == nil {
			continue
		}
		carriedByKey[open.Key] = timedInterval{
			key:      open.Key,
			title:    open.Title,
			start:    open.Start,
			end:      *open.End,
			eventIDs: slices.Clone(open.EventIDs),
		}
	}

	// 今回そのキーのイベントが1件も無くても、持ち越した区間は処理する必要がある。
	// map の反復順は不定なので、並べてから足して結果を安定させる。
	carriedOnly := make([]string, 0, len(carriedByKey))
	for key := range carriedByKey {
		if _, seen := byKey[key]; !seen {
			carriedOnly = append(carriedOnly, key)
		}
	}
	slices.Sort(carriedOnly)
	order = append(order, carriedOnly...)

	var (
		settled []timedInterval
		pending []rawlog.OpenStateInterval
	)

	for _, key := range order {
		var current *timedInterval
		if carriedInterval, ok := carriedByKey[key]; ok {
			interval := carriedInterval
			current = &interval
		}

		for _, interval := range byKey[key] {
			if current == nil {
				next := interval
				next.eventIDs = slices.Clone(interval.eventIDs)
				current = &next
				continue
			}
			// 持ち越しより丸ごと過去の区間は結合しない。カーソルの引き戻しで
			// 書き込み済みの区間を読み直すと起きる形で、途切れなく続いている
			// わけではない (次の結合判定は負のギャップでも真になってしまう)。
			// 結合すると書き込み済みイベントと未書き込みの持ち越し末尾が
			// 混ざった提案になり、一部重複として丸ごと破棄されて末尾が失われる。
			// 独立して出せば、読み直した分は台帳の包含判定で正しく除外される。
			// (持ち越しが無ければ区間は開始時刻順に並ぶので、ここは通らない)
			if interval.end.Before(current.start) {
				next := interval
				next.eventIDs = slices.Clone(interval.eventIDs)
				settled = append(settled, next)
				continue
			}
			if interval.start.Sub(current.end) <= mergeWindow {
				// 同じキーの区間が途切れずに続いている。1本にまとめる。
				// 終了時刻は後戻りさせない。区間が重なって届くことがあるため。
				if interval.end.After(current.end) {
					current.end = interval.end
				}
				current.eventIDs = append(current.eventIDs, interval.eventIDs...)
				continue
			}
			settled = append(settled, *current)
			next := interval
			next.eventIDs = slices.Clone(interval.eventIDs)
			current = &next
		}

		if current == nil {
			continue
		}
		if cutoff.Sub(current.end) <= mergeWindow {
			endTime := current.end
			pending = append(pending, rawlog.OpenStateInterval{
				Device:   device,
				Source:   source,
				Key:      current.key,
				Title:    current.title,
				Start:    current.start,
				End:      &endTime,
				EventIDs: current.eventIDs,
			})
			continue
		}
		settled = append(settled, *current)
	}
	return settled, pending
}
