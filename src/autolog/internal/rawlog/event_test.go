package rawlog

import (
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := baseTime()
	end := now.Add(time.Minute)
	before := now.Add(-time.Minute)

	newEvent := func(mutate func(*Event)) *Event {
		e, err := NewEvent("ev-1", Device("Laptop"), EventInput, now, nil,
			InputPayload{AppName: "chrome", WindowTitle: "t"})
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if mutate != nil {
			mutate(e)
		}
		return e
	}

	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr string
	}{
		{name: "正常", mutate: nil},
		{name: "終了時刻あり", mutate: func(e *Event) { e.EndTime = &end }},
		{name: "schema_version不一致", mutate: func(e *Event) { e.SchemaVersion = 99 }, wantErr: "unsupported schema_version"},
		{name: "event_idが空", mutate: func(e *Event) { e.EventID = "" }, wantErr: "event_id is empty"},
		// 端末名は書式だけを見る。どの端末を受け付けるかは受け口側の設定で決める。
		{name: "知らない端末名でも書式が正しければ通る", mutate: func(e *Event) { e.Device = "Tablet" }},
		{name: "日本語の端末名も通る", mutate: func(e *Event) { e.Device = "なし" }},
		{name: "端末名が空", mutate: func(e *Event) { e.Device = "" }, wantErr: "device is empty"},
		{name: "アンダースコアは使えない", mutate: func(e *Event) { e.Device = "X1_Yoga" }, wantErr: "underscore"},
		{name: "空白は使えない", mutate: func(e *Event) { e.Device = "X1 Yoga" }, wantErr: "whitespace"},
		{name: "パス区切りは使えない", mutate: func(e *Event) { e.Device = "X1/Yoga" }, wantErr: "directory name"},
		{name: "未知の種別", mutate: func(e *Event) { e.EventType = "unknown" }, wantErr: "unknown event_type"},
		{name: "start_timeがゼロ値", mutate: func(e *Event) { e.StartTime = time.Time{} }, wantErr: "start_time is zero"},
		{name: "end_timeがstart_timeより前", mutate: func(e *Event) { e.EndTime = &before }, wantErr: "end_time is before start_time"},
		{name: "payloadが空", mutate: func(e *Event) { e.Payload = nil }, wantErr: "payload is empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newEvent(tt.mutate).Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestTimeRoundTripKeepsOffset(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	want := time.Date(2026, 7, 25, 21, 0, 0, 0, jst)

	got, err := ParseTime(FormatTime(want))
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("往復後 = %s, want %s", got, want)
	}
	if _, offset := got.Zone(); offset != 9*60*60 {
		t.Errorf("オフセット = %d, want %d", offset, 9*60*60)
	}
}

func TestFormatTimeIsLexicographicallyOrdered(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	// raw.db は時刻を文字列で持ち ORDER BY start_time で並べるため、
	// 同一オフセット内で辞書順と時刻順が一致する必要がある。
	earlier := FormatTime(time.Date(2026, 7, 25, 9, 59, 59, 0, jst))
	later := FormatTime(time.Date(2026, 7, 25, 10, 0, 0, 0, jst))
	if !(earlier < later) {
		t.Errorf("%q < %q が成り立たない", earlier, later)
	}

	acrossDay := FormatTime(time.Date(2026, 7, 26, 0, 0, 0, 0, jst))
	if !(later < acrossDay) {
		t.Errorf("%q < %q が成り立たない", later, acrossDay)
	}
}

func TestParseTimeAcceptsRFC3339(t *testing.T) {
	// Chrome 拡張と Android は Date#toISOString 等で UTC の Z 表記を送ってくることがある。
	got, err := ParseTime("2026-07-25T12:00:00Z")
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}
	want := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("ParseTime = %s, want %s", got, want)
	}
}

func TestDecodePayload(t *testing.T) {
	now := baseTime()
	want := MediaPlayPayload{
		Service:       ServiceYouTubeMusic,
		URL:           "https://music.youtube.com/watch?v=xyz",
		VideoID:       "xyz",
		Title:         "曲名",
		Artist:        "アーティスト",
		PlayedSeconds: 185.5,
	}
	e, err := NewEvent("play-1", Device("Laptop"), EventMediaPlay, now, nil, want)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	got, err := DecodePayload[MediaPlayPayload](e)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if got != want {
		t.Errorf("payload = %+v, want %+v", got, want)
	}
}

func TestMediaPlayPayloadOmitsEmptyURL(t *testing.T) {
	// URL が確定できない再生は url を空のままにする。
	// 検索URLや推測URLを作ってはならない（要件 §8.2）。
	e, err := NewEvent("play-2", Device("Phone"), EventMediaPlay, baseTime(), nil,
		MediaPlayPayload{Service: ServiceYouTube, Title: "取得できたタイトル", PlayedSeconds: 60})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if strings.Contains(string(e.Payload), `"url"`) {
		t.Errorf("空のURLがpayloadに含まれている: %s", e.Payload)
	}

	got, err := DecodePayload[MediaPlayPayload](e)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if got.URL != "" {
		t.Errorf("URL = %q, want 空文字", got.URL)
	}
}
