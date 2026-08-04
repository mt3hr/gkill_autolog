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
		lastTime time.Time
		payload  rawlog.NotificationPayload
		eventIDs []string
	}

	var (
		groups  []*pending
		current = map[string]*pending{}
		results []Proposal
	)

	// 同一通知IDの更新をまとめる。キーが無い通知は内容そのものをキーにする。
	//
	// まとめるのは NotificationUpdateWindow 以内に続いた更新だけ。
	// Android は通知IDを使い回すので、朝の通知と夜の通知が同じキーを
	// 持つことがある。窓なしでまとめると、処理する窓 (通常1日、初回は7日) の
	// 中の別々の通知が「最終状態だけ」に潰れ、結果が取り込みの間隔に依存する。
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

		if existing, ok := current[key]; ok &&
			event.StartTime.Sub(existing.lastTime) <= NotificationUpdateWindow {
			// 同じ通知の更新。最終状態を採用する。RelatedTime は最初の通知時刻のまま据え置く。
			existing.payload = payload
			existing.lastTime = event.StartTime
			existing.eventIDs = append(existing.eventIDs, event.EventID)
			continue
		}

		// 新しい通知。同じキーでも窓を超えて間が空いていれば別の通知として扱う。
		group := &pending{
			firstAt:  index,
			lastTime: event.StartTime,
			payload:  payload,
			eventIDs: []string{event.EventID},
		}
		current[key] = group
		groups = append(groups, group)
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

	for _, item := range groups {
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
			ID:             makeID(KindKmemo, SourceNotification, device, item.eventIDs),
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
