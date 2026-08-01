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
//
// 台帳が提案IDで重複を落とすので、ここでもIDで重複を落とす。
// カーソルの引き戻しで同じイベントを読み直すことがあり、
// それ自体は正しい動きだからである。
func runNormalizeSliced(t *testing.T, events []*rawlog.Event, opts Options, cutoffs []time.Duration) []Proposal {
	t.Helper()

	var (
		from        time.Time
		carriedOpen []rawlog.OpenStateInterval
		carriedSeen []rawlog.NotificationSeen
		byID        = map[string]Proposal{}
	)

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
			byID[proposal.ID] = proposal
		}

		carriedOpen = result.OpenStates
		carriedSeen = result.NotificationSeen
		from = result.SafeCursor
	}

	merged := make([]Proposal, 0, len(byID))
	for _, proposal := range byID {
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

		// 通知: 同じ内容が2分後に再通知される。重複排除の窓は5分。
		androidEvent(t, "n1", rawlog.EventNotification, 10*sec, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),
		androidEvent(t, "n2", rawlog.EventNotification, 130*sec, nil,
			rawlog.NotificationPayload{AppLabel: "Gmail", PackageName: "com.google.android.gm", Title: "件名", Body: "本文"}),

		// Bluetooth: 接続したまま切れ目をまたぐ。
		androidEvent(t, "b1", rawlog.EventBluetooth, 5*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "WH-1000XM6", Connected: true}),
		androidEvent(t, "b2", rawlog.EventBluetooth, 200*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "WH-1000XM6", Connected: false}),
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
	// 除外リストはパッケージ名とアプリ名の両方を見る。
	list, err := ParseDenyList(strings.NewReader("com.termux.api\nTasker\nre:^com\\.example\\.\n"))
	if err != nil {
		t.Fatalf("ParseDenyList: %v", err)
	}

	tests := []struct {
		name        string
		packageName string
		appLabel    string
		want        int
	}{
		{name: "パッケージ名で除外", packageName: "com.termux.api", appLabel: "Termux:API", want: 0},
		{name: "アプリ名で除外", packageName: "net.dinglisch.android.taskerm", appLabel: "Tasker", want: 0},
		{name: "正規表現で除外", packageName: "com.example.app", appLabel: "サンプル", want: 0},
		{name: "当たらなければ残す", packageName: "com.google.android.gm", appLabel: "Gmail", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []*rawlog.Event{
				androidEvent(t, "n1", rawlog.EventNotification, 0, nil, rawlog.NotificationPayload{
					AppLabel: tt.appLabel, PackageName: tt.packageName, Title: "件名", Body: "本文",
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

func TestBluetoothLEAndClassicBecomeOneInterval(t *testing.T) {
	// 1台のヘッドホンがクラシックと LE の両方でつながっても TimeIs は1本。
	events := []*rawlog.Event{
		androidEvent(t, "b1", rawlog.EventBluetooth, 0, nil,
			rawlog.BluetoothPayload{DeviceName: "WH-1000XM6", Connected: true}),
		androidEvent(t, "b2", rawlog.EventBluetooth, 2*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "LE_WH-1000XM6", Connected: true}),
		androidEvent(t, "b3", rawlog.EventBluetooth, 30*min, nil,
			rawlog.BluetoothPayload{DeviceName: "LE_WH-1000XM6", Connected: false}),
		androidEvent(t, "b4", rawlog.EventBluetooth, 30*min+2*sec, nil,
			rawlog.BluetoothPayload{DeviceName: "WH-1000XM6", Connected: false}),
	}

	states := proposalsBySource(runNormalize(t, events, 60*min), SourceBluetooth)
	if len(states) != 1 {
		t.Fatalf("件数 = %d, want 1 (%v)", len(states), describeProposals(states))
	}
	if states[0].Title != "Bluetooth WH-1000XM6" {
		t.Errorf("タイトル = %q, want %q", states[0].Title, "Bluetooth WH-1000XM6")
	}
}

func TestBluetoothNameWithoutLEPrefixIsKept(t *testing.T) {
	// LE_ で始まらない機器名は変えない。
	tests := []struct {
		in   string
		want string
	}{
		{in: "WH-1000XM6", want: "WH-1000XM6"},
		{in: "LE_WH-1000XM6", want: "WH-1000XM6"},
		{in: "le_WH-1000XM6", want: "WH-1000XM6"},
		{in: "LED Lamp", want: "LED Lamp"},
		{in: "Keyboard LE_K370", want: "Keyboard LE_K370"},
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
		Key:      "M720 Triathlon",
		Title:    "Bluetooth M720 Triathlon",
		Start:    base().Add(10 * min),
		EventIDs: []string{"b3"},
	}}

	// 読み直しで、持ち越した開始より前の切断が混ざる。
	events := []*rawlog.Event{
		androidEvent(t, "b2", rawlog.EventBluetooth, 7*min, nil,
			rawlog.BluetoothPayload{DeviceName: "M720 Triathlon", Connected: false}),
		androidEvent(t, "b4", rawlog.EventBluetooth, 40*min, nil,
			rawlog.BluetoothPayload{DeviceName: "M720 Triathlon", Connected: false}),
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
		Key:      "WH-1000XM6",
		Title:    "Bluetooth WH-1000XM6",
		Start:    base(),
		End:      timePtr(base().Add(30 * min)),
		EventIDs: []string{"b1", "b2"},
	}}

	events := []*rawlog.Event{
		// 既に確定した区間の内側にある接続の読み直し。
		androidEvent(t, "b1", rawlog.EventBluetooth, 0, nil,
			rawlog.BluetoothPayload{DeviceName: "WH-1000XM6", Connected: true}),
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
		Key:      "WH-1000XM6",
		Title:    "Bluetooth WH-1000XM6",
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
