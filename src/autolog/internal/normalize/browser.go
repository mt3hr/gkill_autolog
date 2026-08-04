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
// YouTube 以外のサイトはホストで落とさず、同じURLの再生があったときだけ
// browserViews が URLog を譲る（mediaURLs）。YouTube の閲覧URLは再生URLと
// 完全一致しない（&list= などが付く）ため、こちらはホスト単位で外す必要がある。
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
//
// mediaURLs は mediaPlays が同じ端末で URLog にした URL。
// 動画サイトでは同じページが閲覧区間としても届くため、そのままだと同じURLの
// URLog が2件になる。再生側を残し、閲覧側は落とす。
func browserViews(device rawlog.Device, events []*rawlog.Event, denyList *DenyList, mediaURLs map[string]struct{}) ([]Proposal, []URLCandidate, error) {
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
		// 同じURLの再生を media_play 側で URLog にしている。二重に登録しない。
		if _, played := mediaURLs[payload.URL]; played {
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

		id := makeID(KindURLog, SourceBrowser, device, []string{event.EventID})
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
// 再生していた区間は常に TimeIs にする。そのうえで URL を確定できた再生には
// URLog も作る。区間と「何を再生したか」は別の事実なので、両方残す。
//   - URL を確定できるのは Chrome 拡張の再生と、動画IDを確認できた Android の再生
//   - Android の MediaSession はタイトルしか返さないことが多く、その場合は TimeIs だけ
//
// どちらも実再生時間が MinPlayedSeconds 以上のものだけを残す（要件 §8.1）。
// URLog は同じ動画や楽曲でも再生の都度1件作り、RelatedTime は再生開始時刻にする。
//
// TimeIs は同じタイトルの再生が WindowMergeWindow 以内に続いていれば1本にまとめる。
// MediaSession は曲を止めずに聴いていても区間を細かく切って返すため、
// まとめないと同じ曲の TimeIs が並ぶ。まとめた結果、TimeIs 1本に対して
// URLog が複数並ぶことがある。
//
// 検索URLや推測したURLは作らない（要件 §8.2）。TimeIs にはタイトル・アーティストという
// 観測できた事実だけを載せ、URL の代わりを埋め合わせることはしない。
//
// 残す条件は要件で決まっているので、判定を外へ出さない。
//
// 2つ目の戻り値は URLog にした URL。browserViews が同じURLの閲覧区間を落とすのに使う。
// 3つ目の戻り値は次回へ持ち越す末尾の区間。
func mediaPlays(device rawlog.Device, events []*rawlog.Event, opts Options, carried []rawlog.OpenStateInterval) ([]Proposal, map[string]struct{}, []rawlog.OpenStateInterval, error) {
	var (
		proposals []Proposal
		intervals []timedInterval
	)
	urlogURLs := map[string]struct{}{}

	for _, event := range filterType(events, rawlog.EventMediaPlay) {
		payload, err := rawlog.DecodePayload[rawlog.MediaPlayPayload](event)
		if err != nil {
			return nil, nil, nil, err
		}

		// 判定は実再生時間で行う（要件 §8.1）。
		// 区間の長さでは代用できない。一時停止を挟むと壁時計は伸びるが、
		// 実際に再生したのは数秒ということがある。
		if payload.PlayedSeconds < MinPlayedSeconds {
			continue
		}

		// URL を確定できた再生は URLog にする。
		// 利用者が列挙した除外パターンには従う。対象が全サイトに広がったため、
		// 落としたいものを url_denylist.txt で抑えられる必要がある。
		if url := strings.TrimSpace(payload.URL); url != "" && !opts.DenyList.Matches(url) {
			proposals = append(proposals, Proposal{
				ID:             makeID(KindURLog, SourceMedia, device, []string{event.EventID}),
				Kind:           KindURLog,
				Device:         device,
				Source:         SourceMedia,
				SourceEventIDs: []string{event.EventID},
				URL:            url,
				Title:          mediaTitle(payload),
				RelatedTime:    timePtr(event.StartTime),
			})
			urlogURLs[url] = struct{}{}
		}

		// 再生していた区間は URL の有無によらず TimeIs にする。
		// タイトルが無いと何を再生したのか分からず、区間だけが残る。
		// それは app_usage の TimeIs と変わらないので作らない。
		title := mediaTitle(payload)
		if title == "" {
			continue
		}

		intervals = append(intervals, timedInterval{
			key:      mediaMergeKey(payload, title),
			title:    title,
			start:    event.StartTime,
			end:      mediaEndTime(event, payload),
			eventIDs: []string{event.EventID},
		})
	}

	merged, pending := mergeIntervals(device, SourceMedia, intervals, carried, WindowMergeWindow, opts.Cutoff)

	for _, interval := range merged {
		proposals = append(proposals, Proposal{
			ID:             makeID(KindTimeIs, SourceMedia, device, interval.eventIDs),
			Kind:           KindTimeIs,
			Device:         device,
			Source:         SourceMedia,
			SourceEventIDs: interval.eventIDs,
			Title:          interval.title,
			StartTime:      timePtr(interval.start),
			EndTime:        timePtr(interval.end),
		})
	}
	return proposals, urlogURLs, pending, nil
}

// mediaMergeKey は「同じ再生の続きか」を判定する鍵を返す。
//
// タイトルでは判定できない。自動再生で次の動画・次の曲へ進む間隔は数秒しか
// 空かないため、題名がたまたま同じだと別のコンテンツどうしが1本にまとまる。
// 連続再生したものは1本ずつ残す必要がある。
//
// 動画IDかURLを確認できていればそれを使う。どちらも無いのは Android の
// MediaSession のようにタイトルしか返さない収集元で、そこは従来どおり
// タイトルで結合する。1曲の再生が細切れに届くのを束ねるためにこの結合がある。
func mediaMergeKey(payload rawlog.MediaPlayPayload, title string) string {
	if payload.VideoID != "" {
		return "video_id:" + payload.VideoID
	}
	if url := strings.TrimSpace(payload.URL); url != "" {
		return "url:" + url
	}
	return "title:" + title
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
			ID:             makeID(KindTimeIs, SourceWindow, device, interval.eventIDs),
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
