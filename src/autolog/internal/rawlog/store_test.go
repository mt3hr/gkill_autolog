package rawlog

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mustEvent(t *testing.T, id string, device Device, eventType EventType, start time.Time, end *time.Time, payload any) *Event {
	t.Helper()
	e, err := NewEvent(id, device, eventType, start, end, payload)
	if err != nil {
		t.Fatalf("NewEvent(%s): %v", id, err)
	}
	return e
}

func baseTime() time.Time {
	return time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
}

func TestPutIsIdempotentPerDeviceAndEventID(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := baseTime()

	e := mustEvent(t, "ev-1", Device("Laptop"), EventInput, now, nil,
		InputPayload{AppName: "chrome", WindowTitle: "gkill MCP Server - Google Chrome"})

	inserted, err := store.Put(ctx, e)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !inserted {
		t.Fatal("最初の Put が false を返した")
	}

	// 同じ event_id を再投入しても増えないこと（Android・Chrome拡張の再送を想定）。
	inserted, err = store.Put(ctx, e)
	if err != nil {
		t.Fatalf("Put(2回目): %v", err)
	}
	if inserted {
		t.Error("同一 (device, event_id) の再投入で true が返った")
	}

	// 端末が違えば別イベントとして入ること。
	other := mustEvent(t, "ev-1", Device("Phone"), EventInput, now, nil,
		InputPayload{AppName: "chrome", WindowTitle: "同じIDだが別端末"})
	inserted, err = store.Put(ctx, other)
	if err != nil {
		t.Fatalf("Put(別端末): %v", err)
	}
	if !inserted {
		t.Error("端末が異なるのに重複扱いされた")
	}

	events, err := store.Range(ctx, RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("件数 = %d, want 2", len(events))
	}
}

func TestPutBatchRejectsInvalidEventWithoutPartialWrite(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := baseTime()

	valid := mustEvent(t, "ok-1", Device("Laptop"), EventPower, now, nil, PowerPayload{Charging: true})
	invalid := mustEvent(t, "", Device("Laptop"), EventPower, now, nil, PowerPayload{Charging: false})

	if _, err := store.PutBatch(ctx, []*Event{valid, invalid}); err == nil {
		t.Fatal("不正なイベントを含む PutBatch がエラーを返さなかった")
	}

	events, err := store.Range(ctx, RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("検証に失敗したのに %d 件書き込まれた", len(events))
	}
}

func TestRangeFiltersAndOrders(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := baseTime()

	end := now.Add(30 * time.Minute)
	events := []*Event{
		mustEvent(t, "b-later", Device("Laptop"), EventInput, now.Add(2*time.Hour), nil,
			InputPayload{AppName: "Code", WindowTitle: "main.go - gkill - Visual Studio Code"}),
		mustEvent(t, "a-earlier", Device("Laptop"), EventBrowserView, now, &end,
			BrowserViewPayload{URL: "https://example.com/", Title: "Example", BrowserFocused: true}),
		mustEvent(t, "c-android", Device("Phone"), EventNotification, now.Add(time.Hour), nil,
			NotificationPayload{AppLabel: "Gmail", Title: "件名", Body: "本文"}),
	}
	if _, err := store.PutBatch(ctx, events); err != nil {
		t.Fatalf("PutBatch: %v", err)
	}

	t.Run("時刻順に返る", func(t *testing.T) {
		got, err := store.Range(ctx, RangeQuery{})
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		want := []string{"a-earlier", "c-android", "b-later"}
		if len(got) != len(want) {
			t.Fatalf("件数 = %d, want %d", len(got), len(want))
		}
		for i, id := range want {
			if got[i].EventID != id {
				t.Errorf("[%d] = %s, want %s", i, got[i].EventID, id)
			}
		}
	})

	t.Run("端末で絞れる", func(t *testing.T) {
		got, err := store.Range(ctx, RangeQuery{Devices: []Device{Device("Phone")}})
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		if len(got) != 1 || got[0].EventID != "c-android" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("種別で絞れる", func(t *testing.T) {
		got, err := store.Range(ctx, RangeQuery{Types: []EventType{EventInput, EventBrowserView}})
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("件数 = %d, want 2", len(got))
		}
	})

	t.Run("Toは境界を含まない", func(t *testing.T) {
		got, err := store.Range(ctx, RangeQuery{From: now, To: now.Add(time.Hour)})
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		if len(got) != 1 || got[0].EventID != "a-earlier" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestRangeRestoresEndTimeAndPayload(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := baseTime()
	end := now.Add(45 * time.Second)

	want := BrowserViewPayload{
		URL:            "https://www.youtube.com/watch?v=abc123",
		Title:          "タイトル",
		TabID:          7,
		WindowID:       2,
		BrowserFocused: true,
		Source:         "chrome_extension",
	}
	if _, err := store.Put(ctx, mustEvent(t, "view-1", Device("Laptop"), EventBrowserView, now, &end, want)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Range(ctx, RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("件数 = %d, want 1", len(got))
	}
	if got[0].EndTime == nil {
		t.Fatal("end_time が復元されなかった")
	}
	if !got[0].EndTime.Equal(end) {
		t.Errorf("end_time = %s, want %s", got[0].EndTime, end)
	}

	payload, err := DecodePayload[BrowserViewPayload](got[0])
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload != want {
		t.Errorf("payload = %+v, want %+v", payload, want)
	}
}

func TestNilEndTimeStaysNil(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, err := store.Put(ctx, mustEvent(t, "in-1", Device("Laptop"), EventInput, baseTime(), nil,
		InputPayload{AppName: "chrome", WindowTitle: "t"})); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Range(ctx, RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if got[0].EndTime != nil {
		t.Errorf("end_time = %v, want nil", got[0].EndTime)
	}
}

func TestLastEventTime(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := baseTime()

	_, ok, err := store.LastEventTime(ctx, Device("Laptop"), EventInput)
	if err != nil {
		t.Fatalf("LastEventTime: %v", err)
	}
	if ok {
		t.Error("空のストアで ok = true が返った")
	}

	for i, offset := range []time.Duration{0, 5 * time.Minute, 2 * time.Minute} {
		e := mustEvent(t, string(rune('a'+i)), Device("Laptop"), EventInput, now.Add(offset), nil,
			InputPayload{AppName: "chrome", WindowTitle: "t"})
		if _, err := store.Put(ctx, e); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	got, ok, err := store.LastEventTime(ctx, Device("Laptop"), EventInput)
	if err != nil {
		t.Fatalf("LastEventTime: %v", err)
	}
	if !ok {
		t.Fatal("ok = false")
	}
	// 投入順ではなく start_time の最大を返すこと。
	if want := now.Add(5 * time.Minute); !got.Equal(want) {
		t.Errorf("LastEventTime = %s, want %s", got, want)
	}
}

// Chrome 拡張と Android は UTC (末尾 Z) で送ってくることがある。
// 保存時にローカル時刻へ揃えないと、文字列比較の ORDER BY が時刻順にならない。
func TestEventsFromDifferentOffsetsAreOrderedCorrectly(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	jst := time.FixedZone("JST", 9*60*60)
	// 同じ瞬間を別のオフセットで表したもの。UTC 側が 1 分だけ後。
	local := time.Date(2026, 7, 25, 21, 0, 0, 0, jst)
	utc := local.Add(time.Minute).UTC()

	events := []*Event{
		mustEvent(t, "utc-later", Device("Laptop"), EventBrowserView, utc, nil,
			BrowserViewPayload{URL: "https://example.com/later", Title: "後"}),
		mustEvent(t, "local-earlier", Device("Laptop"), EventInput, local, nil,
			InputPayload{AppName: "chrome", WindowTitle: "先"}),
	}
	if _, err := store.PutBatch(ctx, events); err != nil {
		t.Fatalf("PutBatch: %v", err)
	}

	got, err := store.Range(ctx, RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	want := []string{"local-earlier", "utc-later"}
	for i, id := range want {
		if got[i].EventID != id {
			t.Errorf("[%d] = %s, want %s (オフセット違いで順序が壊れている)", i, got[i].EventID, id)
		}
	}

	// 範囲指定もオフセットをまたいで正しく効くこと。
	inRange, err := store.Range(ctx, RangeQuery{From: local.Add(30 * time.Second).UTC()})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(inRange) != 1 || inRange[0].EventID != "utc-later" {
		t.Errorf("From で絞った結果 = %+v, want utc-later のみ", inRange)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, ok, err := store.GetCursor(ctx, CursorNormalize); err != nil {
		t.Fatalf("GetCursor: %v", err)
	} else if ok {
		t.Error("未設定のカーソルで ok = true が返った")
	}

	first := baseTime()
	if err := store.SetCursor(ctx, CursorNormalize, first); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	got, ok, err := store.GetCursor(ctx, CursorNormalize)
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if !ok || !got.Equal(first) {
		t.Fatalf("GetCursor = %s (ok=%v), want %s", got, ok, first)
	}

	second := first.Add(24 * time.Hour)
	if err := store.SetCursor(ctx, CursorNormalize, second); err != nil {
		t.Fatalf("SetCursor(上書き): %v", err)
	}
	got, _, err = store.GetCursor(ctx, CursorNormalize)
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if !got.Equal(second) {
		t.Errorf("上書き後 = %s, want %s", got, second)
	}
}

func TestOpenStateIntervalRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if opens, err := store.LoadOpenStates(ctx); err != nil {
		t.Fatalf("LoadOpenStates: %v", err)
	} else if len(opens) != 0 {
		t.Errorf("初期状態の件数 = %d, want 0", len(opens))
	}

	watch := OpenStateInterval{
		Device:   "Phone",
		Source:   "bluetooth",
		Key:      "Watch",
		Title:    "Bluetooth Watch",
		Start:    baseTime(),
		EventIDs: []string{"b1", "b2"},
	}
	wifi := OpenStateInterval{
		Device:   "Phone",
		Source:   "wifi",
		Key:      "TestWifi",
		Title:    "Wi-Fi TestWifi",
		Start:    baseTime().Add(time.Hour),
		EventIDs: []string{"w1"},
	}
	if err := store.SaveOpenStates(ctx, []OpenStateInterval{watch, wifi}); err != nil {
		t.Fatalf("SaveOpenStates: %v", err)
	}

	opens, err := store.LoadOpenStates(ctx)
	if err != nil {
		t.Fatalf("LoadOpenStates: %v", err)
	}
	if len(opens) != 2 {
		t.Fatalf("件数 = %d, want 2 (%+v)", len(opens), opens)
	}
	// device, source, state_key 順に返る。
	if got := opens[0]; got.Key != watch.Key || !got.Start.Equal(watch.Start) ||
		len(got.EventIDs) != 2 || got.EventIDs[1] != "b2" || got.Title != watch.Title {
		t.Errorf("1件目 = %+v, want %+v", got, watch)
	}

	// 閉じた区間は渡さない。保存は入れ替えなので消える。
	if err := store.SaveOpenStates(ctx, []OpenStateInterval{wifi}); err != nil {
		t.Fatalf("SaveOpenStates(2回目): %v", err)
	}
	opens, err = store.LoadOpenStates(ctx)
	if err != nil {
		t.Fatalf("LoadOpenStates(2回目): %v", err)
	}
	if len(opens) != 1 || opens[0].Key != wifi.Key {
		t.Fatalf("入れ替え後 = %+v, want TestWifi のみ", opens)
	}
}

func TestPutBatchMarksBackfillForLateEvents(t *testing.T) {
	// 区間イベントは終わってから届くので、取り込みがカーソルを進めた後に
	// カーソルより過去の start_time で入ることがある。
	// そのとき低水位マークが残り、import が読み直せること。
	store := openTestStore(t)
	ctx := context.Background()
	cursor := baseTime()

	if err := store.SetCursor(ctx, CursorNormalize, cursor); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}

	// カーソルより未来のイベントはマークを作らない。
	future := mustEvent(t, "future-1", Device("Phone"), EventAppUsage,
		cursor.Add(time.Hour), timePtrOf(cursor.Add(2*time.Hour)), AppUsagePayload{AppLabel: "Chrome"})
	if _, err := store.PutBatch(ctx, []*Event{future}); err != nil {
		t.Fatalf("PutBatch(未来): %v", err)
	}
	if _, ok, err := store.GetCursor(ctx, CursorBackfill); err != nil || ok {
		t.Fatalf("未来のイベントでマークができた (ok=%v, err=%v)", ok, err)
	}

	// カーソルより過去のイベントが入るとマークができる。
	late := mustEvent(t, "late-1", Device("Phone"), EventAppUsage,
		cursor.Add(-30*time.Minute), timePtrOf(cursor.Add(time.Hour)), AppUsagePayload{AppLabel: "YouTube Music"})
	if _, err := store.PutBatch(ctx, []*Event{late}); err != nil {
		t.Fatalf("PutBatch(遅着): %v", err)
	}
	mark, ok, err := store.GetCursor(ctx, CursorBackfill)
	if err != nil {
		t.Fatalf("GetCursor(backfill): %v", err)
	}
	if !ok {
		t.Fatal("遅着イベントを入れたのにマークが無い")
	}
	if !mark.Equal(cursor.Add(-30 * time.Minute)) {
		t.Errorf("マーク = %s, want %s", mark, cursor.Add(-30*time.Minute))
	}

	// さらに過去のイベントが入るとマークが下がる。
	older := mustEvent(t, "late-2", Device("Phone"), EventNotification,
		cursor.Add(-2*time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "件名"})
	if _, err := store.PutBatch(ctx, []*Event{older}); err != nil {
		t.Fatalf("PutBatch(さらに過去): %v", err)
	}
	mark, _, err = store.GetCursor(ctx, CursorBackfill)
	if err != nil {
		t.Fatalf("GetCursor(backfill): %v", err)
	}
	if !mark.Equal(cursor.Add(-2 * time.Hour)) {
		t.Errorf("マーク = %s, want %s (より過去へ下がるべき)", mark, cursor.Add(-2*time.Hour))
	}

	// 既存マークより新しい遅着ではマークが動かない。
	between := mustEvent(t, "late-3", Device("Phone"), EventNotification,
		cursor.Add(-time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "別の件名"})
	if _, err := store.PutBatch(ctx, []*Event{between}); err != nil {
		t.Fatalf("PutBatch(マークとカーソルの間): %v", err)
	}
	mark, _, err = store.GetCursor(ctx, CursorBackfill)
	if err != nil {
		t.Fatalf("GetCursor(backfill): %v", err)
	}
	if !mark.Equal(cursor.Add(-2 * time.Hour)) {
		t.Errorf("マーク = %s, want %s (新しい遅着で上がってはならない)", mark, cursor.Add(-2*time.Hour))
	}
}

func TestPutBatchDoesNotMarkBackfillForDuplicates(t *testing.T) {
	// 再送 (実際には挿入されない) ではマークを作らない。
	// 作ってしまうと、Android の再送のたびに import が過去を読み直すことになる。
	store := openTestStore(t)
	ctx := context.Background()
	cursor := baseTime()

	late := mustEvent(t, "late-1", Device("Phone"), EventAppUsage,
		cursor.Add(-time.Hour), timePtrOf(cursor.Add(-30*time.Minute)), AppUsagePayload{AppLabel: "Chrome"})
	if _, err := store.PutBatch(ctx, []*Event{late}); err != nil {
		t.Fatalf("PutBatch(1回目): %v", err)
	}

	if err := store.SetCursor(ctx, CursorNormalize, cursor); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}

	if _, err := store.PutBatch(ctx, []*Event{late}); err != nil {
		t.Fatalf("PutBatch(再送): %v", err)
	}
	if _, ok, err := store.GetCursor(ctx, CursorBackfill); err != nil || ok {
		t.Fatalf("再送でマークができた (ok=%v, err=%v)", ok, err)
	}
}

func TestPutBatchWithoutCursorDoesNotMarkBackfill(t *testing.T) {
	// 一度も取り込んでいなければマークは不要 (初回は7日前から読む)。
	store := openTestStore(t)
	ctx := context.Background()

	e := mustEvent(t, "ev-1", Device("Phone"), EventNotification,
		baseTime(), nil, NotificationPayload{AppLabel: "Gmail", Title: "件名"})
	if _, err := store.PutBatch(ctx, []*Event{e}); err != nil {
		t.Fatalf("PutBatch: %v", err)
	}
	if _, ok, err := store.GetCursor(ctx, CursorBackfill); err != nil || ok {
		t.Fatalf("カーソルが無いのにマークができた (ok=%v, err=%v)", ok, err)
	}
}

func TestClearBackfillMark(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	cursor := baseTime()

	if err := store.SetCursor(ctx, CursorNormalize, cursor); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	late := mustEvent(t, "late-1", Device("Phone"), EventNotification,
		cursor.Add(-time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "件名"})
	if _, err := store.PutBatch(ctx, []*Event{late}); err != nil {
		t.Fatalf("PutBatch: %v", err)
	}
	mark, _, err := store.GetCursor(ctx, CursorBackfill)
	if err != nil {
		t.Fatalf("GetCursor(backfill): %v", err)
	}

	// 値が違えば消えない。取り込み中に新しい遅着が来た場合、次回へ引き継ぐため。
	if err := store.ClearBackfillMark(ctx, mark.Add(time.Minute)); err != nil {
		t.Fatalf("ClearBackfillMark(不一致): %v", err)
	}
	if _, ok, _ := store.GetCursor(ctx, CursorBackfill); !ok {
		t.Fatal("値が一致しないのにマークが消えた")
	}

	// 値が一致すれば消える。
	if err := store.ClearBackfillMark(ctx, mark); err != nil {
		t.Fatalf("ClearBackfillMark: %v", err)
	}
	if _, ok, _ := store.GetCursor(ctx, CursorBackfill); ok {
		t.Fatal("マークが消えていない")
	}
}

func TestAdvanceCursorCheckedMarksInsertionsDuringImport(t *testing.T) {
	// import は Range で読んでから書き終えるまでに数分かかる。その間に
	// 挿入されたイベントのうち、start_time が旧カーソルと新カーソルの間の
	// ものは挿入時の markBackfill の対象にならない (旧カーソルより未来のため)。
	// カーソルの前進時に検査して低水位マークを残し、次回読み直せること。
	ctx := context.Background()
	oldCursor := baseTime()
	newCursor := oldCursor.Add(4 * time.Hour)

	t.Run("取り込み中の挿入がマークになる", func(t *testing.T) {
		store := openTestStore(t)
		if err := store.SetCursor(ctx, CursorNormalize, oldCursor); err != nil {
			t.Fatalf("SetCursor: %v", err)
		}

		// import が読む範囲を確定した (Range を呼んだ) 時点の印。
		ranged, err := store.MaxRowID(ctx)
		if err != nil {
			t.Fatalf("MaxRowID: %v", err)
		}

		// 書き込み中に、旧カーソルと新カーソルの間のイベントが届く。
		// 旧カーソルより未来なので挿入時のマークは作られない (前提の確認)。
		during := mustEvent(t, "during-1", Device("Phone"), EventAppUsage,
			oldCursor.Add(time.Hour), timePtrOf(oldCursor.Add(2*time.Hour)), AppUsagePayload{AppLabel: "Chrome"})
		if _, err := store.PutBatch(ctx, []*Event{during}); err != nil {
			t.Fatalf("PutBatch: %v", err)
		}
		if _, ok, err := store.GetCursor(ctx, CursorBackfill); err != nil || ok {
			t.Fatalf("挿入時にマークができている (ok=%v, err=%v)", ok, err)
		}

		if err := store.AdvanceCursorChecked(ctx, CursorNormalize, newCursor, ranged, time.Time{}); err != nil {
			t.Fatalf("AdvanceCursorChecked: %v", err)
		}

		cursor, _, err := store.GetCursor(ctx, CursorNormalize)
		if err != nil {
			t.Fatalf("GetCursor: %v", err)
		}
		if !cursor.Equal(newCursor) {
			t.Errorf("カーソル = %s, want %s", cursor, newCursor)
		}
		mark, ok, err := store.GetCursor(ctx, CursorBackfill)
		if err != nil {
			t.Fatalf("GetCursor(backfill): %v", err)
		}
		if !ok {
			t.Fatal("取り込み中に挿入されたイベントがマークにならず、恒久に読まれなくなる")
		}
		if !mark.Equal(oldCursor.Add(time.Hour)) {
			t.Errorf("マーク = %s, want %s", mark, oldCursor.Add(time.Hour))
		}
	})

	t.Run("消費したマークを消しつつ新しい遅着を残す", func(t *testing.T) {
		store := openTestStore(t)
		if err := store.SetCursor(ctx, CursorNormalize, oldCursor); err != nil {
			t.Fatalf("SetCursor: %v", err)
		}
		// 前回の取り込み後に届いていた遅着。今回の import はこのマークまで遡って読む。
		consumed := mustEvent(t, "late-consumed", Device("Phone"), EventNotification,
			oldCursor.Add(-time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "件名"})
		if _, err := store.PutBatch(ctx, []*Event{consumed}); err != nil {
			t.Fatalf("PutBatch(事前の遅着): %v", err)
		}
		usedMark, _, err := store.GetCursor(ctx, CursorBackfill)
		if err != nil {
			t.Fatalf("GetCursor(backfill): %v", err)
		}

		ranged, err := store.MaxRowID(ctx)
		if err != nil {
			t.Fatalf("MaxRowID: %v", err)
		}
		// 書き込み中の挿入。マークの値は動かない (旧カーソルより未来のため)。
		during := mustEvent(t, "during-2", Device("Phone"), EventAppUsage,
			oldCursor.Add(2*time.Hour), timePtrOf(oldCursor.Add(3*time.Hour)), AppUsagePayload{AppLabel: "YouTube"})
		if _, err := store.PutBatch(ctx, []*Event{during}); err != nil {
			t.Fatalf("PutBatch(取り込み中): %v", err)
		}

		if err := store.AdvanceCursorChecked(ctx, CursorNormalize, newCursor, ranged, usedMark); err != nil {
			t.Fatalf("AdvanceCursorChecked: %v", err)
		}

		// 消費した分は消え、取り込み中の挿入だけが新しいマークとして残る。
		// 消費したマークをそのまま消すだけだと、取り込み中の挿入まで一緒に忘れられる。
		mark, ok, err := store.GetCursor(ctx, CursorBackfill)
		if err != nil {
			t.Fatalf("GetCursor(backfill): %v", err)
		}
		if !ok {
			t.Fatal("取り込み中の挿入がマークに残っていない")
		}
		if !mark.Equal(oldCursor.Add(2 * time.Hour)) {
			t.Errorf("マーク = %s, want %s", mark, oldCursor.Add(2*time.Hour))
		}
	})

	t.Run("取り込み中にさらに下がったマークは消さない", func(t *testing.T) {
		store := openTestStore(t)
		if err := store.SetCursor(ctx, CursorNormalize, oldCursor); err != nil {
			t.Fatalf("SetCursor: %v", err)
		}
		consumed := mustEvent(t, "late-consumed", Device("Phone"), EventNotification,
			oldCursor.Add(-time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "件名"})
		if _, err := store.PutBatch(ctx, []*Event{consumed}); err != nil {
			t.Fatalf("PutBatch(事前の遅着): %v", err)
		}
		usedMark, _, err := store.GetCursor(ctx, CursorBackfill)
		if err != nil {
			t.Fatalf("GetCursor(backfill): %v", err)
		}

		ranged, err := store.MaxRowID(ctx)
		if err != nil {
			t.Fatalf("MaxRowID: %v", err)
		}
		// 取り込み中に、さらに過去の遅着が届いてマークが下がる。
		older := mustEvent(t, "late-older", Device("Phone"), EventNotification,
			oldCursor.Add(-2*time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "別の件名"})
		if _, err := store.PutBatch(ctx, []*Event{older}); err != nil {
			t.Fatalf("PutBatch(さらに過去): %v", err)
		}

		if err := store.AdvanceCursorChecked(ctx, CursorNormalize, newCursor, ranged, usedMark); err != nil {
			t.Fatalf("AdvanceCursorChecked: %v", err)
		}

		mark, ok, err := store.GetCursor(ctx, CursorBackfill)
		if err != nil {
			t.Fatalf("GetCursor(backfill): %v", err)
		}
		if !ok {
			t.Fatal("下がったマークまで消えた")
		}
		if !mark.Equal(oldCursor.Add(-2 * time.Hour)) {
			t.Errorf("マーク = %s, want %s", mark, oldCursor.Add(-2*time.Hour))
		}
	})

	t.Run("挿入が無ければマークだけ消える", func(t *testing.T) {
		store := openTestStore(t)
		if err := store.SetCursor(ctx, CursorNormalize, oldCursor); err != nil {
			t.Fatalf("SetCursor: %v", err)
		}
		consumed := mustEvent(t, "late-consumed", Device("Phone"), EventNotification,
			oldCursor.Add(-time.Hour), nil, NotificationPayload{AppLabel: "Gmail", Title: "件名"})
		if _, err := store.PutBatch(ctx, []*Event{consumed}); err != nil {
			t.Fatalf("PutBatch(事前の遅着): %v", err)
		}
		usedMark, _, err := store.GetCursor(ctx, CursorBackfill)
		if err != nil {
			t.Fatalf("GetCursor(backfill): %v", err)
		}
		ranged, err := store.MaxRowID(ctx)
		if err != nil {
			t.Fatalf("MaxRowID: %v", err)
		}

		if err := store.AdvanceCursorChecked(ctx, CursorNormalize, newCursor, ranged, usedMark); err != nil {
			t.Fatalf("AdvanceCursorChecked: %v", err)
		}

		if _, ok, _ := store.GetCursor(ctx, CursorBackfill); ok {
			t.Error("消費したマークが消えていない")
		}
		cursor, _, err := store.GetCursor(ctx, CursorNormalize)
		if err != nil {
			t.Fatalf("GetCursor: %v", err)
		}
		if !cursor.Equal(newCursor) {
			t.Errorf("カーソル = %s, want %s", cursor, newCursor)
		}
	})
}

func timePtrOf(t time.Time) *time.Time { return &t }
