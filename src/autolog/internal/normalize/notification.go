package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// notifications は Android の通知を Kmemo へ変換する。
//
// ルール（要件 §13）:
//   - 常駐通知は除外する
//   - 除外リストに当たるアプリの通知は除外する
//   - タイトルと本文が両方空なら除外する
//   - 同一通知IDの更新は最終状態だけを記録する
//   - 同じ内容が短時間に再通知された場合は1件にまとめる
//   - RelatedTime は最初の通知時刻
//
// 本文は Android に表示された内容だけを使う。補完はしない。
//
// 内容の重複排除は取り込み1回のなかだけでは完結しない。
// 前回までに記録した内容と時刻を carried で受け取り、今回の分を足して返す。
// これがないと、取り込みを短い間隔で走らせたときに窓の切れ目で重複がすり抜ける。
func notifications(device rawlog.Device, events []*rawlog.Event, opts Options, carried []rawlog.NotificationSeen) ([]Proposal, []rawlog.NotificationSeen, error) {
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
			return nil, nil, err
		}

		if payload.Ongoing {
			continue
		}
		// パッケージ名・アプリ名・チャンネルIDを見る。
		// パッケージ名とアプリ名の両方を見るのは、
		// 収集元によってはパッケージ名が取れないことがあるため。
		// チャンネルIDも見るのは、ダウンロード完了のように
		// 「同じアプリの一部の通知だけ落としたい」場合があるため。
		// カテゴリは msg / sys のような短い固定語彙で、部分一致だと
		// 利用者の書いたパターンが意図せず当たるので照合しない。
		// チャンネルIDはこの項目ができる前の生ログでは空なので、
		// 空のときは照合しない。空文字に当たる正規表現を書かれても
		// 過去の分がまとめて消えないようにする。
		if opts.NotificationDenyList.Matches(payload.PackageName) ||
			opts.NotificationDenyList.Matches(payload.AppLabel) ||
			(payload.ChannelID != "" && opts.NotificationDenyList.Matches(payload.ChannelID)) {
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
	// 前回までに記録した時刻を引き継ぐので、窓の切れ目をまたいでも効く。
	seen := map[string]time.Time{}
	for _, item := range carried {
		if item.Device != device {
			continue
		}
		seen[item.ContentHash] = item.LastAt
	}
	notificationEvents := filterType(events, rawlog.EventNotification)

	for _, key := range order {
		item := byKey[key]
		content := notificationContent(item.payload)
		if content == "" {
			continue
		}

		startTime := notificationEvents[item.firstAt].StartTime
		hash := contentHash(content)

		if previous, ok := seen[hash]; ok {
			if startTime.Sub(previous) <= NotificationDedupeWindow {
				continue
			}
		}
		// 記録したものだけを次の判定の起点にする。
		// 捨てたものを起点にすると、鳴り続ける通知が永久に記録されなくなる。
		seen[hash] = startTime

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

	// map の反復順は不定なので、並べてから返して結果を安定させる。
	hashes := make([]string, 0, len(seen))
	for hash := range seen {
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)

	stillSeen := make([]rawlog.NotificationSeen, 0, len(hashes))
	for _, hash := range hashes {
		stillSeen = append(stillSeen, rawlog.NotificationSeen{
			Device:      device,
			ContentHash: hash,
			LastAt:      seen[hash],
		})
	}
	return results, stillSeen, nil
}

// contentHash は Kmemo 本文の識別子を返す。
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
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
