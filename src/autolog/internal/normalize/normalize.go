// Package normalize は生ログを Kyou の提案へ変換する。
//
// ここに入るのは決定的なルール処理だけで、LLM は通さない。
// セッション化・結合・閾値・集計をすべてコードで行い、
// 何を残すかは閾値と除外リストだけで決める。
// 同じ生ログからは、何度実行しても同じ提案が出る。
//
// 判定基準を1か所に集めるため、Chrome 拡張や収集プログラムの側では絞り込みをしない。
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// Kind は提案する Kyou の種類。
type Kind string

const (
	KindTimeIs Kind = "timeis"
	KindURLog  Kind = "urlog"
	KindKmemo  Kind = "kmemo"
)

// 収集元を表すタグ。
//
// Kyou に付くタグはこの1つだけ。端末名はタグではなく create_device に入る
// （書き分けは端末別ユーザーで行う。documents/reverse/design-philosophy.md 参照）。
const (
	SourceWindow       = "autolog_window"
	SourceDevice       = "autolog_device"
	SourceBrowser      = "autolog_browser"
	SourceMedia        = "autolog_media"
	SourceWifi         = "autolog_wifi"
	SourceBluetooth    = "autolog_bluetooth"
	SourceCharge       = "autolog_charge"
	SourceNotification = "autolog_notification"
)

// 要件で決まっている閾値と結合幅。
const (
	// IdleTimeout はこれだけ入力が無ければウィンドウのセッションを終える（要件 §6.3）。
	IdleTimeout = 5 * time.Minute
	// WindowMergeWindow はこの時間内に元のウィンドウへ戻ったら前後を結合する（要件 §6.3）。
	WindowMergeWindow = time.Minute
	// MinWindowDuration はこれ未満のウィンドウ操作を TimeIs にしない。
	//
	// 要件に規定は無いが、下限が無いと一瞬触っただけのウィンドウが大量に Kyou になる。
	// 閲覧 URLog (30秒) やアプリ利用 (30秒) と同じ考え方で下限を設ける。
	// 判定は割り込みの結合を済ませたあとに行う。
	// 短い区間どうしが結合されて下限を超えることがあるため。
	MinWindowDuration = time.Minute
	// MinViewDuration はこれ未満の連続閲覧を URLog にしない（要件 §7.1）。
	MinViewDuration = 30 * time.Second
	// MinPlayedSeconds はこれ未満しか再生されなかったコンテンツを URLog にしない（要件 §8.1）。
	MinPlayedSeconds = 30
	// MinAppUsage はこれ未満のアプリ利用を TimeIs にしない（要件 §11.2）。
	//
	// PC 側の MinWindowDuration と同じ値にする。
	// どちらも「一瞬触っただけの操作は残さない」という同じ判断なので、
	// 端末の種類で基準が変わる理由がない。
	// 判定は結合を済ませたあとの長さで行う。
	MinAppUsage = time.Minute
	// WifiMergeWindow はこの時間内の再接続を結合する（要件 §9.1）。
	WifiMergeWindow = 30 * time.Second
	// BluetoothMergeWindow はこの時間内の再接続を結合する（要件 §9.2）。
	BluetoothMergeWindow = time.Minute
	// ChargeMergeWindow はこの時間内の充電再開を結合する（要件 §9.3）。
	ChargeMergeWindow = 30 * time.Second
	// NotificationDedupeWindow はこの時間内の同内容の再通知を1件にまとめる（要件 §13）。
	NotificationDedupeWindow = 5 * time.Minute
	// NotificationUpdateWindow は同一通知キーの更新をひとつの通知とみなす間隔。
	//
	// 「同一通知IDの更新は最終状態だけを記録する」（要件 §13）ためのまとめだが、
	// Android は通知IDを使い回すので、無制限にまとめると処理する窓
	// （通常1日、初回は7日）の中の別々の通知が最終状態だけに潰れ、
	// 結果が取り込みの間隔に依存してしまう。
	// この間隔を超えて間が空いた更新は、同じキーでも別の通知として扱う。
	NotificationUpdateWindow = 30 * time.Minute
)

// Proposal は gkill へ書き込む1件の提案。
type Proposal struct {
	// ID は元になった生ログから決まる決定的な識別子。
	// 同じ生ログから何度 normalize しても同じ値になるため、台帳での重複判定に使える。
	ID string `json:"id"`

	Kind   Kind          `json:"kind"`
	Device rawlog.Device `json:"device"`
	// Source は収集元。書き込み時にタグとして付ける。
	Source string `json:"source"`
	// SourceEventIDs は元になった生ログのイベントID。
	SourceEventIDs []string `json:"source_event_ids"`

	// TimeIs 用。
	Title     string     `json:"title,omitempty"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`

	// URLog 用。
	URL string `json:"url,omitempty"`

	// Kmemo 用。
	Content string `json:"content,omitempty"`

	// URLog と Kmemo の RelatedTime。
	RelatedTime *time.Time `json:"related_time,omitempty"`
}

// URLCandidate は URLog にした閲覧区間の一覧。
//
// 取捨は browserViews の中で除外リストと閾値によって済んでいるので、
// 取り込みの経路では使っていない。何がどれだけ URLog になったかを
// 外から確かめたいときのために残してある。
type URLCandidate struct {
	ProposalID      string `json:"proposal_id"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	RelatedTime     string `json:"related_time"`
	DurationSeconds int    `json:"duration_seconds"`
}

// Options は normalize の実行条件。
type Options struct {
	// Cutoff はここまでの生ログを処理する。通常は当日の午前4時。
	// これ以降のイベントは次回に回す。
	Cutoff time.Time
	// DenyList は URLog にしない URL のパターン。nil なら除外しない。
	DenyList *DenyList
	// NotificationDenyList は Kmemo にしない通知のパターン。nil なら除外しない。
	// パッケージ名・アプリ名・チャンネルIDを照合する。
	NotificationDenyList *DenyList
	// OpenStates は前回までに持ち越された区間。
	// 接続区間なら、開始イベントが今回の窓の外にあっても閉じられるようになる。
	// アプリ利用やメディア再生の末尾なら、今回のバッチの先頭と結合できるようになる。
	OpenStates []rawlog.OpenStateInterval
	// NotificationSeen は前回までに記録した通知の内容と時刻。
	// これを渡すと、窓の切れ目をまたいでも同内容の再通知をまとめられる。
	NotificationSeen []rawlog.NotificationSeen
	// UsageTitle は端末利用 TimeIs のタイトル。空なら DefaultUsageTitle。
	//
	// 端末ごとに呼び分けたい語（「Windows利用」など）は環境によって違うので、
	// コードに埋め込まず設定で決める。
	UsageTitle string
}

// usageTitle は端末利用 TimeIs のタイトルを返す。
func (o Options) usageTitle() string {
	if o.UsageTitle == "" {
		return DefaultUsageTitle
	}
	return o.UsageTitle
}

// Result は normalize の出力。
type Result struct {
	// Proposals は書き込み内容が確定した提案。
	Proposals []Proposal
	// URLCandidates は URLog にした閲覧区間の一覧。
	// 対応する Proposal は Proposals にも入っているので、取り込みではこちらを使わない。
	URLCandidates []URLCandidate
	// OpenStates は Cutoff 時点でまだ確定していない区間。
	// 呼び出し側が保存し、次回の Options.OpenStates として渡す。
	OpenStates []rawlog.OpenStateInterval
	// NotificationSeen は Cutoff 時点で覚えておくべき通知の内容と時刻。
	// 呼び出し側が保存し、次回の Options.NotificationSeen として渡す。
	NotificationSeen []rawlog.NotificationSeen
	// SafeCursor は次回の開始位置。
	// 継続中のセッションの開始時刻より先へは進めないため、Cutoff より前になることがある。
	SafeCursor time.Time
}

// Run は生ログを提案へ変換する。events は start_time 昇順で渡すこと。
func Run(events []*rawlog.Event, opts Options) (*Result, error) {
	result := &Result{SafeCursor: opts.Cutoff}

	// 端末ごとに独立して処理する。
	// Windows と Android の履歴は統合しない（要件 §8.2）。
	byDevice := map[rawlog.Device][]*rawlog.Event{}
	for _, e := range events {
		if !e.StartTime.Before(opts.Cutoff) {
			continue
		}
		byDevice[e.Device] = append(byDevice[e.Device], e)
	}

	// 持ち越しがある端末は、今回イベントが1件も無くても処理する。
	// 処理しないと、確定させてよくなった区間がいつまでも持ち越されたままになり、
	// その端末が次に何かするまで gkill へ出てこない。
	for _, open := range opts.OpenStates {
		if _, ok := byDevice[open.Device]; !ok {
			byDevice[open.Device] = nil
		}
	}
	for _, seen := range opts.NotificationSeen {
		if _, ok := byDevice[seen.Device]; !ok {
			byDevice[seen.Device] = nil
		}
	}

	devices := make([]rawlog.Device, 0, len(byDevice))
	for device := range byDevice {
		devices = append(devices, device)
	}
	slices.Sort(devices)

	for _, device := range devices {
		deviceEvents := byDevice[device]

		windowProposals, windowCursor, err := windowSessions(device, deviceEvents, opts)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, windowProposals...)
		result.limitCursor(windowCursor)

		usageProposals, usageCursor, err := usageSessions(device, deviceEvents, opts)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, usageProposals...)
		result.limitCursor(usageCursor)

		// 再生とアプリ利用の末尾は、次のバッチの先頭と結合されるかもしれない。
		// カーソルは引き戻さず、末尾の区間だけを Result で持ち越して次回にまとめる。
		//
		// 閲覧区間より先に処理する。同じURLを再生として URLog にしたかどうかを
		// browserViews へ渡し、閲覧側の URLog を落とすため。
		mediaProposals, mediaURLs, mediaPending, err := mediaPlays(device, deviceEvents, opts, opts.OpenStates)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, mediaProposals...)
		result.OpenStates = append(result.OpenStates, mediaPending...)

		browserProposals, candidates, err := browserViews(device, deviceEvents, opts.DenyList, mediaURLs)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, browserProposals...)
		result.URLCandidates = append(result.URLCandidates, candidates...)

		appProposals, appPending, err := appUsages(device, deviceEvents, opts, opts.OpenStates)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, appProposals...)
		result.OpenStates = append(result.OpenStates, appPending...)

		// 接続区間はカーソルを引き戻さない。常時つないだままの機器
		// (スマートウォッチや自宅の Wi-Fi) があると永久に進まなくなるため、
		// 開いている区間は Result で持ち越して次回に閉じる。
		stateProposals, stateOpens, err := connectionStates(device, deviceEvents, opts, opts.OpenStates)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, stateProposals...)
		result.OpenStates = append(result.OpenStates, stateOpens...)

		notificationProposals, notificationSeen, err := notifications(device, deviceEvents, opts, opts.NotificationSeen)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, notificationProposals...)
		result.NotificationSeen = append(result.NotificationSeen, notificationSeen...)
	}

	slices.SortFunc(result.Proposals, func(a, b Proposal) int {
		at, bt := a.sortTime(), b.sortTime()
		if !at.Equal(bt) {
			return at.Compare(bt)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}

// limitCursor は継続中セッションの開始時刻までカーソルを引き戻す。
func (r *Result) limitCursor(cursor time.Time) {
	if !cursor.IsZero() && cursor.Before(r.SafeCursor) {
		r.SafeCursor = cursor
	}
}

// sortTime は並べ替えに使う時刻を返す。
func (p Proposal) sortTime() time.Time {
	switch {
	case p.StartTime != nil:
		return *p.StartTime
	case p.RelatedTime != nil:
		return *p.RelatedTime
	default:
		return time.Time{}
	}
}

// makeID は元になった生ログのイベントIDから決定的な識別子を作る。
//
// 同じ生ログを再度 normalize しても同じ値になるので、
// バッチが途中で失敗して再実行しても二重登録にならない。
//
// 同じイベントIDが2回入っていても1回として扱う。
// カーソルの引き戻しで同じイベントを読み直すことがあり、
// 重複を数えていると読み直しの有無で識別子が変わってしまう。
func makeID(kind Kind, source string, eventIDs []string) string {
	ids := slices.Clone(eventIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)

	hash := sha256.New()
	hash.Write([]byte(string(kind)))
	hash.Write([]byte{0})
	hash.Write([]byte(source))
	for _, id := range ids {
		hash.Write([]byte{0})
		hash.Write([]byte(id))
	}
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

// filterType は指定した種別のイベントだけを返す。
func filterType(events []*rawlog.Event, eventType rawlog.EventType) []*rawlog.Event {
	var filtered []*rawlog.Event
	for _, e := range events {
		if e.EventType == eventType {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func timePtr(t time.Time) *time.Time { return &t }
