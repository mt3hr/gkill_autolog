package normalize

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// runNormalizeSliced は同じ生ログを何回かに分けて処理し、出た提案をまとめて返す。
//
// 実際の取り込み（cmd_import）と同じ手順を踏む。
//   - 読む範囲は「前回の SafeCursor 〜 今回の Cutoff」
//   - 持ち越しは Result から次回の Options へ渡す
//   - 台帳と同じ二重の重複排除を行う。提案IDの一致に加え、
//     元イベントがすべて書き込み済みの提案（確定済み区間の断片）も落とす
//     （ledger.IsCovered 相当）。カーソルの引き戻しで同じイベントを
//     読み直すことがあり、それ自体は正しい動きだからである。
func runNormalizeSliced(t *testing.T, events []*rawlog.Event, opts Options, cutoffs []time.Duration) []Proposal {
	t.Helper()

	var (
		from          time.Time
		carriedOpen   []rawlog.OpenStateInterval
		carriedSeen   []rawlog.NotificationSeen
		written       = map[string]Proposal{}
		writtenEvents = map[string]struct{}{}
	)
	coverageKey := func(p Proposal, eventID string) string {
		return string(p.Kind) + "\x00" + p.Source + "\x00" + string(p.Device) + "\x00" + eventID
	}
	isCovered := func(p Proposal) bool {
		if len(p.SourceEventIDs) == 0 {
			return false
		}
		for _, id := range p.SourceEventIDs {
			if _, ok := writtenEvents[coverageKey(p, id)]; !ok {
				return false
			}
		}
		return true
	}

	for _, cutoff := range cutoffs {
		sliceOpts := opts
		sliceOpts.Cutoff = base().Add(cutoff)
		sliceOpts.OpenStates = carriedOpen
		sliceOpts.NotificationSeen = carriedSeen

		var slice []*rawlog.Event
		for _, e := range events {
			if !from.IsZero() && e.StartTime.Before(from) {
				continue
			}
			slice = append(slice, e)
		}

		result, err := Run(slice, sliceOpts)
		if err != nil {
			t.Fatalf("Run(cutoff=%v): %v", cutoff, err)
		}
		for _, proposal := range result.Proposals {
			if _, ok := written[proposal.ID]; ok {
				continue
			}
			if isCovered(proposal) {
				continue
			}
			written[proposal.ID] = proposal
			for _, id := range proposal.SourceEventIDs {
				writtenEvents[coverageKey(proposal, id)] = struct{}{}
			}
		}

		carriedOpen = result.OpenStates
		carriedSeen = result.NotificationSeen
		from = result.SafeCursor
	}

	merged := make([]Proposal, 0, len(written))
	for _, proposal := range written {
		merged = append(merged, proposal)
	}
	sortProposals(merged)
	return merged
}

// sortProposals は比較できるよう提案を並べる。
func sortProposals(proposals []Proposal) {
	slices.SortFunc(proposals, func(a, b Proposal) int {
		at, bt := a.sortTime(), b.sortTime()
		if !at.Equal(bt) {
			return at.Compare(bt)
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// describeProposals は突き合わせの失敗時に読める形へ直す。
func describeProposals(proposals []Proposal) []string {
	lines := make([]string, 0, len(proposals))
	for _, p := range proposals {
		line := string(p.Kind) + " " + p.Source + " " + p.Title + p.URL + p.Content
		if p.StartTime != nil {
			line += " start=" + p.StartTime.Sub(base()).String()
		}
		if p.EndTime != nil {
			line += " end=" + p.EndTime.Sub(base()).String()
		}
		if p.RelatedTime != nil {
			line += " at=" + p.RelatedTime.Sub(base()).String()
		}
		lines = append(lines, line)
	}
	return lines
}

// assertSameProposals は2つの提案の並びが一致することを確かめる。
func assertSameProposals(t *testing.T, got []Proposal, want []Proposal) {
	t.Helper()

	gotLines, wantLines := describeProposals(got), describeProposals(want)
	if slices.Equal(gotLines, wantLines) {
		return
	}
	t.Errorf("提案が一致しない\n刻んで処理 (%d件):\n  %s\n1回で処理 (%d件):\n  %s",
		len(gotLines), strings.Join(gotLines, "\n  "),
		len(wantLines), strings.Join(wantLines, "\n  "))
}

// splitConsistencyEvents は窓の切れ目をまたぐ生ログを作る。
//
// どれも「切れ目で分けて処理すると結果が変わる」経路を通る。
func splitConsistencyEvents(t *testing.T) []*rawlog.Event {
	t.Helper()

	return []*rawlog.Event{
		// アプリ利用: 30秒空いて同じアプリへ戻る。結合すると1分を超える。
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(40*sec),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 70*sec, durationPtr(110*sec),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),

		// メディア再生: 同じ曲が10秒空いて続く。
		mediaEventNoURL(t, "m1", 0, 40*sec, 40, "曲名", "アーティスト"),
		mediaEventNoURL(t, "m2", 50*sec, 90*sec, 40, "曲名", "アーティスト"),

		// メディア再生 (URLあり): URLog は再生の都度、TimeIs は結合して1本。
		mediaEvent(t, "m3", 0, 40, "https://www.youtube.com/watch?v=abc"),
		mediaEvent(t, "m4", 50*sec, 40, "https://www.youtube.com/watch?v=abc"),

		// 通知: 同じ内容が2分後に再通知される。重複排除の窓は5分。
		androidEvent(t, "n1", rawlog.EventNotification, 10*sec, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),
		androidEvent(t, "n2", rawlog.EventNotification, 130*sec, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),

		// Bluetooth: 接続したまま切れ目をまたぐ。
		androidEvent(t, "b1", rawlog.EventBluetooth, 5*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: true}),
		androidEvent(t, "b2", rawlog.EventBluetooth, 200*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: false}),
	}
}

func TestSplitConsistency(t *testing.T) {
	// 取り込みを何回に分けて走らせても、同じ生ログからは同じ提案が出ること。
	// 実行間隔は運用で変わる（手で叩くこともある）ので、
	// 結果が間隔に依存すると「同じ入力からは同じ結果」という約束が守れない。
	events := splitConsistencyEvents(t)
	final := 10 * min

	tests := []struct {
		name    string
		cutoffs []time.Duration
	}{
		{name: "1分ごと", cutoffs: []time.Duration{min, 2 * min, 3 * min, 4 * min, 5 * min, final}},
		{name: "2分ごと", cutoffs: []time.Duration{2 * min, 4 * min, 6 * min, 8 * min, final}},
		{
			name: "区間の途中で切る",
			// 45秒・75秒はアプリ利用と再生の区間の内側にあたる。
			cutoffs: []time.Duration{45 * sec, 75 * sec, 100 * sec, 135 * sec, 4 * min, final},
		},
	}

	want := runNormalize(t, events, final).Proposals
	if len(want) == 0 {
		t.Fatal("1回で処理したときの提案が0件。テストの前提が壊れている")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runNormalizeSliced(t, events, Options{}, tt.cutoffs)
			assertSameProposals(t, got, want)
		})
	}
}

func TestCursorRegressionDoesNotDuplicateSettledInterval(t *testing.T) {
	// 閉じないセッションがカーソルを引き戻すと、確定済みのアプリ利用区間の
	// イベント列が途中から読み直される。断片の event_id 集合から別 id の
	// 提案が出ても、台帳相当の重複排除（イベント包含）で落ちること。
	events := []*rawlog.Event{
		// 30秒の途切れを挟んで1本に結合され、cutoff=10分で確定する。
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(40*sec),
			rawlog.AppUsagePayload{AppLabel: "Chrome"}),
		// 画面ON。閉じないのでカーソルはここで止まり続ける。
		androidEvent(t, "s1", rawlog.EventSession, 50*sec, nil,
			rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 70*sec, durationPtr(180*sec),
			rawlog.AppUsagePayload{AppLabel: "Chrome"}),
	}

	got := runNormalizeSliced(t, events, Options{}, []time.Duration{10 * min, 20 * min})
	want := runNormalize(t, events, 20*min).Proposals
	assertSameProposals(t, got, want)
}

func TestWindowInterruptionMergesAcrossBatches(t *testing.T) {
	// 割り込みの最中に cutoff が来ても、直後に元のアプリへ戻れば
	// 1本の TimeIs に結合されること。結合幅の内側で終わった区間を
	// 確定させてしまうと、取り込む間隔しだいで1本にも2本にもなる。
	events := []*rawlog.Event{
		event(t, "i1", rawlog.EventInput, 0, nil, rawlog.InputPayload{AppName: "Code", WindowTitle: "A"}),
		event(t, "i2", rawlog.EventInput, 70*sec, nil, rawlog.InputPayload{AppName: "Code", WindowTitle: "A"}),
		event(t, "i3", rawlog.EventInput, 80*sec, nil, rawlog.InputPayload{AppName: "chrome", WindowTitle: "B"}),
		event(t, "i4", rawlog.EventInput, 100*sec, nil, rawlog.InputPayload{AppName: "Code", WindowTitle: "A"}),
		event(t, "i5", rawlog.EventInput, 170*sec, nil, rawlog.InputPayload{AppName: "Code", WindowTitle: "A"}),
	}

	got := runNormalizeSliced(t, events, Options{}, []time.Duration{90 * sec, 20 * min})
	want := runNormalize(t, events, 20*min).Proposals
	assertSameProposals(t, got, want)
}

func TestAppUsageMergesAcrossBatches(t *testing.T) {
	// 窓の切れ目でアプリ利用が細切れにならないこと。
	// 40秒と40秒が結合して1分を超え、1本の TimeIs になる。
	events := []*rawlog.Event{
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(40*sec),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 70*sec, durationPtr(110*sec),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
	}

	got := runNormalizeSliced(t, events, Options{}, []time.Duration{min, 10 * min})
	if len(got) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(got), describeProposals(got))
	}
	if start := got[0].StartTime.Sub(base()); start != 0 {
		t.Errorf("開始 = %v, want 0", start)
	}
	if end := got[0].EndTime.Sub(base()); end != 110*sec {
		t.Errorf("終了 = %v, want %v", end, 110*sec)
	}
}

func TestAppUsageMinimumAppliesAfterMerge(t *testing.T) {
	// 下限の判定は結合を済ませたあとの長さで行う。
	// 単体では下限に満たない断片でも、続いていれば1本の利用として残す。
	events := []*rawlog.Event{
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(35*sec),
			rawlog.AppUsagePayload{AppLabel: "Termux", PackageName: "com.termux"}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 50*sec, durationPtr(85*sec),
			rawlog.AppUsagePayload{AppLabel: "Termux", PackageName: "com.termux"}),
	}

	apps := proposalsBySource(runNormalize(t, events, 10*min), SourceWindow)
	if len(apps) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(apps), describeProposals(apps))
	}
	if end := apps[0].EndTime.Sub(base()); end != 85*sec {
		t.Errorf("終了 = %v, want %v", end, 85*sec)
	}
}

func TestAppUsageDoesNotMergeBeyondWindow(t *testing.T) {
	// 結合幅を超えて空いていれば別の利用として残す。
	events := []*rawlog.Event{
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(2*min),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 5*min, durationPtr(7*min),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
	}

	apps := proposalsBySource(runNormalize(t, events, 60*min), SourceWindow)
	if len(apps) != 2 {
		t.Fatalf("件数 = %d, want 2 (%v)", len(apps), describeProposals(apps))
	}
}

func TestPendingIntervalIsSettledOnceMergeWindowPassed(t *testing.T) {
	// 結合幅を過ぎて終わった区間は持ち越さず確定させる。
	// 持ち越し続けると、いつまで経っても gkill に出てこない。
	events := []*rawlog.Event{
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(2*min),
			rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
	}

	// Cutoff が終了の30秒後。まだ結合相手が来るかもしれないので持ち越す。
	pendingResult := runNormalize(t, events, 2*min+30*sec)
	if apps := proposalsBySource(pendingResult, SourceWindow); len(apps) != 0 {
		t.Errorf("持ち越すべき区間が確定している: %v", describeProposals(apps))
	}
	if len(pendingResult.OpenStates) != 1 {
		t.Fatalf("持ち越し = %d 件, want 1", len(pendingResult.OpenStates))
	}
	if pendingResult.OpenStates[0].End == nil {
		t.Error("持ち越しに終了時刻が入っていない")
	}

	// Cutoff が終了の2分後。もう結合相手は現れないので確定させる。
	settledResult := runNormalize(t, events, 4*min)
	if apps := proposalsBySource(settledResult, SourceWindow); len(apps) != 1 {
		t.Errorf("件数 = %d, want 1", len(apps))
	}
	if len(settledResult.OpenStates) != 0 {
		t.Errorf("持ち越し = %d 件, want 0", len(settledResult.OpenStates))
	}
}

func TestNotificationDedupeAcrossBatches(t *testing.T) {
	// 同内容の再通知は、窓の切れ目をまたいでも1件にまとめる。
	events := []*rawlog.Event{
		androidEvent(t, "n1", rawlog.EventNotification, 0, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),
		androidEvent(t, "n2", rawlog.EventNotification, 2*min, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),
	}

	got := runNormalizeSliced(t, events, Options{}, []time.Duration{min, 3 * min})
	if len(got) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(got), describeProposals(got))
	}
}

func TestNotificationRecordsAgainAfterDedupeWindow(t *testing.T) {
	// 重複排除の窓を過ぎた再通知は改めて記録する。
	// 捨てたものを起点にすると、鳴り続ける通知が永久に残らなくなる。
	events := []*rawlog.Event{
		androidEvent(t, "n1", rawlog.EventNotification, 0, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),
		androidEvent(t, "n2", rawlog.EventNotification, 6*min, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),
	}

	got := runNormalizeSliced(t, events, Options{}, []time.Duration{min, 10 * min})
	if len(got) != 2 {
		t.Fatalf("件数 = %d, want 2 (%v)", len(got), describeProposals(got))
	}
}

func TestNotificationDenyList(t *testing.T) {
	// 除外リストはパッケージ名・アプリ名・チャンネルIDを見る。
	list, err := ParseDenyList(strings.NewReader("com.termux.api\nTasker\nre:^com\\.example\\.\nre:(?i)^downloads$\n"))
	if err != nil {
		t.Fatalf("ParseDenyList: %v", err)
	}

	tests := []struct {
		name        string
		packageName string
		appLabel    string
		channelID   string
		want        int
	}{
		{name: "パッケージ名で除外", packageName: "com.termux.api", appLabel: "Termux:API", want: 0},
		{name: "アプリ名で除外", packageName: "net.dinglisch.android.taskerm", appLabel: "Tasker", want: 0},
		{name: "正規表現で除外", packageName: "com.example.app", appLabel: "サンプル", want: 0},
		{name: "チャンネルIDで除外", packageName: "com.android.chrome", appLabel: "Chrome", channelID: "downloads", want: 0},
		{name: "同じアプリの別チャンネルは残す", packageName: "com.android.chrome", appLabel: "Chrome", channelID: "sites", want: 1},
		{name: "チャンネルIDが空でも落とさない", packageName: "com.google.android.gm", appLabel: "Gmail", want: 1},
		{name: "当たらなければ残す", packageName: "com.google.android.gm", appLabel: "Gmail", channelID: "mail", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []*rawlog.Event{
				androidEvent(t, "n1", rawlog.EventNotification, 0, nil, rawlog.NotificationPayload{
					AppLabel: tt.appLabel, PackageName: tt.packageName, ChannelID: tt.channelID,
					Title: "件名", Body: "本文",
				}),
			}

			result := runNormalizeWith(t, events, Options{
				Cutoff:               base().Add(10 * min),
				NotificationDenyList: list,
			})
			if got := len(proposalsBySource(result, SourceNotification)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNotificationDenyListIgnoresEmptyChannelID(t *testing.T) {
	// チャンネルIDができる前に集めた生ログでは空になる。
	// 空文字に当たる正規表現を書かれても、過去の分がまとめて消えてはいけない。
	// ^$ は空文字にだけ当たる。パッケージ名もアプリ名も空ではないので、
	// チャンネルIDを照合していなければ通知は残る。
	list, err := ParseDenyList(strings.NewReader("re:^$\n"))
	if err != nil {
		t.Fatalf("ParseDenyList: %v", err)
	}

	events := []*rawlog.Event{
		androidEvent(t, "n1", rawlog.EventNotification, 0, nil, rawlog.NotificationPayload{
			AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文",
		}),
	}

	result := runNormalizeWith(t, events, Options{
		Cutoff:               base().Add(10 * min),
		NotificationDenyList: list,
	})
	if got := len(proposalsBySource(result, SourceNotification)); got != 1 {
		t.Errorf("件数 = %d, want 1", got)
	}
}

func TestNotificationDenyListEmptyKeepsAll(t *testing.T) {
	// 除外リストが無いときは何も落とさない。
	events := []*rawlog.Event{
		androidEvent(t, "n1", rawlog.EventNotification, 0, nil, rawlog.NotificationPayload{
			AppLabel: "Termux:API", PackageName: "com.termux.api", Title: "件名", Body: "本文",
		}),
	}

	result := runNormalize(t, events, 10*min)
	if got := len(proposalsBySource(result, SourceNotification)); got != 1 {
		t.Errorf("件数 = %d, want 1", got)
	}
}

func TestDefaultNotificationDenyListExcludesAutomation(t *testing.T) {
	// 既定の除外リストで自動化ツールの通知が落ちること。
	list, err := ParseDenyList(strings.NewReader(DefaultNotificationDenyList))
	if err != nil {
		t.Fatalf("ParseDenyList: %v", err)
	}

	for _, packageName := range []string{"com.termux.api", "net.dinglisch.android.taskerm"} {
		if !list.Matches(packageName) {
			t.Errorf("%s が既定の除外リストに当たらない", packageName)
		}
	}
	// コメントアウトしてある行は効かない。
	if list.Matches("com.google.android.gms") {
		t.Error("コメント行が有効になっている")
	}
}

func TestDefaultNotificationDenyListExcludesDownloads(t *testing.T) {
	// 既定の除外リストでダウンロードの通知が落ちること。
	list, err := ParseDenyList(strings.NewReader(DefaultNotificationDenyList))
	if err != nil {
		t.Fatalf("ParseDenyList: %v", err)
	}

	// ダウンロード専用のアプリはパッケージ名で落ちる。UI 側のパッケージも巻き取る。
	for _, value := range []string{
		"com.android.providers.downloads",
		"com.android.providers.downloads.ui",
	} {
		if !list.Matches(value) {
			t.Errorf("%s が既定の除外リストに当たらない", value)
		}
	}
	// 他の通知も出すアプリはチャンネルIDで落とす。
	// Chrome は進行中が downloads、完了が completed_downloads。
	// 実際に届くのは完了の方なので、こちらが落ちないと意味がない。
	for _, channelID := range []string{
		"downloads",
		"Downloads",
		"completed_downloads",
		"Completed_Downloads",
	} {
		if !list.Matches(channelID) {
			t.Errorf("チャンネルID %s が既定の除外リストに当たらない", channelID)
		}
	}
	// Chrome そのものは落とさない。ダウンロード以外の通知は残す。
	// browser / media は Chrome の他のチャンネルID。
	for _, value := range []string{
		"com.android.chrome",
		"Chrome",
		"sites",
		"browser",
		"media",
	} {
		if list.Matches(value) {
			t.Errorf("%s まで既定の除外リストに当たっている", value)
		}
	}
	// コメントアウトしてある行は効かない。
	for _, value := range []string{
		"com.google.android.documentsui",
		"SBROWSER_DOWNLOADS_NOTIFICATION_CHANNEL",
	} {
		if list.Matches(value) {
			t.Errorf("コメント行 %s が有効になっている", value)
		}
	}
}

func TestBluetoothLEAndClassicBecomeOneInterval(t *testing.T) {
	// 1台のヘッドホンがクラシックと LE の両方でつながっても TimeIs は1本。
	events := []*rawlog.Event{
		androidEvent(t, "b1", rawlog.EventBluetooth, 0, nil,
			rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: true}),
		androidEvent(t, "b2", rawlog.EventBluetooth, 2*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "LE_Headphones", Connected: true}),
		androidEvent(t, "b3", rawlog.EventBluetooth, 30*min, nil,
			rawlog.BluetoothPayload{DeviceName: "LE_Headphones", Connected: false}),
		androidEvent(t, "b4", rawlog.EventBluetooth, 30*min+2*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: false}),
	}

	states := proposalsBySource(runNormalize(t, events, 60*min), SourceBluetooth)
	if len(states) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(states), describeProposals(states))
	}
	if states[0].Title != "Bluetooth Headphones" {
		t.Errorf("タイトル = %q, want %q", states[0].Title, "Bluetooth Headphones")
	}
}

func TestBluetoothNameWithoutLEPrefixIsKept(t *testing.T) {
	// LE_ で始まらない機器名は変えない。
	tests := []struct {
		in   string
		want string
	}{
		{in: "Headphones", want: "Headphones"},
		{in: "LE_Headphones", want: "Headphones"},
		{in: "le_Headphones", want: "Headphones"},
		{in: "LED Lamp", want: "LED Lamp"},
		{in: "Keyboard LE_Extra", want: "Keyboard LE_Extra"},
		{in: "", want: ""},
	}

	for _, tt := range tests {
		if got := bluetoothDeviceName(tt.in); got != tt.want {
			t.Errorf("bluetoothDeviceName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConnectionRereadDoesNotInvertInterval(t *testing.T) {
	// カーソルの引き戻しで、既に処理した接続・切断を読み直すことがある。
	// 読み直しても終了が開始より前の区間を作らないこと。
	//
	// 持ち越した区間の開始より前にある切断を、その区間を閉じる相手にしてしまうと
	// 「07/26 13:35:03 → 13:32:33」のような区間ができる。
	carried := []rawlog.OpenStateInterval{{
		Device:   rawlog.Device("Phone"),
		Source:   SourceBluetooth,
		Key:      "Mouse",
		Title:    "Bluetooth Mouse",
		Start:    base().Add(10 * min),
		EventIDs: []string{"b3"},
	}}

	// 読み直しで、持ち越した開始より前の切断が混ざる。
	events := []*rawlog.Event{
		androidEvent(t, "b2", rawlog.EventBluetooth, 7*min, nil,
			rawlog.BluetoothPayload{DeviceName: "Mouse", Connected: false}),
		androidEvent(t, "b4", rawlog.EventBluetooth, 40*min, nil,
			rawlog.BluetoothPayload{DeviceName: "Mouse", Connected: false}),
	}

	result := runNormalizeWith(t, events, Options{
		Cutoff:     base().Add(60 * min),
		OpenStates: carried,
	})

	states := proposalsBySource(result, SourceBluetooth)
	if len(states) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(states), describeProposals(states))
	}
	if states[0].EndTime.Before(*states[0].StartTime) {
		t.Errorf("終了が開始より前: %v", describeProposals(states))
	}
	if end := states[0].EndTime.Sub(base()); end != 40*min {
		t.Errorf("終了 = %v, want %v", end, 40*min)
	}
}

func TestConnectionRereadInsideSettledIntervalMakesNoNewInterval(t *testing.T) {
	// 確定済みの区間の内側にある接続を読み直しても、区間を増やさない。
	carried := []rawlog.OpenStateInterval{{
		Device:   rawlog.Device("Phone"),
		Source:   SourceBluetooth,
		Key:      "Headphones",
		Title:    "Bluetooth Headphones",
		Start:    base(),
		End:      timePtr(base().Add(30 * min)),
		EventIDs: []string{"b1", "b2"},
	}}

	events := []*rawlog.Event{
		// 既に確定した区間の内側にある接続の読み直し。
		androidEvent(t, "b1", rawlog.EventBluetooth, 0, nil,
			rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: true}),
	}

	result := runNormalizeWith(t, events, Options{
		Cutoff:     base().Add(60 * min),
		OpenStates: carried,
	})

	states := proposalsBySource(result, SourceBluetooth)
	if len(states) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(states), describeProposals(states))
	}
	if start := states[0].StartTime.Sub(base()); start != 0 {
		t.Errorf("開始 = %v, want 0", start)
	}
	if end := states[0].EndTime.Sub(base()); end != 30*min {
		t.Errorf("終了 = %v, want %v", end, 30*min)
	}
}

func TestMakeIDIgnoresDuplicateEventIDs(t *testing.T) {
	// 読み直しで同じイベントIDが2回入っても識別子は変わらない。
	// 変わると、同じ区間が別の提案として二重に書き込まれる。
	once := makeID(KindTimeIs, SourceWindow, []string{"e1", "e2"})
	twice := makeID(KindTimeIs, SourceWindow, []string{"e1", "e2", "e1"})
	if once != twice {
		t.Errorf("識別子が変わった: %s != %s", once, twice)
	}
}

func TestCarryStateOfSilentDeviceSurvives(t *testing.T) {
	// イベントが1件も届かなかった端末の持ち越しを落とさないこと。
	// 端末ごとに処理する作りなので、拾い直さないと静かにしていた端末の
	// 接続区間が消える。
	events := []*rawlog.Event{
		event(t, "l1", rawlog.EventInput, 0, nil,
			rawlog.InputPayload{AppName: "メモ帳", WindowTitle: "無題 - メモ帳"}),
	}

	carried := []rawlog.OpenStateInterval{{
		Device:   rawlog.Device("Phone"),
		Source:   SourceBluetooth,
		Key:      "Headphones",
		Title:    "Bluetooth Headphones",
		Start:    base(),
		EventIDs: []string{"b1"},
	}}

	result := runNormalizeWith(t, events, Options{
		Cutoff:     base().Add(60 * min),
		OpenStates: carried,
	})

	if len(result.OpenStates) != 1 {
		t.Fatalf("持ち越し = %d 件, want 1", len(result.OpenStates))
	}
	if result.OpenStates[0].Device != rawlog.Device("Phone") {
		t.Errorf("端末 = %q, want Phone", result.OpenStates[0].Device)
	}
}

func TestPullbackDoesNotAbsorbWrittenIntervalsIntoCarriedTail(t *testing.T) {
	// カーソルの引き戻し (低水位マーク・書き込み失敗) では、書き込み済みの
	// 区間と持ち越しの末尾を同じバッチで読み直す。このとき両者が1つの提案へ
	// 結合されると「一部だけ書き込み済み」の提案になり、Writer の一部重複
	// ポリシーで丸ごと破棄されて、未書き込みの持ち越し末尾が恒久に失われる。
	// 読み直した区間は元と同じ提案に戻り、末尾は独立の提案になること。

	early := androidEvent(t, "app-written", rawlog.EventAppUsage, 0, durationPtr(10*min),
		rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"})
	tail := androidEvent(t, "app-carried", rawlog.EventAppUsage, 58*min, durationPtr(59*min+50*sec),
		rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"})
	events := []*rawlog.Event{early, tail}

	// 1回目: 前半は確定して書き込まれ、末尾は cutoff 間際なので持ち越しになる。
	first := runNormalizeWith(t, events, Options{Cutoff: base().Add(60 * min)})
	firstApps := proposalsBySource(first, SourceWindow)
	if len(firstApps) != 1 || len(first.OpenStates) != 1 {
		t.Fatalf("前提が崩れた: 確定 %d 件 / 持ち越し %d 件, want 1 / 1",
			len(firstApps), len(first.OpenStates))
	}
	writtenID := firstApps[0].ID
	written := map[string]struct{}{}
	for _, id := range firstApps[0].SourceEventIDs {
		written[id] = struct{}{}
	}

	// 2回目: 引き戻しで同じイベントを持ち越しと一緒に読み直す。
	second := runNormalizeWith(t, events, Options{
		Cutoff:     base().Add(120 * min),
		OpenStates: first.OpenStates,
	})
	secondApps := proposalsBySource(second, SourceWindow)

	uniqueIDs := func(ids []string) []string {
		s := slices.Clone(ids)
		slices.Sort(s)
		return slices.Compact(s)
	}
	var sawWritten, sawTail bool
	for _, p := range secondApps {
		ids := uniqueIDs(p.SourceEventIDs)
		covered := 0
		for _, id := range ids {
			if _, ok := written[id]; ok {
				covered++
			}
		}
		if covered != 0 && covered != len(ids) {
			t.Errorf("書き込み済みと未書き込みが混ざった提案ができた (Writer が破棄して末尾が失われる): %v", ids)
		}
		if p.ID == writtenID {
			sawWritten = true
		}
		if len(ids) == 1 && ids[0] == "app-carried" {
			sawTail = true
		}
	}
	if !sawWritten {
		t.Error("読み直した区間が元と同じ提案に戻っていない (台帳で除外できない)")
	}
	if !sawTail {
		t.Error("持ち越し末尾が独立の提案として確定していない")
	}
}
