package gkillclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// fixedTime はテストで使う固定時刻。
var fixedTime = time.Date(2026, 7, 26, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))

// newTestClient はテスト用サーバに向いた Client を作る。
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(Options{
		BaseURL:        server.URL,
		User:           "user_auto_Laptop",
		PasswordSHA256: strings.Repeat("a", 64),
		Device:         rawlog.Device("Laptop"),
		HTTPClient:     server.Client(),
		Now:            func() time.Time { return fixedTime },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// writeJSON はテスト用サーバの応答を書く。
func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("failed to encode response: %v", err)
	}
}

// writeJSONBody は WriteHeader を呼んだあとの w へ本文だけを書く。
// writeJSON はヘッダも書くので、ステータスを自分で決めるテストではこちらを使う。
func writeJSONBody(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("failed to encode response: %v", err)
	}
}

func TestAddTimeIsSendsAppAndDevice(t *testing.T) {
	var got map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			writeJSON(t, w, map[string]any{"session_id": "session-1"})
		case "/api/add_timeis":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	start := fixedTime.Add(-time.Hour)
	id, err := client.AddTimeIs(context.Background(), TimeIs{
		Title:     "gkill - Google Chrome",
		StartTime: start,
		EndTime:   fixedTime,
	})
	if err != nil {
		t.Fatalf("AddTimeIs: %v", err)
	}
	if id == "" {
		t.Fatal("AddTimeIs が ID を返さなかった")
	}

	if got["session_id"] != "session-1" {
		t.Errorf("session_id = %v, want session-1", got["session_id"])
	}

	timeis, ok := got["timeis"].(map[string]any)
	if !ok {
		t.Fatalf("timeis がオブジェクトではない: %#v", got["timeis"])
	}
	// MCP 経由では固定値になってしまっていた項目。ここが本改修の要点。
	if timeis["create_app"] != AppName {
		t.Errorf("create_app = %v, want %s", timeis["create_app"], AppName)
	}
	if timeis["create_device"] != "Laptop" {
		t.Errorf("create_device = %v, want Laptop", timeis["create_device"])
	}
	if timeis["update_device"] != "Laptop" {
		t.Errorf("update_device = %v, want Laptop", timeis["update_device"])
	}
	if timeis["create_user"] != "user_auto_Laptop" {
		t.Errorf("create_user = %v, want user_auto_Laptop", timeis["create_user"])
	}
	if timeis["id"] != id {
		t.Errorf("送信した id = %v, 返した id = %s", timeis["id"], id)
	}
	if timeis["data_type"] != "timeis" {
		t.Errorf("data_type = %v, want timeis", timeis["data_type"])
	}
	if timeis["end_time"] == nil {
		t.Error("end_time が null になっている")
	}
}

// 提案ごとに端末が違う場合、その端末名で書き込む。
func TestAddURLogUsesPerProposalDevice(t *testing.T) {
	var got map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			writeJSON(t, w, map[string]any{"session_id": "session-1"})
		case "/api/add_urlog":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if _, err := client.AddURLog(context.Background(), URLog{
		Device:      rawlog.Device("Phone"),
		URL:         "https://example.com/",
		Title:       "Example",
		RelatedTime: fixedTime,
	}); err != nil {
		t.Fatalf("AddURLog: %v", err)
	}

	urlog := got["urlog"].(map[string]any)
	if urlog["create_device"] != "Phone" {
		t.Errorf("create_device = %v, want Phone", urlog["create_device"])
	}
	// タイトルを空で送るとサーバが対象URLを取得しに行くため、必ず埋めて送る。
	if urlog["title"] != "Example" {
		t.Errorf("title = %v, want Example", urlog["title"])
	}
}

// gkill は異常時も HTTP 200 を返し、本文の errors に載せてくる。
func TestErrorsInBodyAreTreatedAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			writeJSON(t, w, map[string]any{"session_id": "session-1"})
		default:
			writeJSON(t, w, map[string]any{
				"errors": []map[string]string{
					{"error_code": "ERR000123", "error_message": "書き込みに失敗しました"},
				},
			})
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.AddKmemo(context.Background(), Kmemo{
		Content:     "通知本文",
		RelatedTime: fixedTime,
	})
	if err == nil {
		t.Fatal("errors があるのにエラーにならなかった")
	}

	apiError, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError ではない: %T (%v)", err, err)
	}
	if len(apiError.Errors) != 1 || apiError.Errors[0].ErrorCode != "ERR000123" {
		t.Errorf("エラー内容が違う: %#v", apiError.Errors)
	}
	if !strings.Contains(err.Error(), "ERR000123") {
		t.Errorf("エラーメッセージにコードが含まれない: %v", err)
	}
}

// セッション切れは1度だけログインし直して再試行する。
func TestSessionExpiryRetriesOnce(t *testing.T) {
	var logins, attempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			logins++
			writeJSON(t, w, map[string]any{"session_id": "session-2"})
		case "/api/add_kmemo":
			attempts++
			if attempts == 1 {
				writeJSON(t, w, map[string]any{
					"errors": []map[string]string{
						{"error_code": codeAccountSessionNotFound, "error_message": "セッションがありません"},
					},
				})
				return
			}
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if _, err := client.AddKmemo(context.Background(), Kmemo{
		Content:     "通知本文",
		RelatedTime: fixedTime,
	}); err != nil {
		t.Fatalf("AddKmemo: %v", err)
	}

	if attempts != 2 {
		t.Errorf("add_kmemo の呼び出し回数 = %d, want 2", attempts)
	}
	if logins != 2 {
		t.Errorf("login の回数 = %d, want 2 (初回 + 再取得)", logins)
	}
}

// gkill は 2026-08 から異常時に 4xx/5xx を返す(ADR-0045)。
// エラーの中身は今までどおり本文の errors にしか入っていないので、
// **ステータスで打ち切ると再ログインが不通になる**。ここが落ちたら、
// その端末の取り込みが丸ごと止まる。
func TestSessionExpiryRetriesOnceWithHTTP401(t *testing.T) {
	var logins, attempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			logins++
			writeJSON(t, w, map[string]any{"session_id": "session-2"})
		case "/api/add_kmemo":
			attempts++
			if attempts == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				writeJSONBody(t, w, map[string]any{
					"errors": []map[string]string{
						{"error_code": codeAccountSessionExpired, "error_message": "セッションの期限が切れています"},
					},
				})
				return
			}
			writeJSON(t, w, map[string]any{})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if _, err := client.AddKmemo(context.Background(), Kmemo{
		Content:     "通知本文",
		RelatedTime: fixedTime,
	}); err != nil {
		t.Fatalf("AddKmemo: %v", err)
	}

	if attempts != 2 {
		t.Errorf("add_kmemo の呼び出し回数 = %d, want 2", attempts)
	}
	if logins != 2 {
		t.Errorf("login の回数 = %d, want 2 (初回 + 再取得)", logins)
	}
}

// 回数制限(429)でも「15分待ってください」の案内が出ること。
//
// ここが効かないと、端末ごとにログインを回すスクリプトが
// **制限に当たったまま回り続けてログイン枠を使い潰す**。
func TestRateLimitErrorExplainsWaitWithHTTP429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSONBody(t, w, map[string]any{
			"errors": []map[string]string{
				{"error_code": codeLoginRateLimit, "error_message": "回数制限です"},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.AddKmemo(context.Background(), Kmemo{
		Content:     "本文",
		RelatedTime: fixedTime,
	})
	if err == nil {
		t.Fatal("回数制限はエラーになるべき")
	}
	if !strings.Contains(err.Error(), "15 分") {
		t.Errorf("回数制限の案内が出ていない: %v", err)
	}
}

// 非200でも本文の errors が読めること(業務エラーの一般形)。
func TestNon200WithErrorsIsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			writeJSON(t, w, map[string]any{"session_id": "session-1"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		writeJSONBody(t, w, map[string]any{
			"errors": []map[string]string{
				{"error_code": "ERR000058", "error_message": "既に同じIDのKmemoがあります"},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.AddKmemo(context.Background(), Kmemo{
		Content:     "本文",
		RelatedTime: fixedTime,
	})
	if err == nil {
		t.Fatal("409 はエラーになるべき")
	}
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("*APIError で返るべき(ステータスで打ち切っていないか確認): %v", err)
	}
	if len(apiError.Errors) != 1 || apiError.Errors[0].ErrorCode != "ERR000058" {
		t.Errorf("error_code が読めていない: %+v", apiError.Errors)
	}
}

// 認証と無関係なエラーでは再試行しない。無駄な書き込みを増やさないため。
func TestNonAuthErrorDoesNotRetry(t *testing.T) {
	var attempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			writeJSON(t, w, map[string]any{"session_id": "session-1"})
		case "/api/add_kmemo":
			attempts++
			writeJSON(t, w, map[string]any{
				"errors": []map[string]string{
					{"error_code": "ERR000123", "error_message": "失敗"},
				},
			})
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if _, err := client.AddKmemo(context.Background(), Kmemo{
		Content:     "通知本文",
		RelatedTime: fixedTime,
	}); err == nil {
		t.Fatal("エラーにならなかった")
	}
	if attempts != 1 {
		t.Errorf("add_kmemo の呼び出し回数 = %d, want 1", attempts)
	}
}

// HTTPS のサーバへ HTTP で繋いだ場合など、JSON ではない応答の原因が分かること。
func TestNonJSONResponseReportsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte("Client sent an HTTP request to an HTTPS server.")); err != nil {
			t.Errorf("failed to write response: %v", err)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	err := client.Login(context.Background())
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if !strings.Contains(err.Error(), "HTTPS server") {
		t.Errorf("本文が含まれない: %v", err)
	}
}

func TestLoginFailureIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"errors": []map[string]string{
				{"error_code": "ERR000002", "error_message": "アカウントが見つかりません"},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if err := client.Login(context.Background()); err == nil {
		t.Fatal("ログイン失敗がエラーにならなかった")
	}
}

// ログインに失敗したら、その後は何度呼ばれてもログインを試みないこと。
//
// 提案ごとに再ログインすると、ログイン試行の回数制限
// (IP ごとに 15 分で 10 回) をさらに使い潰して復帰を遅らせる。
func TestLoginFailureIsNotRetried(t *testing.T) {
	var logins int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			logins++
			writeJSON(t, w, map[string]any{
				"errors": []map[string]string{
					{"error_code": codeLoginRateLimit, "error_message": "ログイン試行回数の上限を超えました"},
				},
			})
			return
		}
		t.Errorf("ログインできていないのに %s を呼んだ", r.URL.Path)
	}))
	defer server.Close()

	client := newTestClient(t, server)

	// 100 件の提案を書こうとしたのと同じことをする。
	for range 100 {
		if _, err := client.AddKmemo(context.Background(), Kmemo{
			Content: "通知本文", RelatedTime: fixedTime,
		}); err == nil {
			t.Fatal("エラーにならなかった")
		}
	}

	if logins != 1 {
		t.Errorf("ログインの回数 = %d, want 1 (一度失敗したら試さない)", logins)
	}
}

// 回数制限のときは、待てば直ることが分かる案内を出すこと。
func TestRateLimitErrorExplainsWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"errors": []map[string]string{
				{"error_code": codeLoginRateLimit, "error_message": "ログイン試行回数の上限を超えました"},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	err := client.Login(context.Background())
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if !strings.Contains(err.Error(), "15 分") {
		t.Errorf("待てば直ることが伝わらない: %v", err)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	cases := map[string]Options{
		"BaseURL が空": {User: "u", PasswordSHA256: "p"},
		"User が空":    {BaseURL: "http://127.0.0.1:9999", PasswordSHA256: "p"},
		"パスワードが空":    {BaseURL: "http://127.0.0.1:9999", User: "u"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(opts); err == nil {
				t.Error("エラーにならなかった")
			}
		})
	}
}

func TestAddTimeIsRejectsMissingInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("検証前に API を呼んではいけない")
		writeJSON(t, w, map[string]any{})
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if _, err := client.AddTimeIs(context.Background(), TimeIs{Title: "t", StartTime: fixedTime}); err == nil {
		t.Error("終了時刻が無いのにエラーにならなかった")
	}
}
