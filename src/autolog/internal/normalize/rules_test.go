package normalize

import (
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// ---------------------------------------------------------------- 端末利用

func TestUsageSessionFromUnlockToLock(t *testing.T) {
	result := runNormalize(t, []*rawlog.Event{
		event(t, "s1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		event(t, "s2", rawlog.EventSession, 90*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLock}),
	}, 120*min)

	usage := proposalsBySource(result, SourceDevice)
	if len(usage) != 1 {
		t.Fatalf("件数 = %d, want 1 (%+v)", len(usage), usage)
	}
	if usage[0].Title != DefaultUsageTitle {
		t.Errorf("タイトル = %q, want %q", usage[0].Title, DefaultUsageTitle)
	}
	if usage[0].StartTime.Sub(base()) != 0 || usage[0].EndTime.Sub(base()) != 90*min {
		t.Errorf("区間 = %v..%v", usage[0].StartTime.Sub(base()), usage[0].EndTime.Sub(base()))
	}
}

func TestUsageSessionInProgressIsDeferred(t *testing.T) {
	// 閉じていないセッションは処理せず、カーソルも開始時刻より先へ進めない（要件 §17）。
	result := runNormalize(t, []*rawlog.Event{
		event(t, "s1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		event(t, "s2", rawlog.EventSession, 30*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLock}),
		event(t, "s3", rawlog.EventSession, 40*min, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
	}, 120*min)

	if got := len(proposalsBySource(result, SourceDevice)); got != 1 {
		t.Fatalf("件数 = %d, want 1", got)
	}
	if cursor := result.SafeCursor.Sub(base()); cursor != 40*min {
		t.Errorf("SafeCursor = %v, want %v", cursor, 40*min)
	}
}

func TestUsageSessionDoesNotSpanReboot(t *testing.T) {
	// シャットダウンで必ず閉じるため、再起動前後は結合されない（要件 §6.1）。
	result := runNormalize(t, []*rawlog.Event{
		event(t, "s1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionLogon}),
		event(t, "s2", rawlog.EventSession, 30*min, nil, rawlog.SessionPayload{Action: rawlog.SessionShutdown}),
		event(t, "s3", rawlog.EventSession, 60*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLogon}),
		event(t, "s4", rawlog.EventSession, 90*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLock}),
	}, 120*min)

	usage := proposalsBySource(result, SourceDevice)
	if len(usage) != 2 {
		t.Fatalf("件数 = %d, want 2 (%+v)", len(usage), usage)
	}
	if usage[0].EndTime.Sub(base()) != 30*min || usage[1].StartTime.Sub(base()) != 60*min {
		t.Error("再起動前後が結合されている")
	}
}

// 端末利用のタイトルは設定で差し替えられる。
// 端末ごとの呼び分け（「Windows利用」など）はコードではなく設定で決める。
func TestUsageTitleIsConfigurable(t *testing.T) {
	const title = "この端末を利用"

	result := runNormalizeWith(t, []*rawlog.Event{
		androidEvent(t, "s1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		androidEvent(t, "s2", rawlog.EventSession, 10*min, nil, rawlog.SessionPayload{Action: rawlog.SessionScreenOff}),
	}, Options{Cutoff: base().Add(60 * min), UsageTitle: title})

	usage := proposalsBySource(result, SourceDevice)
	if len(usage) != 1 {
		t.Fatalf("件数 = %d, want 1", len(usage))
	}
	if usage[0].Title != title {
		t.Errorf("タイトル = %q, want %q", usage[0].Title, title)
	}
}

// ---------------------------------------------------------------- ブラウザ

func browserEvent(t *testing.T, id string, start, duration time.Duration, url string) *rawlog.Event {
	t.Helper()
	return event(t, id, rawlog.EventBrowserView, start, durationPtr(start+duration),
		rawlog.BrowserViewPayload{URL: url, Title: "タイトル", BrowserFocused: true, Source: "chrome_extension"})
}

func TestBrowserViewMinimumDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     int
	}{
		// 1回の連続閲覧が30秒以上なら候補（要件 §7.1）。
		{name: "29秒は除外", duration: 29 * sec, want: 0},
		{name: "30秒ちょうどは候補", duration: 30 * sec, want: 1},
		{name: "31秒は候補", duration: 31 * sec, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				browserEvent(t, "v1", 0, tt.duration, "https://example.com/"),
			}, 60*min)

			if got := len(proposalsBySource(result, SourceBrowser)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
			if got := len(result.URLCandidates); got != tt.want {
				t.Errorf("候補数 = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBrowserViewExcludedURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want int
	}{
		{name: "新しいタブ", url: "chrome://newtab/", want: 0},
		{name: "Chrome設定画面", url: "chrome://settings/privacy", want: 0},
		{name: "拡張機能画面", url: "chrome-extension://abcdef/options.html", want: 0},
		{name: "about:blank", url: "about:blank", want: 0},
		{name: "空文字", url: "", want: 0},
		// 保存対象（要件 §7.1）。
		{name: "Google検索結果", url: "https://www.google.com/search?q=gkill", want: 1},
		{name: "localhost", url: "http://localhost:9999/rykv", want: 1},
		{name: "gkill自身のページ", url: "http://127.0.0.1:9999/mi", want: 1},
		{name: "一般のWebページ", url: "https://github.com/mt3hr/gkill", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				browserEvent(t, "v1", 0, 5*min, tt.url),
			}, 60*min)

			if got := len(proposalsBySource(result, SourceBrowser)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBrowserViewSkipsMediaServices(t *testing.T) {
	// YouTube / YouTube Music は「個別コンテンツの URLog だけを残す」（要件 §3、§8.1）。
	// 閲覧区間からは作らない。作ると media_play と二重になり、
	// トップページや検索結果まで URLog になってしまう。
	tests := []struct {
		name string
		url  string
		want int
	}{
		{name: "YouTube視聴ページは作らない", url: "https://www.youtube.com/watch?v=abc123", want: 0},
		{name: "YouTubeトップも作らない", url: "https://www.youtube.com/", want: 0},
		{name: "YouTube検索結果も作らない", url: "https://www.youtube.com/results?search_query=go", want: 0},
		{name: "YouTube Musicも作らない", url: "https://music.youtube.com/watch?v=abc", want: 0},
		{name: "短縮URLも作らない", url: "https://youtu.be/abc123", want: 0},
		{name: "モバイル版も作らない", url: "https://m.youtube.com/watch?v=abc", want: 0},
		// ホスト名で判定するので、URL の一部に含まれるだけなら影響しない。
		{name: "別サイトの参照元パラメータは巻き込まない", url: "https://example.com/?ref=youtube.com", want: 1},
		{name: "紛らわしいドメインも巻き込まない", url: "https://notyoutube.com/watch", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				browserEvent(t, "v1", 0, 5*min, tt.url),
			}, 60*min)

			if got := len(proposalsBySource(result, SourceBrowser)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMediaPlayStillRecordsYouTube(t *testing.T) {
	// browser 経路から外しても、再生そのものは media_play で記録される。
	result := runNormalize(t, []*rawlog.Event{
		browserEvent(t, "v1", 0, 5*min, "https://www.youtube.com/watch?v=abc123"),
		mediaEvent(t, "m1", 0, 300, "https://www.youtube.com/watch?v=abc123"),
	}, 60*min)

	if got := len(proposalsBySource(result, SourceBrowser)); got != 0 {
		t.Errorf("browser 由来 = %d 件, want 0", got)
	}
	media := proposalsBySource(result, SourceMedia)
	if len(media) != 1 {
		t.Fatalf("media 由来 = %d 件, want 1", len(media))
	}
	if media[0].URL != "https://www.youtube.com/watch?v=abc123" {
		t.Errorf("URL = %q", media[0].URL)
	}
}

func TestBrowserViewDenyList(t *testing.T) {
	denyList, err := ParseDenyList(strings.NewReader(`
# コメントは無視
doubleclick.net
x.com/home
re:^https://example\.org/ads/
`))
	if err != nil {
		t.Fatalf("ParseDenyList: %v", err)
	}
	if denyList.Len() != 3 {
		t.Fatalf("パターン数 = %d, want 3", denyList.Len())
	}

	tests := []struct {
		name string
		url  string
		want int
	}{
		{name: "部分一致で除外", url: "https://ad.doubleclick.net/x", want: 0},
		{name: "大文字小文字を区別しない", url: "https://AD.DoubleClick.NET/x", want: 0},
		{name: "パス込みの部分一致", url: "https://x.com/home", want: 0},
		{name: "正規表現で除外", url: "https://example.org/ads/banner", want: 0},
		{name: "似ているが一致しないものは残す", url: "https://x.com/someone/status/1", want: 1},
		{name: "無関係なページは残す", url: "https://go.dev/doc/", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []*rawlog.Event{browserEvent(t, "v1", 0, 5*min, tt.url)}
			result, err := Run(events, Options{Cutoff: base().Add(60 * min), DenyList: denyList})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(proposalsBySource(result, SourceBrowser)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDenyListRejectsBadRegexp(t *testing.T) {
	if _, err := ParseDenyList(strings.NewReader("re:[unclosed")); err == nil {
		t.Error("不正な正規表現でエラーにならなかった")
	}
}

func TestBrowserViewSameURLIsSeparateSession(t *testing.T) {
	// 同一URLでも再度表示した場合は別セッション。離れた区間の時間は合算しない（要件 §7.1）。
	result := runNormalize(t, []*rawlog.Event{
		browserEvent(t, "v1", 0, 40*sec, "https://example.com/"),
		browserEvent(t, "v2", 30*min, 50*sec, "https://example.com/"),
	}, 60*min)

	views := proposalsBySource(result, SourceBrowser)
	if len(views) != 2 {
		t.Fatalf("件数 = %d, want 2 (別セッションとして残すべき)", len(views))
	}
	// RelatedTime は閲覧開始時刻。
	if views[0].RelatedTime.Sub(base()) != 0 || views[1].RelatedTime.Sub(base()) != 30*min {
		t.Error("RelatedTime が閲覧開始時刻になっていない")
	}
	if views[0].ID == views[1].ID {
		t.Error("別セッションなのに同じIDになっている")
	}
}

func TestBrowserViewFromHistoryDBWithoutForegroundIsSkipped(t *testing.T) {
	// 履歴DBに存在するだけで前面表示を確認できないページは自動登録しない（要件 §12）。
	result := runNormalize(t, []*rawlog.Event{
		event(t, "v1", rawlog.EventBrowserView, 0, durationPtr(5*min),
			rawlog.BrowserViewPayload{URL: "https://example.com/", Title: "T",
				BrowserFocused: false, Source: "chrome_history_db"}),
	}, 60*min)

	if got := len(proposalsBySource(result, SourceBrowser)); got != 0 {
		t.Errorf("件数 = %d, want 0", got)
	}
}

func TestBrowserViewWithoutEndTimeIsSkipped(t *testing.T) {
	// 不完全なイベントは gkill へ書かず生ログへ残す（要件 §17）。
	result := runNormalize(t, []*rawlog.Event{
		event(t, "v1", rawlog.EventBrowserView, 0, nil,
			rawlog.BrowserViewPayload{URL: "https://example.com/", Title: "T", BrowserFocused: true}),
	}, 60*min)

	if got := len(proposalsBySource(result, SourceBrowser)); got != 0 {
		t.Errorf("件数 = %d, want 0", got)
	}
}

// ---------------------------------------------------------------- 再生

func mediaEvent(t *testing.T, id string, start time.Duration, played float64, url string) *rawlog.Event {
	t.Helper()
	return event(t, id, rawlog.EventMediaPlay, start, nil, rawlog.MediaPlayPayload{
		Service: rawlog.ServiceYouTube, URL: url, VideoID: "abc",
		Title: "動画タイトル", PlayedSeconds: played,
	})
}

func TestMediaPlayMinimumPlayedSeconds(t *testing.T) {
	tests := []struct {
		name   string
		played float64
		want   int
	}{
		// 実再生時間が合計30秒以上（要件 §8.1）。
		{name: "29秒は除外", played: 29, want: 0},
		{name: "30秒ちょうどは記録", played: 30, want: 1},
		{name: "31秒は記録", played: 31, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				mediaEvent(t, "m1", 0, tt.played, "https://www.youtube.com/watch?v=abc"),
			}, 60*min)

			if got := len(proposalsBySource(result, SourceMedia)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMediaPlayWithoutURLIsSkipped(t *testing.T) {
	// URLを取得できない場合、検索URLや推測したURLは生成しない。
	// Kmemoへの代替記録も行わない（要件 §8.2）。
	result := runNormalize(t, []*rawlog.Event{
		mediaEvent(t, "m1", 0, 300, ""),
	}, 60*min)

	if len(result.Proposals) != 0 {
		t.Errorf("URLが無い再生から %d 件作られた: %+v", len(result.Proposals), result.Proposals)
	}
}

func TestMediaPlayCreatesOneURLogPerPlayback(t *testing.T) {
	// 同じ動画や楽曲でも、再生の都度 URLog を作る（要件 §8.1）。
	const url = "https://www.youtube.com/watch?v=abc"
	result := runNormalize(t, []*rawlog.Event{
		mediaEvent(t, "m1", 0, 120, url),
		mediaEvent(t, "m2", 30*min, 90, url),
	}, 60*min)

	plays := proposalsBySource(result, SourceMedia)
	if len(plays) != 2 {
		t.Fatalf("件数 = %d, want 2", len(plays))
	}
	// RelatedTime は再生開始時刻。
	if plays[0].RelatedTime.Sub(base()) != 0 || plays[1].RelatedTime.Sub(base()) != 30*min {
		t.Error("RelatedTime が再生開始時刻になっていない")
	}
}

func TestMediaPlayDoesNotCreateTimeIs(t *testing.T) {
	// YouTube / YouTube Music が作る Kyou は URLog のみ（要件 §8.1）。
	result := runNormalize(t, []*rawlog.Event{
		mediaEvent(t, "m1", 0, 300, "https://www.youtube.com/watch?v=abc"),
	}, 60*min)

	for _, p := range result.Proposals {
		if p.Kind == KindTimeIs {
			t.Errorf("再生から TimeIs が作られた: %+v", p)
		}
	}
}

// ---------------------------------------------------------------- 接続状態

func wifiEvent(t *testing.T, id string, at time.Duration, ssid string, connected bool) *rawlog.Event {
	t.Helper()
	return event(t, id, rawlog.EventWifi, at, nil, rawlog.WifiPayload{SSID: ssid, Connected: connected})
}

func TestWifiReconnectMerging(t *testing.T) {
	tests := []struct {
		name      string
		gap       time.Duration
		wantCount int
	}{
		// 同じSSIDへ30秒以内に再接続した場合は前後を結合する（要件 §9.1）。
		{name: "30秒ちょうどで結合", gap: 30 * sec, wantCount: 1},
		{name: "31秒なら別TimeIs", gap: 31 * sec, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				wifiEvent(t, "w1", 0, "TestWifi", true),
				wifiEvent(t, "w2", 10*min, "TestWifi", false),
				wifiEvent(t, "w3", 10*min+tt.gap, "TestWifi", true),
				wifiEvent(t, "w4", 20*min, "TestWifi", false),
			}, 60*min)

			wifi := proposalsBySource(result, SourceWifi)
			if len(wifi) != tt.wantCount {
				t.Fatalf("件数 = %d, want %d (%+v)", len(wifi), tt.wantCount, wifi)
			}
			if tt.wantCount == 1 && wifi[0].EndTime.Sub(base()) != 20*min {
				t.Errorf("結合後の終了時刻 = %v, want %v", wifi[0].EndTime.Sub(base()), 20*min)
			}
			if wifi[0].Title != "Wi-Fi TestWifi" {
				t.Errorf("タイトル = %q", wifi[0].Title)
			}
		})
	}
}

func TestBluetoothMergingAndSimultaneousDevices(t *testing.T) {
	// 同じ機器へ1分以内に再接続した場合は結合。
	// 複数機器へ同時接続している場合は、それぞれ別 TimeIs（要件 §9.2）。
	result := runNormalize(t, []*rawlog.Event{
		event(t, "b1", rawlog.EventBluetooth, 0, nil, rawlog.BluetoothPayload{DeviceName: "Keyboard", Connected: true}),
		event(t, "b2", rawlog.EventBluetooth, 1*min, nil, rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: true}),
		event(t, "b3", rawlog.EventBluetooth, 10*min, nil, rawlog.BluetoothPayload{DeviceName: "Keyboard", Connected: false}),
		event(t, "b4", rawlog.EventBluetooth, 10*min+59*sec, nil, rawlog.BluetoothPayload{DeviceName: "Keyboard", Connected: true}),
		event(t, "b5", rawlog.EventBluetooth, 20*min, nil, rawlog.BluetoothPayload{DeviceName: "Keyboard", Connected: false}),
		event(t, "b6", rawlog.EventBluetooth, 25*min, nil, rawlog.BluetoothPayload{DeviceName: "Headphones", Connected: false}),
	}, 60*min)

	bluetooth := proposalsBySource(result, SourceBluetooth)
	if len(bluetooth) != 2 {
		t.Fatalf("件数 = %d, want 2 (機器ごとに1件) (%+v)", len(bluetooth), bluetooth)
	}

	byTitle := map[string]Proposal{}
	for _, p := range bluetooth {
		byTitle[p.Title] = p
	}
	keyboard, ok := byTitle["Bluetooth Keyboard"]
	if !ok {
		t.Fatalf("Keyboard の TimeIs が無い: %+v", bluetooth)
	}
	if keyboard.EndTime.Sub(base()) != 20*min {
		t.Errorf("Keyboard の終了時刻 = %v, want %v (59秒の切断は結合される)", keyboard.EndTime.Sub(base()), 20*min)
	}
	if _, ok := byTitle["Bluetooth Headphones"]; !ok {
		t.Error("Headphones の TimeIs が無い")
	}
}

func TestChargeMergingAndTitle(t *testing.T) {
	result := runNormalize(t, []*rawlog.Event{
		event(t, "p1", rawlog.EventPower, 0, nil, rawlog.PowerPayload{Charging: true}),
		event(t, "p2", rawlog.EventPower, 30*min, nil, rawlog.PowerPayload{Charging: false}),
		event(t, "p3", rawlog.EventPower, 30*min+20*sec, nil, rawlog.PowerPayload{Charging: true}),
		event(t, "p4", rawlog.EventPower, 50*min, nil, rawlog.PowerPayload{Charging: false}),
	}, 60*min)

	charge := proposalsBySource(result, SourceCharge)
	if len(charge) != 1 {
		t.Fatalf("件数 = %d, want 1 (30秒以内の再開は結合) (%+v)", len(charge), charge)
	}
	if charge[0].Title != "充電" {
		t.Errorf("タイトル = %q, want 充電", charge[0].Title)
	}
	if charge[0].EndTime.Sub(base()) != 50*min {
		t.Errorf("終了時刻 = %v, want %v", charge[0].EndTime.Sub(base()), 50*min)
	}
}

func TestConnectionStillOpenIsDeferred(t *testing.T) {
	// 切断を観測していない接続は継続中。処理せず次回へ持ち越す。
	result := runNormalize(t, []*rawlog.Event{
		wifiEvent(t, "w1", 10*min, "TestWifi", true),
	}, 60*min)

	if got := len(proposalsBySource(result, SourceWifi)); got != 0 {
		t.Errorf("件数 = %d, want 0", got)
	}
	if cursor := result.SafeCursor.Sub(base()); cursor != 10*min {
		t.Errorf("SafeCursor = %v, want %v", cursor, 10*min)
	}
}

// ---------------------------------------------------------------- 通知

func notificationEvent(t *testing.T, id string, at time.Duration, key, title, body string, ongoing bool) *rawlog.Event {
	t.Helper()
	return androidEvent(t, id, rawlog.EventNotification, at, nil, rawlog.NotificationPayload{
		AppLabel: "Gmail", PackageName: "com.google.android.gm",
		Title: title, Body: body, NotificationKey: key, Ongoing: ongoing,
	})
}

func TestNotificationContentFormat(t *testing.T) {
	result := runNormalize(t, []*rawlog.Event{
		notificationEvent(t, "n1", 0, "k1", "○○さんからのメール", "明日の打ち合わせについて", false),
	}, 60*min)

	kmemos := proposalsBySource(result, SourceNotification)
	if len(kmemos) != 1 {
		t.Fatalf("件数 = %d, want 1", len(kmemos))
	}
	want := "アプリ：Gmail\nタイトル：○○さんからのメール\n本文：明日の打ち合わせについて"
	if kmemos[0].Content != want {
		t.Errorf("本文 =\n%s\nwant\n%s", kmemos[0].Content, want)
	}
	if kmemos[0].Kind != KindKmemo {
		t.Errorf("種類 = %q, want kmemo", kmemos[0].Kind)
	}
	// パッケージ名は gkill へ保存しない（要件 §11.2）。
	if strings.Contains(kmemos[0].Content, "com.google.android.gm") {
		t.Error("パッケージ名が本文に含まれている")
	}
}

func TestNotificationTitleOrBodyOnly(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
		want  string
	}{
		{name: "タイトルだけ", title: "件名のみ", body: "", want: "アプリ：Gmail\nタイトル：件名のみ"},
		{name: "本文だけ", title: "", body: "本文のみ", want: "アプリ：Gmail\n本文：本文のみ"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				notificationEvent(t, "n1", 0, "k1", tt.title, tt.body, false),
			}, 60*min)

			kmemos := proposalsBySource(result, SourceNotification)
			if len(kmemos) != 1 {
				t.Fatalf("件数 = %d, want 1", len(kmemos))
			}
			if kmemos[0].Content != tt.want {
				t.Errorf("本文 = %q, want %q", kmemos[0].Content, tt.want)
			}
		})
	}
}

func TestNotificationExclusions(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		body    string
		ongoing bool
	}{
		{name: "常駐通知は除外", title: "再生中", body: "曲名", ongoing: true},
		{name: "タイトルも本文も空なら除外", title: "", body: "", ongoing: false},
		{name: "空白のみも除外", title: "  ", body: "\t", ongoing: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				notificationEvent(t, "n1", 0, "k1", tt.title, tt.body, tt.ongoing),
			}, 60*min)

			if got := len(proposalsBySource(result, SourceNotification)); got != 0 {
				t.Errorf("件数 = %d, want 0", got)
			}
		})
	}
}

func TestNotificationUpdateKeepsLastStateAndFirstTime(t *testing.T) {
	// 同一通知IDの更新は最終状態だけを記録し、RelatedTime は最初の通知時刻（要件 §13）。
	result := runNormalize(t, []*rawlog.Event{
		notificationEvent(t, "n1", 0, "same-key", "1件の新着", "最初の本文", false),
		notificationEvent(t, "n2", 10*min, "same-key", "3件の新着", "最後の本文", false),
	}, 60*min)

	kmemos := proposalsBySource(result, SourceNotification)
	if len(kmemos) != 1 {
		t.Fatalf("件数 = %d, want 1 (%+v)", len(kmemos), kmemos)
	}
	if !strings.Contains(kmemos[0].Content, "3件の新着") || !strings.Contains(kmemos[0].Content, "最後の本文") {
		t.Errorf("最終状態が採用されていない: %q", kmemos[0].Content)
	}
	if kmemos[0].RelatedTime.Sub(base()) != 0 {
		t.Errorf("RelatedTime = %v, want 0 (最初の通知時刻)", kmemos[0].RelatedTime.Sub(base()))
	}
}

func TestNotificationRepeatWithinShortWindowIsCollapsed(t *testing.T) {
	tests := []struct {
		name string
		gap  time.Duration
		want int
	}{
		{name: "5分以内の同内容再通知は1件", gap: 4 * min, want: 1},
		{name: "十分離れていれば別件", gap: 6 * min, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				notificationEvent(t, "n1", 0, "key-a", "同じ件名", "同じ本文", false),
				notificationEvent(t, "n2", tt.gap, "key-b", "同じ件名", "同じ本文", false),
			}, 60*min)

			if got := len(proposalsBySource(result, SourceNotification)); got != tt.want {
				t.Errorf("件数 = %d, want %d", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------- アプリ利用

func TestAppUsageMinimumDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     int
	}{
		// アプリが30秒以上利用された場合だけ記録（要件 §11.2）。
		{name: "29秒は除外", duration: 29 * sec, want: 0},
		{name: "30秒ちょうどは記録", duration: 30 * sec, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runNormalize(t, []*rawlog.Event{
				androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(tt.duration),
					rawlog.AppUsagePayload{AppLabel: "Chrome", PackageName: "com.android.chrome"}),
			}, 60*min)

			apps := proposalsBySource(result, SourceWindow)
			if len(apps) != tt.want {
				t.Fatalf("件数 = %d, want %d", len(apps), tt.want)
			}
			if tt.want == 1 && apps[0].Title != "Chrome" {
				// パッケージ名ではなく表示上のアプリ名を使う。
				t.Errorf("タイトル = %q, want Chrome", apps[0].Title)
			}
		})
	}
}

func TestAppUsageDuplicateIntervalIsCollapsed(t *testing.T) {
	// UsageStatsManager は前面のままのアプリがあると読み出し位置が戻るため、
	// 収集側が同じ区間を再送することがある。同じ区間は1件にまとめる。
	result := runNormalize(t, []*rawlog.Event{
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(10*min),
			rawlog.AppUsagePayload{AppLabel: "Claude", PackageName: "com.anthropic.claude"}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 0, durationPtr(10*min),
			rawlog.AppUsagePayload{AppLabel: "Claude", PackageName: "com.anthropic.claude"}),
	}, 60*min)

	apps := proposalsBySource(result, SourceWindow)
	if len(apps) != 1 {
		t.Fatalf("件数 = %d, want 1 (%+v)", len(apps), apps)
	}
}

func TestAppUsageDifferentIntervalsAreKept(t *testing.T) {
	// 同じアプリでも区間が違えば別々に残す。
	result := runNormalize(t, []*rawlog.Event{
		androidEvent(t, "a1", rawlog.EventAppUsage, 0, durationPtr(10*min),
			rawlog.AppUsagePayload{AppLabel: "Chrome"}),
		androidEvent(t, "a2", rawlog.EventAppUsage, 20*min, durationPtr(30*min),
			rawlog.AppUsagePayload{AppLabel: "Chrome"}),
	}, 60*min)

	if got := len(proposalsBySource(result, SourceWindow)); got != 2 {
		t.Errorf("件数 = %d, want 2", got)
	}
}

// ---------------------------------------------------------------- 全体

func TestDevicesAreProcessedIndependently(t *testing.T) {
	// Windows と Android の履歴は統合しない（要件 §8.2）。
	result := runNormalize(t, []*rawlog.Event{
		event(t, "w1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		androidEvent(t, "a1", rawlog.EventSession, 5*min, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		event(t, "w2", rawlog.EventSession, 30*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLock}),
		androidEvent(t, "a2", rawlog.EventSession, 40*min, nil, rawlog.SessionPayload{Action: rawlog.SessionScreenOff}),
	}, 60*min)

	usage := proposalsBySource(result, SourceDevice)
	if len(usage) != 2 {
		t.Fatalf("件数 = %d, want 2 (%+v)", len(usage), usage)
	}
	byDevice := map[rawlog.Device]Proposal{}
	for _, p := range usage {
		byDevice[p.Device] = p
	}
	if byDevice[rawlog.Device("Laptop")].EndTime.Sub(base()) != 30*min {
		t.Error("Laptop の区間が別端末のイベントに影響されている")
	}
	if byDevice[rawlog.Device("Phone")].StartTime.Sub(base()) != 5*min {
		t.Error("Phone の区間が別端末のイベントに影響されている")
	}
}

func TestEventsAtOrAfterCutoffAreIgnored(t *testing.T) {
	result := runNormalize(t, []*rawlog.Event{
		event(t, "s1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		event(t, "s2", rawlog.EventSession, 30*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLock}),
		// Cutoff 以降は次回へ回す。
		event(t, "s3", rawlog.EventSession, 60*min, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
	}, 60*min)

	if got := len(proposalsBySource(result, SourceDevice)); got != 1 {
		t.Errorf("件数 = %d, want 1", got)
	}
	if cursor := result.SafeCursor.Sub(base()); cursor != 60*min {
		t.Errorf("SafeCursor = %v, want %v", cursor, 60*min)
	}
}

func TestProposalIDIsDeterministic(t *testing.T) {
	// 同じ生ログから何度 normalize しても同じIDになること。
	// バッチが途中で失敗して再実行しても二重登録にならないための前提。
	events := []*rawlog.Event{
		event(t, "s1", rawlog.EventSession, 0, nil, rawlog.SessionPayload{Action: rawlog.SessionUnlock}),
		event(t, "s2", rawlog.EventSession, 30*min, nil, rawlog.SessionPayload{Action: rawlog.SessionLock}),
		browserEvent(t, "v1", 5*min, 2*min, "https://example.com/"),
	}

	first := runNormalize(t, events, 60*min)
	second := runNormalize(t, events, 60*min)

	if len(first.Proposals) != len(second.Proposals) {
		t.Fatalf("件数が違う: %d != %d", len(first.Proposals), len(second.Proposals))
	}
	for i := range first.Proposals {
		if first.Proposals[i].ID != second.Proposals[i].ID {
			t.Errorf("[%d] ID が違う: %s != %s", i, first.Proposals[i].ID, second.Proposals[i].ID)
		}
		if first.Proposals[i].ID == "" {
			t.Errorf("[%d] ID が空", i)
		}
	}
}
