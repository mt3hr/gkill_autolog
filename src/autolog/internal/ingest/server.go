// Package ingest は Chrome 拡張からイベントを受け取る HTTP サーバ。
//
//	POST /ingest  Chrome 拡張から。127.0.0.1 のみに bind する。
//
// Authorization: Bearer <token> を要求し、受け取ったイベントは
// 生ログストアへ冪等に追記する。同じ event_id の再送は握り潰される。
//
// 端末をまたぐ受け口は持たない。各端末は自分で集めて自分の gkill へ取り込む。
package ingest

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"golang.org/x/sync/errgroup"
)

const (
	// maxBodyBytes は1リクエストの上限。Chrome 拡張はまとめて送ってくるが、
	// それでもこの大きさを超えることはない。
	maxBodyBytes = 8 << 20

	// shutdownTimeout は停止時に送信中のリクエストを待つ時間。
	shutdownTimeout = 5 * time.Second
)

// Request は受け口の JSON ボディ。
type Request struct {
	Events []*rawlog.Event `json:"events"`
}

// Response は受け口の応答。
type Response struct {
	// Accepted は受け取った件数。
	Accepted int `json:"accepted"`
	// Inserted は実際に追記された件数。再送分は含まれない。
	Inserted int `json:"inserted"`
}

// Options は受け口の設定。
type Options struct {
	// LocalDevice はこの機械の端末名。/ingest はこの端末のイベントとして扱う。
	LocalDevice rawlog.Device

	// ChromeAddr は Chrome 拡張からの受信アドレス。127.0.0.1 に bind すること。
	ChromeAddr string
	// ChromeToken は Chrome 拡張との共有トークン。空なら受け口を開かない。
	ChromeToken string
}

// Run は受け口を開き、ctx が終わるまで待ち受ける。
func Run(ctx context.Context, store *rawlog.Store, opts Options, logger *slog.Logger) error {
	group, groupCtx := errgroup.WithContext(ctx)
	started := 0

	if opts.ChromeToken == "" {
		logger.Warn("共有トークンが未設定のため Chrome 拡張の受け口を開かない", "addr", opts.ChromeAddr)
	} else {
		started++
		handler := newHandler(store, logger, opts.ChromeToken, deviceFixed(opts.LocalDevice))
		group.Go(func() error {
			return serve(groupCtx, "chrome", opts.ChromeAddr, "/ingest", handler, logger)
		})
	}

	if started == 0 {
		// 受け口が1つも開かないのは設定漏れなので、collect 全体は止めずに待機する。
		<-ctx.Done()
		return nil
	}
	return group.Wait()
}

// serve は1つの受け口を待ち受ける。
func serve(ctx context.Context, name, addr, path string, handler http.Handler, logger *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("POST "+path, handler)
	mux.Handle("OPTIONS "+path, http.HandlerFunc(handlePreflight))
	return serveMux(ctx, name, addr, mux, path, logger)
}

// serveMux は組み立てた mux で待ち受ける。
func serveMux(ctx context.Context, name, addr string, mux *http.ServeMux, path string, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s for %s: %w", addr, name, err)
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("受け口の停止に失敗した", "name", name, "error", err)
		}
	}()

	logger.Info("受け口を開いた", "name", name, "addr", listener.Addr().String(), "path", path)

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to serve %s: %w", name, err)
	}
	// Serve は Shutdown が始まった時点で返る。処理中のリクエストを待ち終える前に
	// 呼び出し側へ戻ると、store が閉じられて処理中の書き込みが失敗する。
	<-shutdownDone
	return nil
}

// deviceResolver は受け取ったイベントの端末名を決める。
type deviceResolver func(e *rawlog.Event) error

// deviceFixed は端末名を常にこの機械のものへ揃える。
// Chrome 拡張は同じ機械で動いているので、拡張側に端末名を持たせない。
func deviceFixed(device rawlog.Device) deviceResolver {
	return func(e *rawlog.Event) error {
		e.Device = device
		return nil
	}
}

// newHandler は受け口のハンドラを作る。
func newHandler(store *rawlog.Store, logger *slog.Logger, token string, resolveDevice deviceResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)

		if !hasValidToken(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			logger.Warn("受信したJSONを解釈できなかった", "error", err)
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}

		now := time.Now()
		for _, event := range request.Events {
			if event == nil {
				http.Error(w, "events must not contain null", http.StatusBadRequest)
				return
			}
			if err := resolveDevice(event); err != nil {
				logger.Warn("受信したイベントの端末名が不正", "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if event.SchemaVersion == 0 {
				event.SchemaVersion = rawlog.SchemaVersion
			}
			if event.CapturedAt.IsZero() {
				event.CapturedAt = now
			}
			if err := event.Validate(); err != nil {
				logger.Warn("受信したイベントが不正", "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		inserted, err := store.PutBatch(r.Context(), request.Events)
		if err != nil {
			logger.Error("受信したイベントを保存できなかった", "count", len(request.Events), "error", err)
			http.Error(w, "failed to store events", http.StatusInternalServerError)
			return
		}
		if inserted != len(request.Events) {
			logger.Debug("再送分を除いて保存した", "inserted", inserted, "accepted", len(request.Events))
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(Response{Accepted: len(request.Events), Inserted: inserted}); err != nil {
			logger.Warn("応答を書けなかった", "error", err)
		}
	})
}

// hasValidToken は Authorization ヘッダのトークンを検査する。
// 比較時間が内容に依存しないようにする。
func hasValidToken(r *http.Request, token string) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(token)) == 1
}

// setCORSHeaders は Chrome 拡張の fetch を通すためのヘッダを付ける。
// 受け口自体はトークンで守るので、オリジンは制限しない。
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
}

// handlePreflight は CORS のプリフライトへ応答する。
func handlePreflight(w http.ResponseWriter, _ *http.Request) {
	setCORSHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}
