// Package gkillclient は gkill の HTTP API を直接叩く。
//
// 以前は gkill Write MCP サーバ (Node) を子プロセスで起動して stdio JSON-RPC で
// 話していたが、Android では Node を動かせないため純 Go の HTTP クライアントへ置き換えた。
//
// MCP との違いで重要なのは、エンティティを丸ごと送れることによって
// CreateApp / CreateDevice を自分で決められる点。
// MCP はこれらを gkill_mcp_write / mcp に固定していたため、
// 実際にどの端末で記録されたのかが Kyou に残らず、やむなくタグで代替していた。
package gkillclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// AppName は書き込んだ Kyou の create_app / update_app に入る値。
const AppName = "gkill_autolog"

// 認証に関わるエラーコード。gkill の api/message/error_codes.go に対応する。
// これらが返ったときだけセッションを取り直す。
const (
	codeAccountNotFound        = "ERR000002"
	codeAccountSessionNotFound = "ERR000013"
	codeAccountDisabled        = "ERR000238"
	codeAccountSessionExpired  = "ERR000373"
	// codeLoginRateLimit はログイン試行の回数制限。
	// gkill は IP ごとに 15 分で 10 回までしか受け付けない
	// (gkill_server_api_rate_limit.go の limit/window)。
	codeLoginRateLimit = "ERR000374"
)

// errorBodyLimit はエラー時に読み取るレスポンス本文の上限。
//
// HTTPS のサーバへ HTTP で繋いだときのように、JSON ではない応答が返ることがある。
// 原因が分かる程度には見せたいが、丸ごとログへ流し込みたくはない。
const errorBodyLimit = 512

// Options は Client の設定。
type Options struct {
	// BaseURL は gkill サーバのアドレス。末尾のスラッシュは無くてよい。
	BaseURL string
	// User と PasswordSHA256 はログインに使う。パスワードは sha256 の16進表記。
	User           string
	PasswordSHA256 string
	// Device は書き込む Kyou の create_device に入る既定値。
	// 各 Add の入力で個別に指定されていればそちらが優先される。
	Device rawlog.Device
	// Locale は API へ渡すロケール。空なら ja。
	Locale string
	// Insecure は TLS 証明書の検証を省くかどうか。
	// gkill を自己署名証明書の HTTPS で動かしている場合に要る。
	Insecure bool
	// HTTPClient を指定するとそれを使う。テストで差し替える。
	HTTPClient *http.Client
	// Now は現在時刻。テストで差し替える。
	Now func() time.Time
}

// Client は gkill の HTTP API クライアント。
//
// 並行利用は想定していない。取り込みは1件ずつ順に行う。
type Client struct {
	baseURL  string
	user     string
	password string
	device   rawlog.Device
	locale   string
	http     *http.Client
	now      func() time.Time

	sessionID string
	// loginErr は直近のログイン失敗。
	//
	// 一度失敗したらこの実行中は再試行しない。提案ごとに再ログインすると、
	// ログイン試行の回数制限 (IP ごとに 15 分で 10 回) をさらに使い潰し、
	// 復帰を遅らせるだけになる。書けなかった分は台帳へ記録されないので、
	// 次回の取り込みでやり直される。
	loginErr error
}

// New は Client を作る。この時点ではログインしない。
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("gkill の接続先 (GKILL_BASE_URL) が設定されていない")
	}
	if strings.TrimSpace(opts.User) == "" {
		return nil, fmt.Errorf("gkill のユーザー名が設定されていない")
	}
	if strings.TrimSpace(opts.PasswordSHA256) == "" {
		return nil, fmt.Errorf("gkill のパスワード (sha256) が設定されていない")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if opts.Insecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		httpClient = &http.Client{Timeout: time.Minute, Transport: transport}
	}

	locale := opts.Locale
	if locale == "" {
		locale = "ja"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Client{
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
		user:     opts.User,
		password: opts.PasswordSHA256,
		device:   opts.Device,
		locale:   locale,
		http:     httpClient,
		now:      now,
	}, nil
}

// User はログインに使うユーザー名を返す。ログの表示に使う。
func (c *Client) User() string { return c.user }

// GkillError は API が返すエラー1件。
type GkillError struct {
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// APIError は API が errors を返したことを表す。
//
// gkill は異常時も HTTP 200 を返し、本文の errors にエラーを載せる。
// ステータスコードだけを見ていると失敗を見落とす。
type APIError struct {
	Path   string
	Errors []GkillError
}

func (e *APIError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, gkillError := range e.Errors {
		parts = append(parts, fmt.Sprintf("%s: %s", gkillError.ErrorCode, gkillError.ErrorMessage))
	}
	return fmt.Sprintf("gkill API %s が失敗した: %s", e.Path, strings.Join(parts, "; "))
}

// isAuthError はセッションを取り直せば直る可能性があるかを返す。
func (e *APIError) isAuthError() bool {
	for _, gkillError := range e.Errors {
		switch gkillError.ErrorCode {
		case codeAccountNotFound, codeAccountSessionNotFound,
			codeAccountDisabled, codeAccountSessionExpired:
			return true
		}
	}
	return false
}

// isRateLimited はログイン試行の回数制限に当たったかを返す。
func (e *APIError) isRateLimited() bool {
	for _, gkillError := range e.Errors {
		if gkillError.ErrorCode == codeLoginRateLimit {
			return true
		}
	}
	return false
}

// Login はセッションを取得する。既に持っていても取り直す。
//
// 一度失敗したら、この Client ではもうログインを試みない。
// 失敗を覚えたまま同じエラーを返し続ける。
func (c *Client) Login(ctx context.Context) error {
	if c.loginErr != nil {
		return c.loginErr
	}

	err := c.login(ctx)
	if err != nil {
		c.loginErr = err
	}
	return err
}

func (c *Client) login(ctx context.Context) error {
	body := map[string]any{
		"user_id":         c.user,
		"password_sha256": c.password,
		"locale_name":     c.locale,
	}

	var response struct {
		Errors    []GkillError `json:"errors"`
		SessionID string       `json:"session_id"`
	}
	if err := c.post(ctx, "/api/login", body, &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		apiError := &APIError{Path: "/api/login", Errors: response.Errors}
		if apiError.isRateLimited() {
			return fmt.Errorf("%w\n"+
				"  gkill はログインを IP ごとに 15 分で 10 回までしか受け付けません。\n"+
				"  15 分ほど待ってからやり直してください。書けなかった分は次回に持ち越されます", apiError)
		}
		return apiError
	}
	if response.SessionID == "" {
		return fmt.Errorf("gkill へログインできたが session_id が返らなかった (user %s)", c.user)
	}

	c.sessionID = response.SessionID
	return nil
}

// call は認証付きの API を呼ぶ。
//
// セッション切れは1度だけログインし直して再試行する。
// build はリクエスト本文を作る関数で、session_id は呼び出し側で入れる必要がない。
func (c *Client) call(ctx context.Context, path string, build func(sessionID string) map[string]any) error {
	if c.sessionID == "" {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}

	err := c.callOnce(ctx, path, build)
	var apiError *APIError
	if !errors.As(err, &apiError) || !apiError.isAuthError() {
		return err
	}

	// セッションが無効になっている。取り直して1度だけやり直す。
	c.sessionID = ""
	if err := c.Login(ctx); err != nil {
		return err
	}
	return c.callOnce(ctx, path, build)
}

func (c *Client) callOnce(ctx context.Context, path string, build func(sessionID string) map[string]any) error {
	var response struct {
		Errors []GkillError `json:"errors"`
	}
	if err := c.post(ctx, path, build(c.sessionID), &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		return &APIError{Path: path, Errors: response.Errors}
	}
	return nil
}

// post は JSON を POST してレスポンスを out へ読む。
func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("リクエストを組み立てられない (%s): %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("リクエストを作れない (%s): %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("gkill (%s) へ接続できない: %w", c.baseURL, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		// 本文に原因が出ていることがある。
		// HTTPS のサーバへ HTTP で繋いだ場合など、JSON ですらないことも多い。
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyLimit))
		return fmt.Errorf("gkill API %s が HTTP %d を返した: %s",
			path, response.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("gkill API %s の応答を解釈できない: %w", path, err)
	}
	return nil
}

// deviceOf は書き込みに使う端末名を決める。
func (c *Client) deviceOf(device rawlog.Device) string {
	if device != "" {
		return string(device)
	}
	return string(c.device)
}

// newMeta は Kyou 共通のメタ情報を作る。
func (c *Client) newMeta(dataType string, device rawlog.Device) meta {
	now := c.now()
	deviceName := c.deviceOf(device)
	return meta{
		ID:       uuid.NewString(),
		DataType: dataType,
		// RepName は送っても usecase 側が書き込み用 rep 固定で無視する
		// (usecase/timeis.go の WriteTimeIsRep など)。混乱を避けるため空にしておく。
		RepName:      "",
		IsDeleted:    false,
		CreateTime:   now,
		CreateApp:    AppName,
		CreateDevice: deviceName,
		CreateUser:   c.user,
		UpdateTime:   now,
		UpdateApp:    AppName,
		UpdateDevice: deviceName,
		UpdateUser:   c.user,
	}
}

// meta は gkill の各エンティティが共通で持つ項目。
//
// gkill 本体の reps.TimeIs などをそのまま使えるとよいが、
// 取り込むと gkill サーバの依存関係を丸ごと抱えることになるため、
// 必要な項目だけをここに写している。
type meta struct {
	IsDeleted    bool      `json:"is_deleted"`
	ID           string    `json:"id"`
	RepName      string    `json:"rep_name"`
	DataType     string    `json:"data_type"`
	CreateTime   time.Time `json:"create_time"`
	CreateApp    string    `json:"create_app"`
	CreateDevice string    `json:"create_device"`
	CreateUser   string    `json:"create_user"`
	UpdateTime   time.Time `json:"update_time"`
	UpdateApp    string    `json:"update_app"`
	UpdateDevice string    `json:"update_device"`
	UpdateUser   string    `json:"update_user"`
}

// TimeIs は追加する時間区間。
type TimeIs struct {
	Device    rawlog.Device
	Title     string
	StartTime time.Time
	EndTime   time.Time
}

// URLog は追加するブックマーク。
type URLog struct {
	Device      rawlog.Device
	URL         string
	Title       string
	RelatedTime time.Time
}

// Kmemo は追加するテキスト。
type Kmemo struct {
	Device      rawlog.Device
	Content     string
	RelatedTime time.Time
}

// Tag は追加するタグ。
type Tag struct {
	Device rawlog.Device
	// TargetID はタグを付ける Kyou の ID。
	TargetID string
	Tag      string
	// RelatedTime は対象 Kyou の時刻に合わせる。
	RelatedTime time.Time
}

type timeIsPayload struct {
	meta
	Title     string     `json:"title"`
	StartTime time.Time  `json:"start_time"`
	EndTime   *time.Time `json:"end_time"`
}

type urlogPayload struct {
	meta
	RelatedTime    time.Time `json:"related_time"`
	URL            string    `json:"url"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	FaviconImage   string    `json:"favicon_image"`
	ThumbnailImage string    `json:"thumbnail_image"`
}

type kmemoPayload struct {
	meta
	RelatedTime time.Time `json:"related_time"`
	Content     string    `json:"content"`
}

type tagPayload struct {
	meta
	RelatedTime time.Time `json:"related_time"`
	TargetID    string    `json:"target_id"`
	Tag         string    `json:"tag"`
}

// AddTimeIs は TimeIs を追加し、作った Kyou の ID を返す。
func (c *Client) AddTimeIs(ctx context.Context, in TimeIs) (string, error) {
	if in.Title == "" {
		return "", fmt.Errorf("timeis のタイトルが空")
	}
	if in.StartTime.IsZero() || in.EndTime.IsZero() {
		return "", fmt.Errorf("timeis %q の開始/終了時刻が無い", in.Title)
	}

	endTime := in.EndTime
	payload := timeIsPayload{
		meta:      c.newMeta("timeis", in.Device),
		Title:     in.Title,
		StartTime: in.StartTime,
		EndTime:   &endTime,
	}
	err := c.call(ctx, "/api/add_timeis", func(sessionID string) map[string]any {
		return map[string]any{
			"session_id":  sessionID,
			"timeis":      payload,
			"locale_name": c.locale,
		}
	})
	if err != nil {
		return "", err
	}
	return payload.ID, nil
}

// AddURLog は URLog を追加し、作った Kyou の ID を返す。
//
// タイトルは必ず埋めて送る。空にすると gkill サーバが対象 URL を取得しに行き、
// タイトルを補おうとする (handle_add_urlog.go -> FillURLogField)。
func (c *Client) AddURLog(ctx context.Context, in URLog) (string, error) {
	if in.URL == "" {
		return "", fmt.Errorf("urlog の URL が空")
	}
	if in.RelatedTime.IsZero() {
		return "", fmt.Errorf("urlog %q の関連時刻が無い", in.URL)
	}

	payload := urlogPayload{
		meta:        c.newMeta("urlog", in.Device),
		RelatedTime: in.RelatedTime,
		URL:         in.URL,
		Title:       in.Title,
	}
	err := c.call(ctx, "/api/add_urlog", func(sessionID string) map[string]any {
		return map[string]any{
			"session_id":  sessionID,
			"urlog":       payload,
			"locale_name": c.locale,
		}
	})
	if err != nil {
		return "", err
	}
	return payload.ID, nil
}

// AddKmemo は Kmemo を追加し、作った Kyou の ID を返す。
func (c *Client) AddKmemo(ctx context.Context, in Kmemo) (string, error) {
	if in.Content == "" {
		return "", fmt.Errorf("kmemo の本文が空")
	}
	if in.RelatedTime.IsZero() {
		return "", fmt.Errorf("kmemo の関連時刻が無い")
	}

	payload := kmemoPayload{
		meta:        c.newMeta("kmemo", in.Device),
		RelatedTime: in.RelatedTime,
		Content:     in.Content,
	}
	err := c.call(ctx, "/api/add_kmemo", func(sessionID string) map[string]any {
		return map[string]any{
			"session_id":  sessionID,
			"kmemo":       payload,
			"locale_name": c.locale,
		}
	})
	if err != nil {
		return "", err
	}
	return payload.ID, nil
}

// AddTag は既存の Kyou へタグを付ける。
func (c *Client) AddTag(ctx context.Context, in Tag) (string, error) {
	if in.Tag == "" {
		return "", fmt.Errorf("タグ名が空")
	}
	if in.TargetID == "" {
		return "", fmt.Errorf("タグ %q の対象 ID が空", in.Tag)
	}

	relatedTime := in.RelatedTime
	if relatedTime.IsZero() {
		relatedTime = c.now()
	}

	payload := tagPayload{
		meta:        c.newMeta("tag", in.Device),
		RelatedTime: relatedTime,
		TargetID:    in.TargetID,
		Tag:         in.Tag,
	}
	err := c.call(ctx, "/api/add_tag", func(sessionID string) map[string]any {
		return map[string]any{
			"session_id":  sessionID,
			"tag":         payload,
			"locale_name": c.locale,
		}
	})
	if err != nil {
		return "", err
	}
	return payload.ID, nil
}
