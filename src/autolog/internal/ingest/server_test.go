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
	"strings"
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

// postRaw は JSON 化を経由せず、バイト列そのままを受け口へ送る。
// 本文サイズの境界を1バイト単位で検証するために使う。
func postRaw(t *testing.T, handler http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// bodyOfExactSize は全体がちょうど size バイトになる有効なリクエストボディを作る。
// title の詰め物は ASCII なので、1文字増やすと本文もちょうど1バイト増える。
func bodyOfExactSize(t *testing.T, eventID string, size int) []byte {
	t.Helper()
	build := func(pad int) []byte {
		event := browserViewEvent(eventID, "https://example.com/", baseTime(), 60)
		event["payload"].(map[string]any)["title"] = strings.Repeat("a", pad)
		encoded, err := json.Marshal(map[string]any{"events": []any{event}})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return encoded
	}
	base := build(0)
	pad := size - len(base)
	if pad < 0 {
		t.Fatalf("size %d が小さすぎる (最小 %d バイト)", size, len(base))
	}
	body := build(pad)
	if len(body) != size {
		t.Fatalf("body size = %d, want %d", len(body), size)
	}
	return body
}

// 本文サイズの上限の境界。上限ちょうどは受け付け、1バイトでも超えたら拒否すること。
// Chrome 拡張は送れなかった分をキューに貯めてまとめて再送してくるため、
// 巨大な本文で受け口が読み続けないことをここで固定しておく。
func TestBodySizeLimit(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	rec := postRaw(t, handler, bodyOfExactSize(t, "view-at-limit", maxBodyBytes))
	if rec.Code != http.StatusOK {
		t.Fatalf("上限ちょうど: status = %d, body = %.200s", rec.Code, rec.Body)
	}

	rec = postRaw(t, handler, bodyOfExactSize(t, "view-over-limit", maxBodyBytes+1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("上限超え: status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "view-at-limit" {
		t.Errorf("上限ちょうどの1件だけが保存されるべき: %d 件", len(events))
	}
}

// events に null が混ざったら全体を 400 で拒否すること。
// 一部だけ黙って保存すると、拡張側は成功と誤解してキューを捨ててしまう。
// 全体を失敗させれば拡張はまとめて再送してくるので取りこぼさない。
func TestNullEventIsRejectedWholesale(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	valid := browserViewEvent("view-1", "https://example.com/", baseTime(), 60)
	rec := postEvents(t, handler, testToken, map[string]any{"events": []any{valid, nil}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	// null より前にある有効なイベントも保存されない。
	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("null を含むリクエストなのに %d 件保存された", len(events))
	}
}

// schema_version を省略したら現行の SchemaVersion を補って受け付けること。
// このフィールドを付ける前の拡張からの送信を捨てないため。
func TestOmittedSchemaVersionIsFilled(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	event := browserViewEvent("view-1", "https://example.com/", baseTime(), 60)
	delete(event, "schema_version")

	rec := postEvents(t, handler, testToken, map[string]any{"events": []any{event}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("保存件数 = %d, want 1", len(events))
	}
	if events[0].SchemaVersion != rawlog.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", events[0].SchemaVersion, rawlog.SchemaVersion)
	}
}

// addrCapture は serve が「受け口を開いた」ログに載せる待ち受けアドレスを拾う slog.Handler。
// ポート 0 で bind させて、実際に割り当てられたポートを競合なしに知るために使う。
type addrCapture struct {
	ch chan string
}

func (c *addrCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *addrCapture) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key != "addr" {
			return true
		}
		select {
		case c.ch <- attr.Value.String():
		default:
		}
		return false
	})
	return nil
}

func (c *addrCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *addrCapture) WithGroup(string) slog.Handler      { return c }

// ルーティングの検証。受け口は POST /ingest (と CORS の OPTIONS) だけを受け付け、
// 想定外のメソッドは 405、想定外のパスは 404 になること。
// mux は serve が組み立てるので、実際にポートを開いて HTTP 越しに確かめる。
func TestServeRoutesOnlyIngestPath(t *testing.T) {
	store := newTestStore(t)
	handler := newHandler(store, discardLogger(), testToken, deviceFixed(rawlog.Device("Laptop")))

	capture := &addrCapture{ch: make(chan string, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serve(ctx, "chrome", "127.0.0.1:0", "/ingest", handler, slog.New(capture))
	}()

	var baseURL string
	select {
	case addr := <-capture.ch:
		baseURL = "http://" + addr
	case err := <-serveErr:
		t.Fatalf("serve が起動できない: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("受け口の起動を待ちきれない")
	}

	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "GET は許さない", method: http.MethodGet, path: "/ingest", want: http.StatusMethodNotAllowed},
		{name: "DELETE は許さない", method: http.MethodDelete, path: "/ingest", want: http.StatusMethodNotAllowed},
		{name: "想定外のパス", method: http.MethodPost, path: "/other", want: http.StatusNotFound},
		{name: "部分一致のパスは通さない", method: http.MethodPost, path: "/ingest/extra", want: http.StatusNotFound},
		{name: "プリフライトは通る", method: http.MethodOptions, path: "/ingest", want: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, baseURL+tt.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			// 認可ではなくルーティングで弾かれることを示すため、正しいトークンを付ける。
			req.Header.Set("Authorization", "Bearer "+testToken)
			res, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			res.Body.Close()
			if res.StatusCode != tt.want {
				t.Errorf("%s %s: status = %d, want %d", tt.method, tt.path, res.StatusCode, tt.want)
			}
		})
	}

	// ctx を終えたら serve はエラーなく停止すること。
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("停止時にエラーが返った: %v", err)
		}
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Error("serve が停止しない")
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
