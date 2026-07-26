package normalize

import (
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
// 返す2つ目の値は継続中区間のうち最も早い開始時刻。
func connectionStates(device rawlog.Device, events []*rawlog.Event) ([]Proposal, time.Time, error) {
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
				return stateChange{
					key:       payload.DeviceName,
					title:     "Bluetooth " + payload.DeviceName,
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
		proposals    []Proposal
		earliestOpen time.Time
	)

	for _, group := range groups {
		var changes []stateChange
		for _, event := range filterType(events, group.eventType) {
			change, err := group.decode(event)
			if err != nil {
				return nil, time.Time{}, err
			}
			if change.key == "" {
				continue
			}
			changes = append(changes, change)
		}

		intervals := buildStateIntervals(changes, group.mergeWindow)
		for _, interval := range intervals {
			if interval.open {
				// 切断を観測していない＝継続中。次回バッチに回す。
				if earliestOpen.IsZero() || interval.start.Before(earliestOpen) {
					earliestOpen = interval.start
				}
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

	return proposals, earliestOpen, nil
}

// buildStateIntervals は接続・切断の並びを区間へまとめ、短い切断を結合する。
//
// 同時に複数の対象へつながっている場合は、それぞれ別の区間として扱う。
func buildStateIntervals(changes []stateChange, mergeWindow time.Duration) []stateInterval {
	// キーごとに独立して処理する。
	byKey := map[string][]stateChange{}
	var order []string
	for _, change := range changes {
		if _, seen := byKey[change.key]; !seen {
			order = append(order, change.key)
		}
		byKey[change.key] = append(byKey[change.key], change)
	}

	var intervals []stateInterval
	for _, key := range order {
		var (
			current *stateInterval
			result  []stateInterval
		)

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
					if change.at.Sub(previous.end) <= mergeWindow {
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
