package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

const testToken = "test-token-abcdef"

func newTestStore(t *testing.T) *rawlog.Store {
	t.Helper()
	store, err := rawlog.OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseTime() time.Time {
	return time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
}

// postEvents は受け口へイベントを送る。
func postEvents(t *testing.T, handler http.Handler, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(encoded))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// browserViewEvent は Chrome 拡張が送ってくる形のイベントを作る。
// 拡張は端末名を持たないため device は空にしてある。
func browserViewEvent(eventID, url string, start time.Time, seconds int) map[string]any {
	end := start.Add(time.Duration(seconds) * time.Second)
	return map[string]any{
		"schema_version": 1,
		"event_id":       eventID,
		"event_type":     "browser_view",
		"start_time":     rawlog.FormatTime(start),
		"end_time":       rawlog.FormatTime(end),
		"payload": map[string]any{
			"url":             url,
			"title":           "テストページ",
			"tab_id":          7,
			"browser_focused": true,
			"source":          "chrome_extension",
		},
	}
}

func TestChromeEndpointStoresEventsWithLocalDevice(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	rec := postEvents(t, handler, testToken, Request{})
	if rec.Code != http.StatusOK {
		t.Fatalf("空リクエスト: status = %d, body = %s", rec.Code, rec.Body)
	}

	body := map[string]any{"events": []any{
		browserViewEvent("view-1", "https://example.com/a", baseTime(), 45),
		browserViewEvent("view-2", "https://example.com/b", baseTime().Add(time.Minute), 90),
	}}
	rec = postEvents(t, handler, testToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var response Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("応答を解釈できない: %v (%s)", err, rec.Body)
	}
	if response.Accepted != 2 || response.Inserted != 2 {
		t.Errorf("response = %+v, want accepted=2 inserted=2", response)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("保存件数 = %d, want 2", len(events))
	}
	for _, e := range events {
		// 拡張は端末名を送らないので、受け口がこの機械の名前を入れる。
		if e.Device != rawlog.Device("Laptop") {
			t.Errorf("device = %q, want %q", e.Device, rawlog.Device("Laptop"))
		}
		if e.CapturedAt.IsZero() {
			t.Error("captured_at が補われていない")
		}
	}
}

func TestChromeEndpointOverridesClaimedDevice(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	event := browserViewEvent("view-1", "https://example.com/", baseTime(), 60)
	event["device"] = "Phone"

	rec := postEvents(t, handler, testToken, map[string]any{"events": []any{event}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if events[0].Device != rawlog.Device("Laptop") {
		t.Errorf("device = %q, want %q (拡張の申告は無視する)", events[0].Device, rawlog.Device("Laptop"))
	}
}

func TestResendIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	body := map[string]any{"events": []any{
		browserViewEvent("view-1", "https://example.com/", baseTime(), 60),
	}}

	rec := postEvents(t, handler, testToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("初回: status = %d", rec.Code)
	}

	// 拡張は送信成功を確認できるまでキューに残すため、同じイベントを再送してくる。
	rec = postEvents(t, handler, testToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("再送: status = %d", rec.Code)
	}
	var response Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("応答を解釈できない: %v", err)
	}
	if response.Accepted != 1 || response.Inserted != 0 {
		t.Errorf("再送時の response = %+v, want accepted=1 inserted=0", response)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("保存件数 = %d, want 1", len(events))
	}
}

func TestTokenIsRequired(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))
	body := map[string]any{"events": []any{
		browserViewEvent("view-1", "https://example.com/", baseTime(), 60),
	}}

	tests := []struct {
		name  string
		token string
	}{
		{name: "トークンなし", token: ""},
		{name: "間違ったトークン", token: "wrong-token"},
		{name: "前方一致だけのトークン", token: testToken[:5]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postEvents(t, handler, tt.token, body)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("認証に失敗したのに %d 件保存された", len(events))
	}
}

func TestAndroidEndpointRequiresForeignDevice(t *testing.T) {
	tests := []struct {
		name       string
		device     any
		wantStatus int
	}{
		{name: "許可した端末なら受け付ける", device: "Phone", wantStatus: http.StatusOK},
		{name: "許可リストに足した端末も受け付ける", device: "Tablet", wantStatus: http.StatusOK},
		{name: "この機械を名乗るのは拒否", device: "Laptop", wantStatus: http.StatusBadRequest},
		{name: "端末名なしは拒否", device: nil, wantStatus: http.StatusBadRequest},
		{name: "許可していない端末は拒否", device: "Unknown", wantStatus: http.StatusBadRequest},
	}

	// 受け付ける端末は設定で決める。コードに列挙しない。
	allowed := []rawlog.Device{rawlog.Device("Laptop"), rawlog.Device("Phone"), "Tablet"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			handler := newHandler(store, discardLogger(), testToken, deviceFromRequest(rawlog.Device("Laptop"), allowed))

			event := map[string]any{
				"schema_version": 1,
				"event_id":       "notif-1",
				"event_type":     "notification",
				"start_time":     rawlog.FormatTime(baseTime()),
				"payload":        map[string]any{"app_label": "Gmail", "title": "件名"},
			}
			if tt.device != nil {
				event["device"] = tt.device
			}

			rec := postEvents(t, handler, testToken, map[string]any{"events": []any{event}})
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body = %s)", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestInvalidEventsAreRejectedWholesale(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	valid := browserViewEvent("view-1", "https://example.com/", baseTime(), 60)
	invalid := browserViewEvent("", "https://example.com/bad", baseTime(), 60)

	rec := postEvents(t, handler, testToken, map[string]any{"events": []any{valid, invalid}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("不正なイベントを含むのに %d 件保存された", len(events))
	}
}

func TestUnsupportedSchemaVersionIsRejected(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	event := browserViewEvent("view-1", "https://example.com/", baseTime(), 60)
	event["schema_version"] = 99

	rec := postEvents(t, handler, testToken, map[string]any{"events": []any{event}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestMalformedJSONIsRejected(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCORSHeadersArePresent(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	// Chrome 拡張の fetch はプリフライトを送るため、認証前にヘッダが付いている必要がある。
	rec := postEvents(t, handler, "", Request{})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}

	rec = httptest.NewRecorder()
	handlePreflight(rec, httptest.NewRequest(http.MethodOptions, "/ingest", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("プリフライト status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("Access-Control-Allow-Headers が無い")
	}
}
