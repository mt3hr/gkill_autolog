// Package normalize は生ログを Kyou の提案へ変換する。
//
// ここに入るのは決定的なルール処理だけで、Claude は通さない。
// セッション化・結合・閾値・集計をすべてコードで行い、
// Claude には「この URL を URLog として残すか」の2値判定だけを任せる。
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
// gkill Write MCP は書き込み先 Repository を指定できないため、
// 端末と収集元の識別はタグで行う（docs/design-decisions.md §1）。
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
	MinAppUsage = 30 * time.Second
	// WifiMergeWindow はこの時間内の再接続を結合する（要件 §9.1）。
	WifiMergeWindow = 30 * time.Second
	// BluetoothMergeWindow はこの時間内の再接続を結合する（要件 §9.2）。
	BluetoothMergeWindow = time.Minute
	// ChargeMergeWindow はこの時間内の充電再開を結合する（要件 §9.3）。
	ChargeMergeWindow = 30 * time.Second
	// NotificationDedupeWindow はこの時間内の同内容の再通知を1件にまとめる（要件 §13）。
	NotificationDedupeWindow = 5 * time.Minute
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

// URLCandidate は Claude に残すかどうかを判定してもらう URLog 候補。
//
// Claude はページの意味を要約せず、URLog として残す必要があるかだけを判定する。
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
	// OpenStates は前回までに開いたまま持ち越された接続区間。
	// これを渡すと、開始イベントが今回の窓の外にあっても区間を閉じられる。
	OpenStates []rawlog.OpenStateInterval
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
	// URLCandidates は Claude の判定を待つ URLog 候補。
	// 対応する Proposal は Proposals にも入っており、判定で捨てられたものだけ書き込まない。
	URLCandidates []URLCandidate
	// OpenStates は Cutoff 時点でまだ閉じていない接続区間。
	// 呼び出し側が保存し、次回の Options.OpenStates として渡す。
	OpenStates []rawlog.OpenStateInterval
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

		browserProposals, candidates, err := browserViews(device, deviceEvents, opts.DenyList)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, browserProposals...)
		result.URLCandidates = append(result.URLCandidates, candidates...)

		mediaProposals, err := mediaPlays(device, deviceEvents)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, mediaProposals...)

		appProposals, err := appUsages(device, deviceEvents)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, appProposals...)

		// 接続区間はカーソルを引き戻さない。常時つないだままの機器
		// (スマートウォッチや自宅の Wi-Fi) があると永久に進まなくなるため、
		// 開いている区間は Result で持ち越して次回に閉じる。
		stateProposals, stateOpens, err := connectionStates(device, deviceEvents, opts.OpenStates)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, stateProposals...)
		result.OpenStates = append(result.OpenStates, stateOpens...)

		notificationProposals, err := notifications(device, deviceEvents)
		if err != nil {
			return nil, err
		}
		result.Proposals = append(result.Proposals, notificationProposals...)
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
func makeID(kind Kind, source string, eventIDs []string) string {
	ids := slices.Clone(eventIDs)
	slices.Sort(ids)

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
