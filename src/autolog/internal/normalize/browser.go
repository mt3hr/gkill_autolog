package normalize

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// excludedURLPrefixes は内容を閲覧するページではないため URLog にしない URL。
//
// 新しいタブ・Chrome の設定画面・拡張機能画面・about:blank が該当する（要件 §7.1）。
// Google 検索結果・localhost・gkill 自身のページは保存対象なので除外しない。
var excludedURLPrefixes = []string{
	"chrome://",
	"chrome-extension://",
	"chrome-search://",
	"chrome-untrusted://",
	"edge://",
	"about:",
	"devtools://",
	"view-source:",
}

// isExcludedURL は閲覧内容として扱わない URL かを返す。
func isExcludedURL(rawURL string) bool {
	if strings.TrimSpace(rawURL) == "" {
		return true
	}
	for _, prefix := range excludedURLPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return true
		}
	}
	return false
}

// mediaServiceHosts は media_play が担当するサービス。
var mediaServiceHosts = []string{
	"youtube.com",
	"music.youtube.com",
	"youtu.be",
}

// isMediaServiceURL は media_play 側が担当するサービスの URL かを返す。
//
// YouTube と YouTube Music は「個別コンテンツの URLog だけを残す」（要件 §3、§8.1）。
// 閲覧区間からも URLog を作ると、次の2つの問題が起きる。
//   - 同じ動画が media_play と browser_view の両方から二重に登録される
//   - トップページや検索結果など、個別コンテンツでないページまで URLog になる
//
// そこで、これらのサービスは browser_view からは一切 URLog を作らず、
// media_play 経由だけで記録する。再生していない閲覧は記録されないが、
// 要件が「個別コンテンツだけ」と定めているのでそれでよい。
//
// ホスト名で判定する。文字列の部分一致だと
// https://example.com/?ref=youtube.com のような URL まで拾ってしまう。
func isMediaServiceURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")

	return slices.Contains(mediaServiceHosts, host)
}

// browserViews は browser_view イベントを URLog へ変換する。
//
// 1回の連続閲覧が MinViewDuration 以上のものだけを残す。
// 同一 URL を再度表示した場合は別セッションとして扱い、離れた区間の時間は合算しない（要件 §7.1）。
// RelatedTime は閲覧開始時刻、ページタイトルと URL はそのまま使う。
//
// 広告や中継ページなど機械的に判別できない除外は denyList で行う。
// 2つ目の戻り値は書き込む URLog の一覧で、内容確認用に出力する。
func browserViews(device rawlog.Device, events []*rawlog.Event, denyList *DenyList) ([]Proposal, []URLCandidate, error) {
	var (
		proposals  []Proposal
		candidates []URLCandidate
	)

	for _, event := range filterType(events, rawlog.EventBrowserView) {
		payload, err := rawlog.DecodePayload[rawlog.BrowserViewPayload](event)
		if err != nil {
			return nil, nil, err
		}

		if isExcludedURL(payload.URL) {
			continue
		}
		// YouTube / YouTube Music は media_play 側が個別コンテンツだけを記録する。
		if isMediaServiceURL(payload.URL) {
			continue
		}
		// 利用者が列挙した除外パターン。広告や中継ページなど。
		if denyList.Matches(payload.URL) {
			continue
		}
		if event.EndTime == nil {
			// 終了時刻が無い＝区間が確定していない。不完全なイベントは書き込まない（要件 §17）。
			continue
		}
		duration := event.EndTime.Sub(event.StartTime)
		if duration < MinViewDuration {
			continue
		}
		// 履歴DBに存在するだけで前面表示を確認できないページは自動登録しない（要件 §12）。
		if payload.Source == "chrome_history_db" && !payload.BrowserFocused {
			continue
		}

		id := makeID(KindURLog, SourceBrowser, []string{event.EventID})
		proposals = append(proposals, Proposal{
			ID:             id,
			Kind:           KindURLog,
			Device:         device,
			Source:         SourceBrowser,
			SourceEventIDs: []string{event.EventID},
			URL:            payload.URL,
			Title:          payload.Title,
			RelatedTime:    timePtr(event.StartTime),
		})
		candidates = append(candidates, URLCandidate{
			ProposalID:      id,
			URL:             payload.URL,
			Title:           payload.Title,
			RelatedTime:     rawlog.FormatTime(event.StartTime),
			DurationSeconds: int(duration / time.Second),
		})
	}
	return proposals, candidates, nil
}

// mediaPlays は media_play イベントを Kyou へ変換する。
//
// 一時停止時間と広告再生時間は収集側で既に除いてある。
//
// URL が確定できたかどうかで作る Kyou の種類が変わる。
//   - 確定できた: URLog。Chrome 拡張はページの URL を読めるので常にこちら
//   - 確定できない: TimeIs。Android の MediaSession はタイトルしか返さないことが多い
//
// どちらも実再生時間が MinPlayedSeconds 以上のものだけを残す（要件 §8.1）。
// URLog は同じ動画や楽曲でも再生の都度1件作り、RelatedTime は再生開始時刻にする。
//
// TimeIs は同じタイトルの再生が WindowMergeWindow 以内に続いていれば1本にまとめる。
// Android の MediaSession は曲を止めずに聴いていても区間を細かく切って返すため、
// まとめないと同じ曲の TimeIs が並ぶ。
//
// 検索URLや推測したURLは作らない（要件 §8.2）。TimeIs にはタイトル・アーティストという
// 観測できた事実だけを載せ、URL の代わりを埋め合わせることはしない。
//
// Claude の判定は通さない（残す条件が要件で決まっているため）。
//
// 2つ目の戻り値は次回へ持ち越す末尾の区間。
func mediaPlays(device rawlog.Device, events []*rawlog.Event, opts Options, carried []rawlog.OpenStateInterval) ([]Proposal, []rawlog.OpenStateInterval, error) {
	var (
		proposals []Proposal
		intervals []timedInterval
	)

	for _, event := range filterType(events, rawlog.EventMediaPlay) {
		payload, err := rawlog.DecodePayload[rawlog.MediaPlayPayload](event)
		if err != nil {
			return nil, nil, err
		}

		// 判定は実再生時間で行う（要件 §8.1）。
		// 区間の長さでは代用できない。一時停止を挟むと壁時計は伸びるが、
		// 実際に再生したのは数秒ということがある。
		if payload.PlayedSeconds < MinPlayedSeconds {
			continue
		}

		if strings.TrimSpace(payload.URL) != "" {
			proposals = append(proposals, Proposal{
				ID:             makeID(KindURLog, SourceMedia, []string{event.EventID}),
				Kind:           KindURLog,
				Device:         device,
				Source:         SourceMedia,
				SourceEventIDs: []string{event.EventID},
				URL:            payload.URL,
				Title:          mediaTitle(payload),
				RelatedTime:    timePtr(event.StartTime),
			})
			continue
		}

		// URL が無い再生は TimeIs にする。
		// タイトルまで無いと何を再生したのか分からず、区間だけが残る。
		// それは app_usage の TimeIs と変わらないので作らない。
		title := mediaTitle(payload)
		if title == "" {
			continue
		}

		intervals = append(intervals, timedInterval{
			key:      title,
			title:    title,
			start:    event.StartTime,
			end:      mediaEndTime(event, payload),
			eventIDs: []string{event.EventID},
		})
	}

	merged, pending := mergeIntervals(device, SourceMedia, intervals, carried, WindowMergeWindow, opts.Cutoff)

	for _, interval := range merged {
		proposals = append(proposals, Proposal{
			ID:             makeID(KindTimeIs, SourceMedia, interval.eventIDs),
			Kind:           KindTimeIs,
			Device:         device,
			Source:         SourceMedia,
			SourceEventIDs: interval.eventIDs,
			Title:          interval.title,
			StartTime:      timePtr(interval.start),
			EndTime:        timePtr(interval.end),
		})
	}
	return proposals, pending, nil
}

// mediaEndTime は再生の終了時刻を返す。
//
// 収集側が区間として記録していればその終了時刻を使う。実際に画面を見ていた
// 時間帯そのものなので、一時停止を挟んでいても壁時計として正しい。
//
// 終了時刻が無い場合だけ、実再生秒数を足して埋める。この値は一時停止を
// 含まないため実際の終了より早くなるが、再生を始めた事実は残せる。
func mediaEndTime(event *rawlog.Event, payload rawlog.MediaPlayPayload) time.Time {
	if event.EndTime != nil {
		return *event.EndTime
	}
	return event.StartTime.Add(time.Duration(payload.PlayedSeconds * float64(time.Second)))
}

// mediaTitle は取得できた正式タイトルを返す。
// アーティストが取れていれば併記する。取れなかったものを補完はしない。
func mediaTitle(payload rawlog.MediaPlayPayload) string {
	switch {
	case payload.Title != "" && payload.Artist != "":
		return payload.Title + " - " + payload.Artist
	default:
		return payload.Title
	}
}

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
			ID:             makeID(KindTimeIs, SourceWindow, interval.eventIDs),
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
