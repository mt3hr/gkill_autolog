package normalize

import (
	"slices"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// stateChange は接続状態の変化1件。
type stateChange struct {
	key       string
	title     string
	connected bool
	at        time.Time
	eventID   string
	// marker は接続そのものの変化ではなく「観測の切れ目」であることを表す。
	// スリープ・シャットダウン・収集停止から先は接続を観測できないので、
	// 開いている区間をこの時点で閉じる。
	marker bool
}

// stateInterval は接続していた1区間。
type stateInterval struct {
	key      string
	title    string
	start    time.Time
	end      time.Time
	eventIDs []string
	// open はまだ切断を観測していないことを表す。
	open bool
}

// connectionStates は Wi-Fi・Bluetooth・充電の接続期間を TimeIs へ変換する。
//
// 短時間の切断は結合する。結合幅は種類ごとに要件で決まっている。
//   - Wi-Fi: 30 秒以内の再接続（SSID 単位、BSSID は扱わない）
//   - Bluetooth: 1 分以内の再接続（機器名単位、複数機器は別々の TimeIs）
//   - 充電: 30 秒以内の再開（残量・方式は記録しない）
//
// 返す2つ目の値は cutoff 時点でまだ閉じていない区間。呼び出し側が次回へ持ち越す。
func connectionStates(device rawlog.Device, events []*rawlog.Event, opts Options, carried []rawlog.OpenStateInterval) ([]Proposal, []rawlog.OpenStateInterval, error) {
	groups := []struct {
		eventType   rawlog.EventType
		source      string
		mergeWindow time.Duration
		decode      func(*rawlog.Event) (stateChange, error)
	}{
		{
			eventType:   rawlog.EventWifi,
			source:      SourceWifi,
			mergeWindow: WifiMergeWindow,
			decode: func(e *rawlog.Event) (stateChange, error) {
				payload, err := rawlog.DecodePayload[rawlog.WifiPayload](e)
				if err != nil {
					return stateChange{}, err
				}
				return stateChange{
					key:       payload.SSID,
					title:     "Wi-Fi " + payload.SSID,
					connected: payload.Connected,
					at:        e.StartTime,
					eventID:   e.EventID,
				}, nil
			},
		},
		{
			eventType:   rawlog.EventBluetooth,
			source:      SourceBluetooth,
			mergeWindow: BluetoothMergeWindow,
			decode: func(e *rawlog.Event) (stateChange, error) {
				payload, err := rawlog.DecodePayload[rawlog.BluetoothPayload](e)
				if err != nil {
					return stateChange{}, err
				}
				name := bluetoothDeviceName(payload.DeviceName)
				return stateChange{
					key:       name,
					title:     "Bluetooth " + name,
					connected: payload.Connected,
					at:        e.StartTime,
					eventID:   e.EventID,
				}, nil
			},
		},
		{
			eventType:   rawlog.EventPower,
			source:      SourceCharge,
			mergeWindow: ChargeMergeWindow,
			decode: func(e *rawlog.Event) (stateChange, error) {
				payload, err := rawlog.DecodePayload[rawlog.PowerPayload](e)
				if err != nil {
					return stateChange{}, err
				}
				// 充電は対象が1つしかないのでキーは固定。
				return stateChange{
					key:       "charge",
					title:     "充電",
					connected: payload.Charging,
					at:        e.StartTime,
					eventID:   e.EventID,
				}, nil
			},
		},
	}

	// 観測の切れ目。スリープ・シャットダウン・収集停止から先は接続を観測できない。
	// 開いている接続区間はこの時点で閉じる。閉じないと、電源が入っていない時間まで
	// 「接続中」の区間に含まれてしまう。復帰後は収集側がつながっているものを
	// 記録し直すので、そこから新しい区間が始まる。
	// ロックや画面消灯は含めない。画面が消えていても接続と観測は続いている。
	//
	// 例外は補完されたロック (Recovered)。収集の異常終了後の起動が
	// 「最後に生きていた時刻」として書くもので、そこから先の観測は無い。
	// これを切れ目にしないと、クラッシュや電源断で marker が書かれないまま
	// 再起動し、電源が入っていなかった時間まで接続区間がつながってしまう
	// (再起動後の最初の観測は既存の開区間に吸収されるため)。
	var observationEnds []stateChange
	for _, event := range filterType(events, rawlog.EventSession) {
		payload, err := rawlog.DecodePayload[rawlog.SessionPayload](event)
		if err != nil {
			return nil, nil, err
		}
		isEnd := false
		switch payload.Action {
		case rawlog.SessionSuspend, rawlog.SessionShutdown, rawlog.SessionCollectorStop:
			isEnd = true
		default:
			isEnd = payload.Recovered
		}
		if isEnd {
			observationEnds = append(observationEnds, stateChange{
				connected: false,
				at:        event.StartTime,
				eventID:   event.EventID,
				marker:    true,
			})
		}
	}

	var (
		proposals []Proposal
		stillOpen []rawlog.OpenStateInterval
	)

	// 前回から持ち越した区間を種別・キーで引けるようにする。
	// end_time が入っていれば「切断は観測したが、まだ結合幅の内側にいる」区間。
	carriedBySource := map[string]map[string]stateInterval{}
	for _, open := range carried {
		if open.Device != device {
			continue
		}
		if _, ok := carriedBySource[open.Source]; !ok {
			carriedBySource[open.Source] = map[string]stateInterval{}
		}
		interval := stateInterval{
			key:      open.Key,
			title:    open.Title,
			start:    open.Start,
			end:      open.Start,
			eventIDs: open.EventIDs,
			open:     open.End == nil,
		}
		if open.End != nil {
			interval.end = *open.End
		}
		carriedBySource[open.Source][open.Key] = interval
	}

	for _, group := range groups {
		var changes []stateChange
		for _, event := range filterType(events, group.eventType) {
			change, err := group.decode(event)
			if err != nil {
				return nil, nil, err
			}
			if change.key == "" {
				continue
			}
			changes = append(changes, change)
		}

		intervals := buildStateIntervals(changes, group.mergeWindow, carriedBySource[group.source], observationEnds)

		// キーごとの最後の区間だけが、次のバッチの再接続と結合されうる。
		lastByKey := map[string]int{}
		for i, interval := range intervals {
			lastByKey[interval.key] = i
		}

		for i, interval := range intervals {
			if interval.open {
				// 切断を観測していない＝継続中。
				// カーソルは引き戻さず、区間そのものを次回へ持ち越す。
				// 引き戻していると、常時つないだままの機器があるだけで
				// 処理位置が永久に進まなくなる。
				stillOpen = append(stillOpen, rawlog.OpenStateInterval{
					Device:   device,
					Source:   group.source,
					Key:      interval.key,
					Title:    interval.title,
					Start:    interval.start,
					EventIDs: interval.eventIDs,
				})
				continue
			}

			// 切断を観測していても、結合幅の内側で終わっているうちは確定させない。
			// ここで書き出してしまうと、次のバッチに再接続が来ても
			// 結合する相手がもう残っておらず、取り込む間隔によって
			// 区間が1本にも2本にもなってしまう。
			if lastByKey[interval.key] == i && opts.Cutoff.Sub(interval.end) <= group.mergeWindow {
				endTime := interval.end
				stillOpen = append(stillOpen, rawlog.OpenStateInterval{
					Device:   device,
					Source:   group.source,
					Key:      interval.key,
					Title:    interval.title,
					Start:    interval.start,
					End:      &endTime,
					EventIDs: interval.eventIDs,
				})
				continue
			}

			proposals = append(proposals, Proposal{
				ID:             makeID(KindTimeIs, group.source, interval.eventIDs),
				Kind:           KindTimeIs,
				Device:         device,
				Source:         group.source,
				SourceEventIDs: interval.eventIDs,
				Title:          interval.title,
				StartTime:      timePtr(interval.start),
				EndTime:        timePtr(interval.end),
			})
		}
	}

	return proposals, stillOpen, nil
}

// mergeChangesWithEnds はキーのイベント列へ観測の切れ目を時刻順に差し込む。
//
// 同時刻では実イベントを先にする。切れ目は「その時点までの観測」を閉じるもので、
// 同時刻に観測された変化はまだ観測の内側にあるため。
func mergeChangesWithEnds(changes []stateChange, ends []stateChange) []stateChange {
	if len(ends) == 0 {
		return changes
	}
	merged := make([]stateChange, 0, len(changes)+len(ends))
	i, j := 0, 0
	for i < len(changes) && j < len(ends) {
		if !changes[i].at.After(ends[j].at) {
			merged = append(merged, changes[i])
			i++
		} else {
			merged = append(merged, ends[j])
			j++
		}
	}
	merged = append(merged, changes[i:]...)
	merged = append(merged, ends[j:]...)
	return merged
}

// bluetoothDeviceName は機器名から先頭の `LE_` を落とす。
//
// 1台のヘッドホンがクラシックと LE の両方でつながると、
// Android は `Headphones` と `LE_Headphones` という別々の名前で通知してくる。
// そのままだと同じ機器の TimeIs が2本並び、2台つけているように見える。
// 接頭辞を落として同じキーにすれば、buildStateIntervals が
// 「既に接続中」として吸収する。
func bluetoothDeviceName(name string) string {
	if trimmed, ok := strings.CutPrefix(name, "LE_"); ok {
		return trimmed
	}
	if trimmed, ok := strings.CutPrefix(name, "le_"); ok {
		return trimmed
	}
	return name
}

// buildStateIntervals は接続・切断の並びを区間へまとめ、短い切断を結合する。
//
// 同時に複数の対象へつながっている場合は、それぞれ別の区間として扱う。
// ends は観測の切れ目で、どのキーの開区間もその時点で閉じる。
func buildStateIntervals(changes []stateChange, mergeWindow time.Duration, carried map[string]stateInterval, ends []stateChange) []stateInterval {
	// キーごとに独立して処理する。
	byKey := map[string][]stateChange{}
	var order []string
	for _, change := range changes {
		if _, seen := byKey[change.key]; !seen {
			order = append(order, change.key)
		}
		byKey[change.key] = append(byKey[change.key], change)
	}

	// 持ち越した開区間のキーも処理対象にする。
	// 今回そのキーのイベントが1件も無くても、開いたままとして次回へ渡す必要がある。
	// map の反復順は不定なので、並べてから足して結果を安定させる。
	carriedOnly := make([]string, 0, len(carried))
	for key := range carried {
		if _, seen := byKey[key]; !seen {
			carriedOnly = append(carriedOnly, key)
		}
	}
	slices.Sort(carriedOnly)
	order = append(order, carriedOnly...)

	var intervals []stateInterval
	for _, key := range order {
		var (
			current *stateInterval
			result  []stateInterval
		)
		// 前回から持ち越した区間があれば、それを続きとして扱う。
		if carriedInterval, ok := carried[key]; ok {
			interval := carriedInterval
			interval.eventIDs = slices.Clone(carriedInterval.eventIDs)
			if interval.open {
				// まだ開いたまま。こうすると開始イベントが
				// 今回の窓の外にあっても正しく閉じられる。
				current = &interval
			} else {
				// 切断は観測済みだが、結合幅の内側で終わっている。
				// 確定済みの並びへ入れておくと、今回のバッチに再接続が来たときに
				// 下の「短い切断の結合」がつなぎ直してくれる。
				result = append(result, interval)
			}
		}

		for _, change := range mergeChangesWithEnds(byKey[key], ends) {
			if change.connected {
				if current != nil {
					// 既に接続中。重複した接続通知なので無視する。
					current.eventIDs = append(current.eventIDs, change.eventID)
					continue
				}
				// 直前の区間が短い切断で終わっていれば、そこへつなぎ直す。
				if len(result) > 0 {
					previous := &result[len(result)-1]
					gap := change.at.Sub(previous.end)

					// 直前の区間の内側にある接続は、カーソルの引き戻しで
					// 同じイベントを読み直しただけ。新しい区間を作らない。
					// 差を取るだけで判定すると負の差が結合幅の内側に入ってしまい、
					// 終了が開始より前になった区間ができる。
					if gap < 0 {
						if !change.at.Before(previous.start) {
							previous.eventIDs = append(previous.eventIDs, change.eventID)
							continue
						}
					} else if gap <= mergeWindow {
						previous.eventIDs = append(previous.eventIDs, change.eventID)
						current = previous
						current.open = true
						result = result[:len(result)-1]
						continue
					}
				}
				current = &stateInterval{
					key:      change.key,
					title:    change.title,
					start:    change.at,
					end:      change.at,
					eventIDs: []string{change.eventID},
					open:     true,
				}
				continue
			}

			if current == nil {
				// 対応する接続が無い切断。区間を作れない。
				continue
			}
			if change.at.Before(current.start) {
				// 開始より前の切断。カーソルの引き戻しで読み直しただけなので、
				// 区間を閉じる相手にはしない。これを閉じてしまうと
				// 終了が開始より前の区間ができる。
				// 観測の切れ目は区間の根拠でもないので、イベントIDにも足さない。
				if !change.marker {
					current.eventIDs = append(current.eventIDs, change.eventID)
				}
				continue
			}
			current.end = change.at
			current.eventIDs = append(current.eventIDs, change.eventID)
			current.open = false
			result = append(result, *current)
			current = nil
		}

		if current != nil {
			result = append(result, *current)
		}
		intervals = append(intervals, result...)
	}
	return intervals
}
