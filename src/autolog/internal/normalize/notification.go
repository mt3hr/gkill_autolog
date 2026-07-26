package normalize

import (
	"strings"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// notifications は Android の通知を Kmemo へ変換する。
//
// ルール（要件 §13）:
//   - 常駐通知は除外する
//   - タイトルと本文が両方空なら除外する
//   - 同一通知IDの更新は最終状態だけを記録する
//   - 同じ内容が短時間に再通知された場合は1件にまとめる
//   - RelatedTime は最初の通知時刻
//
// 本文は Android に表示された内容だけを使う。補完はしない。
func notifications(device rawlog.Device, events []*rawlog.Event) ([]Proposal, error) {
	type pending struct {
		firstAt  int
		payload  rawlog.NotificationPayload
		eventIDs []string
	}

	var (
		order   []string
		byKey   = map[string]*pending{}
		results []Proposal
	)

	// 同一通知IDの更新をまとめる。キーが無い通知は内容そのものをキーにする。
	for index, event := range filterType(events, rawlog.EventNotification) {
		payload, err := rawlog.DecodePayload[rawlog.NotificationPayload](event)
		if err != nil {
			return nil, err
		}

		if payload.Ongoing {
			continue
		}
		if strings.TrimSpace(payload.Title) == "" && strings.TrimSpace(payload.Body) == "" {
			continue
		}

		key := payload.NotificationKey
		if key == "" {
			key = payload.PackageName + "\x00" + payload.Title + "\x00" + payload.Body
		}

		if existing, ok := byKey[key]; ok {
			// 最終状態を採用する。RelatedTime は最初の通知時刻のまま据え置く。
			existing.payload = payload
			existing.eventIDs = append(existing.eventIDs, event.EventID)
			continue
		}

		byKey[key] = &pending{
			firstAt:  index,
			payload:  payload,
			eventIDs: []string{event.EventID},
		}
		order = append(order, key)
	}

	// 同内容の短時間の再通知をまとめる。
	seen := map[string]int{}
	notificationEvents := filterType(events, rawlog.EventNotification)

	for _, key := range order {
		item := byKey[key]
		content := notificationContent(item.payload)
		if content == "" {
			continue
		}

		startTime := notificationEvents[item.firstAt].StartTime

		if previousIndex, ok := seen[content]; ok {
			previous := notificationEvents[previousIndex].StartTime
			if startTime.Sub(previous) <= NotificationDedupeWindow {
				continue
			}
		}
		seen[content] = item.firstAt

		results = append(results, Proposal{
			ID:             makeID(KindKmemo, SourceNotification, item.eventIDs),
			Kind:           KindKmemo,
			Device:         device,
			Source:         SourceNotification,
			SourceEventIDs: item.eventIDs,
			Content:        content,
			RelatedTime:    timePtr(startTime),
		})
	}
	return results, nil
}

// notificationContent は Kmemo の本文を組み立てる。
//
// タイトルだけの場合はタイトルのみ、本文だけの場合は本文のみを載せる（要件 §13）。
func notificationContent(payload rawlog.NotificationPayload) string {
	var lines []string
	if label := strings.TrimSpace(payload.AppLabel); label != "" {
		lines = append(lines, "アプリ："+label)
	}
	if title := strings.TrimSpace(payload.Title); title != "" {
		lines = append(lines, "タイトル："+title)
	}
	if body := strings.TrimSpace(payload.Body); body != "" {
		lines = append(lines, "本文："+body)
	}
	if len(lines) <= 1 {
		// アプリ名しか無いものは記録しない。
		return ""
	}
	return strings.Join(lines, "\n")
}
