package normalize

import (
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// interval は windowSessions の結果を検証しやすい形に落とす。
type interval struct {
	title string
	start time.Duration
	end   time.Duration
}

// runWindow は base からの相対秒で与えた入力から TimeIs 区間を作る。
func runWindow(t *testing.T, inputs []inputAt, cutoff time.Duration) ([]interval, time.Duration) {
	t.Helper()

	events := make([]*rawlog.Event, 0, len(inputs))
	for i, in := range inputs {
		events = append(events, event(t, eventID(i), rawlog.EventInput, in.at, nil,
			rawlog.InputPayload{AppDisplayName: in.display, AppName: in.app, WindowTitle: in.title}))
	}

	result, err := Run(events, Options{Cutoff: base().Add(cutoff)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var intervals []interval
	for _, p := range result.Proposals {
		if p.Source != SourceWindow {
			continue
		}
		intervals = append(intervals, interval{
			title: p.Title,
			start: p.StartTime.Sub(base()),
			end:   p.EndTime.Sub(base()),
		})
	}
	return intervals, result.SafeCursor.Sub(base())
}

type inputAt struct {
	at time.Duration
	// display は実行ファイルの説明。空なら app へフォールバックする。
	display string
	app     string
	title   string
}

const (
	sec = time.Second
	min = time.Minute
)

func TestWindowSessionSplitsOnIdleTimeout(t *testing.T) {
	tests := []struct {
		name string
		gap  time.Duration
		want int
	}{
		// 5分間入力が無ければアイドル終了（要件 §6.3）。
		{name: "4分59秒は継続", gap: 5*min - sec, want: 1},
		{name: "5分ちょうどで分割", gap: 5 * min, want: 2},
		{name: "5分1秒で分割", gap: 5*min + sec, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 各区間が最小時間(1分)を超えるよう、区間ごとに90秒ぶんの入力を置く。
			// アイドル判定は「最後の入力からの間隔」で見る。
			const title = "main.go - gkill - Visual Studio Code"
			intervals, _ := runWindow(t, []inputAt{
				{at: 0, app: "Code", title: title},
				{at: 90 * sec, app: "Code", title: title},
				{at: 90*sec + tt.gap, app: "Code", title: title},
				{at: 90*sec + tt.gap + 90*sec, app: "Code", title: title},
			}, tt.gap+30*min)

			if len(intervals) != tt.want {
				t.Fatalf("区間数 = %d, want %d (%+v)", len(intervals), tt.want, intervals)
			}
		})
	}
}

func TestWindowSessionEndsAtLastInputNotIdleDetection(t *testing.T) {
	// 終了時刻はアイドル判定時刻ではなく、最後の入力時刻（要件 §6.3）。
	intervals, _ := runWindow(t, []inputAt{
		{at: 0, app: "Code", title: "A"},
		{at: 2 * min, app: "Code", title: "A"},
		{at: 30 * min, app: "Code", title: "A"},
		{at: 31 * min, app: "Code", title: "A"},
	}, 60*min)

	if len(intervals) != 2 {
		t.Fatalf("区間数 = %d, want 2 (%+v)", len(intervals), intervals)
	}
	if intervals[0].end != 2*min {
		t.Errorf("終了時刻 = %v, want %v (最後の入力時刻であるべき)", intervals[0].end, 2*min)
	}
}

func TestWindowSessionMergesShortInterruption(t *testing.T) {
	// A を70秒操作 → B へ割り込み → A へ戻って70秒操作、という流れ。
	// 各区間は最小時間(1分)を超えるようにしてある。
	tests := []struct {
		name          string
		returnAt      time.Duration
		wantIntervals []interval
	}{
		{
			// 1分以内に元のアプリへ戻ったら前後を結合し、割り込みは TimeIs にしない。
			name:          "1分ちょうどで戻れば結合",
			returnAt:      70*sec + 1*min,
			wantIntervals: []interval{{title: "Code", start: 0, end: 70*sec + 1*min + 70*sec}},
		},
		{
			name:     "1分を超えたら結合しない",
			returnAt: 70*sec + 1*min + sec,
			wantIntervals: []interval{
				{title: "Code", start: 0, end: 70 * sec},
				{title: "Code", start: 70*sec + 1*min + sec, end: 70*sec + 1*min + sec + 70*sec},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intervals, _ := runWindow(t, []inputAt{
				{at: 0, app: "Code", title: "A"},
				{at: 70 * sec, app: "Code", title: "A"},
				// 割り込みは一瞬だけ。1分未満なので単独では TimeIs にならない。
				{at: 80 * sec, app: "chrome", title: "B"},
				{at: tt.returnAt, app: "Code", title: "A"},
				{at: tt.returnAt + 70*sec, app: "Code", title: "A"},
			}, 60*min)

			if len(intervals) != len(tt.wantIntervals) {
				t.Fatalf("区間数 = %d, want %d (%+v)", len(intervals), len(tt.wantIntervals), intervals)
			}
			for i, want := range tt.wantIntervals {
				if intervals[i] != want {
					t.Errorf("[%d] = %+v, want %+v", i, intervals[i], want)
				}
			}
		})
	}
}

func TestWindowSessionMergesConsecutiveInterruptions(t *testing.T) {
	// 割り込みが2つ続いても、元のアプリへ1分以内に戻っていれば結合する。
	intervals, _ := runWindow(t, []inputAt{
		{at: 0, app: "Code", title: "A"},
		{at: 70 * sec, app: "Code", title: "A"},
		{at: 80 * sec, app: "chrome", title: "B"},
		{at: 90 * sec, app: "explorer", title: "C"},
		{at: 100 * sec, app: "Code", title: "A"},
		{at: 170 * sec, app: "Code", title: "A"},
	}, 60*min)

	if len(intervals) != 1 {
		t.Fatalf("区間数 = %d, want 1 (%+v)", len(intervals), intervals)
	}
	if intervals[0].title != "Code" || intervals[0].start != 0 || intervals[0].end != 170*sec {
		t.Errorf("区間 = %+v, want Code 0..170s", intervals[0])
	}
}

func TestWindowSessionSpansMidnight(t *testing.T) {
	// 日付をまたいでも連続していれば1件（要件 §6.3）。
	// base は 10:00 なので、14 時間後が翌日 0:00。
	intervals, _ := runWindow(t, []inputAt{
		{at: 14*time.Hour - 2*min, app: "Code", title: "A"},
		{at: 14 * time.Hour, app: "Code", title: "A"},
		{at: 14*time.Hour + 2*min, app: "Code", title: "A"},
	}, 20*time.Hour)

	if len(intervals) != 1 {
		t.Fatalf("区間数 = %d, want 1 (%+v)", len(intervals), intervals)
	}
	if intervals[0].start != 14*time.Hour-2*min || intervals[0].end != 14*time.Hour+2*min {
		t.Errorf("区間 = %+v, want 日付をまたいで1件", intervals[0])
	}
}

func TestWindowSessionInProgressIsDeferred(t *testing.T) {
	// Cutoff 時点で継続中のセッションは処理せず、次回バッチへ回す（要件 §6.3）。
	// カーソルも継続中セッションの開始時刻より先へ進めてはならない（要件 §17）。
	intervals, cursor := runWindow(t, []inputAt{
		{at: 0, app: "Code", title: "A"},
		{at: 1 * min, app: "Code", title: "A"},
		{at: 50 * min, app: "chrome", title: "B"},
		{at: 52 * min, app: "chrome", title: "B"},
	}, 55*min)

	if len(intervals) != 1 {
		t.Fatalf("区間数 = %d, want 1 (継続中のものは出さない) (%+v)", len(intervals), intervals)
	}
	if intervals[0].title != "Code" {
		t.Errorf("出力された区間 = %q, want Code", intervals[0].title)
	}
	if cursor != 50*min {
		t.Errorf("SafeCursor = %v, want %v (継続中セッションの開始時刻)", cursor, 50*min)
	}
}

func TestWindowSessionFinishedSessionAdvancesCursor(t *testing.T) {
	// 最後のセッションが十分前に終わっていれば、カーソルは Cutoff まで進む。
	_, cursor := runWindow(t, []inputAt{
		{at: 0, app: "Code", title: "A"},
		{at: 1 * min, app: "Code", title: "A"},
	}, 60*min)

	if cursor != 60*min {
		t.Errorf("SafeCursor = %v, want %v", cursor, 60*min)
	}
}

func TestWindowSessionMinimumDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     int
	}{
		// 一瞬触っただけのウィンドウは記録しない。
		{name: "0秒(1回だけ操作)は除外", duration: 0, want: 0},
		{name: "59秒は除外", duration: 59 * sec, want: 0},
		{name: "1分ちょうどは記録", duration: 1 * min, want: 1},
		{name: "2分は記録", duration: 2 * min, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputs := []inputAt{{at: 0, app: "Code", title: "A"}}
			if tt.duration > 0 {
				inputs = append(inputs, inputAt{at: tt.duration, app: "Code", title: "A"})
			}

			intervals, _ := runWindow(t, inputs, 60*min)
			if len(intervals) != tt.want {
				t.Errorf("区間数 = %d, want %d (%+v)", len(intervals), tt.want, intervals)
			}
		})
	}
}

func TestWindowSessionMinimumIsCheckedAfterMerging(t *testing.T) {
	// 短い区間どうしでも、割り込みをはさんで結合された結果が1分以上なら記録する。
	// 40秒操作 → 10秒だけ別ウィンドウ → 戻って合計70秒。
	intervals, _ := runWindow(t, []inputAt{
		{at: 0, app: "Code", title: "A"},
		{at: 40 * sec, app: "Code", title: "A"},
		{at: 50 * sec, app: "chrome", title: "B"},
		{at: 70 * sec, app: "Code", title: "A"},
	}, 60*min)

	if len(intervals) != 1 {
		t.Fatalf("区間数 = %d, want 1 (%+v)", len(intervals), intervals)
	}
	if intervals[0].title != "Code" || intervals[0].end-intervals[0].start != 70*sec {
		t.Errorf("区間 = %+v, want Code の 0..70s", intervals[0])
	}
}

func TestWindowSessionUsesAppNameOnly(t *testing.T) {
	// タイトルには表示上のアプリ名だけを使う（要件 §6.4）。
	// 開いているファイル名やページ名を混ぜない。
	tests := []struct {
		name    string
		display string
		app     string
		title   string
		want    string
	}{
		{
			name:    "実行ファイルの説明があればそれを使う",
			display: "Visual Studio Code",
			app:     "Code",
			title:   "main.go - gkill - Visual Studio Code",
			want:    "Visual Studio Code",
		},
		{
			name:  "説明が取れなければ実行ファイル名",
			app:   "chrome",
			title: "ホーム / X - Google Chrome",
			want:  "chrome",
		},
		{
			// アプリ名がまったく取れないのは権限不足などの例外的な場合。
			// タイトルを空にしないための保険としてウィンドウタイトルを使う。
			name:  "アプリ名が無ければウィンドウタイトル",
			title: "無題 - メモ帳",
			want:  "無題 - メモ帳",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intervals, _ := runWindow(t, []inputAt{
				{at: 0, display: tt.display, app: tt.app, title: tt.title},
				{at: 1 * min, display: tt.display, app: tt.app, title: tt.title},
			}, 60*min)

			if len(intervals) != 1 || intervals[0].title != tt.want {
				t.Errorf("タイトル = %+v, want %q", intervals, tt.want)
			}
		})
	}
}

func TestWindowSessionIgnoresTitleChangesWithinSameApp(t *testing.T) {
	// 同じアプリを触り続けている間は、タブやファイルを切り替えても区間を切らない。
	// 切ってしまうと同じタイトルの TimeIs が延々と並び、
	// 1分未満の断片は MinWindowDuration で落ちて間に穴が空く。
	intervals, _ := runWindow(t, []inputAt{
		{at: 0, display: "Google Chrome", app: "chrome", title: "ホーム / X - Google Chrome"},
		{at: 30 * sec, display: "Google Chrome", app: "chrome", title: "YouTube - Google Chrome"},
		{at: 60 * sec, display: "Google Chrome", app: "chrome", title: "新しいタブ - Google Chrome"},
		{at: 90 * sec, display: "Google Chrome", app: "chrome", title: "gkill"},
	}, 60*min)

	if len(intervals) != 1 {
		t.Fatalf("区間数 = %d, want 1 (%+v)", len(intervals), intervals)
	}
	want := interval{title: "Google Chrome", start: 0, end: 90 * sec}
	if intervals[0] != want {
		t.Errorf("区間 = %+v, want %+v", intervals[0], want)
	}
}
