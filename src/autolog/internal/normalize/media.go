package normalize

import (
	"strings"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

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
// TimeIs は同じ再生 (mediaMergeKey。動画ID > URL > タイトルの優先順で同一性を
// 判定する) が WindowMergeWindow 以内に続いていれば1本にまとめる。
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
