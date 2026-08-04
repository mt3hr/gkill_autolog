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
