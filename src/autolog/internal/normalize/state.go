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

		intervals := buildStateIntervals(changes, group.mergeWindow, carriedBySource[group.source])

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

// bluetoothDeviceName は機器名から先頭の `LE_` を落とす。
//
// 1台のヘッドホンがクラシックと LE の両方でつながると、
// Android は `WH-1000XM6` と `LE_WH-1000XM6` という別々の名前で通知してくる。
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
func buildStateIntervals(changes []stateChange, mergeWindow time.Duration, carried map[string]stateInterval) []stateInterval {
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

		for _, change := range byKey[key] {
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
				current.eventIDs = append(current.eventIDs, change.eventID)
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
