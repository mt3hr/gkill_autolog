// Package rawlog は各端末の収集プログラムが出力する共通生ログを扱う。
// スキーマは schema/event.schema.json と対応している。
package rawlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// SchemaVersion は現在の生ログスキーマバージョン。
const SchemaVersion = 1

// TimeLayout は生ログの時刻表記。gkill 本体の sqlite3impl.TimeLayout と揃えてある。
// 常にオフセットを出すため、文字列の辞書順が時刻順と一致する（同一オフセット内であれば）。
const TimeLayout = "2006-01-02T15:04:05-07:00"

// Device は収集元の端末。gkill の device 名と揃える。
//
// 値をここで列挙はしない。端末を増やすたびにコード修正と再ビルドが要ると窮屈なため、
// どの端末を受け付けるかは受け口側の設定 (AUTOLOG_ALLOWED_DEVICES) で決める。
// ここでは書式だけを検査する。
type Device string

// maxDeviceNameLength は端末名の長さの上限。
const maxDeviceNameLength = 64

// ValidateDeviceName は端末名として使える書式かを返す。
//
// 端末名は gkill のタグになり、スクリーンショットのディレクトリ名
// Screenshot_<端末>_<YYYYMMDD> の一部にもなる。
// そのため区切り文字のアンダースコアと空白は使えない。
func ValidateDeviceName(device Device) error {
	name := string(device)
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("device is empty")
	}
	if len([]rune(name)) > maxDeviceNameLength {
		return fmt.Errorf("device %q is too long (max %d characters)", name, maxDeviceNameLength)
	}
	for _, r := range name {
		switch {
		case r == '_':
			// Screenshot_<端末>_<YYYYMMDD> の区切りと衝突する。
			return fmt.Errorf("device %q must not contain an underscore", name)
		case unicode.IsSpace(r), unicode.IsControl(r):
			return fmt.Errorf("device %q must not contain whitespace or control characters", name)
		case strings.ContainsRune(`/\:*?"<>|`, r):
			// ディレクトリ名に使えない文字。
			return fmt.Errorf("device %q contains a character that cannot be used in a directory name", name)
		}
	}
	return nil
}

// EventType は生ログイベントの種別。
type EventType string

const (
	// EventInput はクリック・ホイール・キー入力を伴うアクティブウィンドウ。マウス移動では出さない。
	EventInput EventType = "input"
	// EventSession は端末の利用状態の変化（ロック解除、ロック、電源など）。
	EventSession EventType = "session"
	// EventWifi は Wi-Fi の接続状態変化。
	EventWifi EventType = "wifi"
	// EventBluetooth は Bluetooth 機器の接続状態変化。
	EventBluetooth EventType = "bluetooth"
	// EventPower は充電状態の変化。
	EventPower EventType = "power"
	// EventBrowserView はブラウザの1回の連続閲覧区間。
	EventBrowserView EventType = "browser_view"
	// EventMediaPlay は動画・音楽の1回の再生。
	EventMediaPlay EventType = "media_play"
	// EventAppUsage は Android のアプリ利用区間。
	EventAppUsage EventType = "app_usage"
	// EventNotification は Android の通知。
	EventNotification EventType = "notification"
)

// Event は共通生ログの1件。payload は event_type ごとに構造が異なる。
type Event struct {
	SchemaVersion int             `json:"schema_version"`
	EventID       string          `json:"event_id"`
	Device        Device          `json:"device"`
	EventType     EventType       `json:"event_type"`
	StartTime     time.Time       `json:"start_time"`
	EndTime       *time.Time      `json:"end_time"`
	CapturedAt    time.Time       `json:"captured_at"`
	Payload       json.RawMessage `json:"payload"`
}

// InputPayload は EventInput の payload。
//
// AppDisplayName は gkill のタイトルに使う表示上のアプリ名。
// 実行ファイルの説明を取れなかった場合と、この項目ができる前に集めた生ログでは空になる。
type InputPayload struct {
	AppName        string `json:"app_name"`
	AppDisplayName string `json:"app_display_name,omitempty"`
	WindowTitle    string `json:"window_title"`
	ProcessPath    string `json:"process_path,omitempty"`
	PID            int    `json:"pid,omitempty"`
	Kind           string `json:"kind,omitempty"` // click / wheel / key
}

// SessionAction は SessionPayload.Action の値。
type SessionAction string

const (
	SessionLogon          SessionAction = "logon"
	SessionUnlock         SessionAction = "unlock"
	SessionLock           SessionAction = "lock"
	SessionLogoff         SessionAction = "logoff"
	SessionShutdown       SessionAction = "shutdown"
	SessionSuspend        SessionAction = "suspend"
	SessionResume         SessionAction = "resume"
	SessionScreenOff      SessionAction = "screen_off"
	SessionCollectorStart SessionAction = "collector_start"
	SessionCollectorStop  SessionAction = "collector_stop"
)

// SessionPayload は EventSession の payload。
type SessionPayload struct {
	Action SessionAction `json:"action"`
	// Recovered は収集プログラムの異常終了後に最終入力時刻から復元した終了イベントであることを示す。
	Recovered bool `json:"recovered,omitempty"`
}

// WifiPayload は EventWifi の payload。BSSID は生ログにも保持しない。
type WifiPayload struct {
	SSID      string `json:"ssid"`
	Connected bool   `json:"connected"`
}

// BluetoothPayload は EventBluetooth の payload。
type BluetoothPayload struct {
	DeviceName string `json:"device_name"`
	Connected  bool   `json:"connected"`
}

// PowerPayload は EventPower の payload。残量・充電方式は記録しない。
type PowerPayload struct {
	Charging bool `json:"charging"`
}

// BrowserViewPayload は EventBrowserView の payload。
type BrowserViewPayload struct {
	URL            string `json:"url"`
	Title          string `json:"title"`
	TabID          int    `json:"tab_id,omitempty"`
	WindowID       int    `json:"window_id,omitempty"`
	BrowserFocused bool   `json:"browser_focused"`
	Source         string `json:"source,omitempty"` // chrome_extension / chrome_history_db
}

// MediaService は再生元サービス。
type MediaService string

const (
	ServiceYouTube      MediaService = "youtube"
	ServiceYouTubeMusic MediaService = "youtube_music"
	// ServiceWeb は YouTube 以外のサイトでの再生。
	ServiceWeb MediaService = "web"
	// ServiceApp は YouTube 以外のアプリでの再生。AppLabel にアプリ名が入る。
	ServiceApp MediaService = "app"
)

// MediaPlayPayload は EventMediaPlay の payload。
// PlayedSeconds には一時停止時間と広告再生時間を含めない。
// URL が確定できない場合は空文字のままにする。検索URLや推測URLを作ってはならない。
//
// AppLabel は表示上のアプリ名。パッケージ名は入れない（要件 §11.2）。
type MediaPlayPayload struct {
	Service       MediaService `json:"service"`
	URL           string       `json:"url,omitempty"`
	VideoID       string       `json:"video_id,omitempty"`
	Title         string       `json:"title,omitempty"`
	Artist        string       `json:"artist,omitempty"`
	AppLabel      string       `json:"app_label,omitempty"`
	PlayedSeconds float64      `json:"played_seconds"`
}

// AppUsagePayload は EventAppUsage の payload。
// PackageName は内部識別用で、gkill へは出さない。
type AppUsagePayload struct {
	AppLabel    string `json:"app_label"`
	PackageName string `json:"package_name,omitempty"`
	SplitScreen bool   `json:"split_screen,omitempty"`
}

// NotificationPayload は EventNotification の payload。
//
// ChannelID と Category は Android が通知に付けている値をそのまま持つ。
// 同じアプリでも通知の種類ごとにチャンネルが分かれるので、
// 「Chrome のダウンロード完了だけ除外する」のような判定に使える。
// この項目ができる前に集めた生ログでは空になる。
type NotificationPayload struct {
	AppLabel        string `json:"app_label"`
	PackageName     string `json:"package_name,omitempty"`
	Title           string `json:"title,omitempty"`
	Body            string `json:"body,omitempty"`
	NotificationKey string `json:"notification_key,omitempty"`
	Ongoing         bool   `json:"ongoing,omitempty"`
	ChannelID       string `json:"channel_id,omitempty"`
	Category        string `json:"category,omitempty"`
}

// NewEvent は payload を JSON 化して Event を組み立てる。
// endTime が nil なら瞬間イベント、または継続中の区間として扱われる。
func NewEvent(eventID string, device Device, eventType EventType, startTime time.Time, endTime *time.Time, payload any) (*Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload for event %s: %w", eventID, err)
	}
	return &Event{
		SchemaVersion: SchemaVersion,
		EventID:       eventID,
		Device:        device,
		EventType:     eventType,
		StartTime:     startTime,
		EndTime:       endTime,
		CapturedAt:    time.Now(),
		Payload:       raw,
	}, nil
}

// DecodePayload は Event の payload を型付き構造体へ復元する。
func DecodePayload[T any](e *Event) (T, error) {
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		return v, fmt.Errorf("failed to unmarshal payload of event %s (%s): %w", e.EventID, e.EventType, err)
	}
	return v, nil
}

// Validate は必須項目とスキーマバージョンを検査する。
// 受信したイベントを raw.db へ入れる前に必ず通すこと。
func (e *Event) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (want %d)", e.SchemaVersion, SchemaVersion)
	}
	if e.EventID == "" {
		return fmt.Errorf("event_id is empty")
	}
	if err := ValidateDeviceName(e.Device); err != nil {
		return err
	}
	switch e.EventType {
	case EventInput, EventSession, EventWifi, EventBluetooth, EventPower,
		EventBrowserView, EventMediaPlay, EventAppUsage, EventNotification:
	default:
		return fmt.Errorf("unknown event_type %q", e.EventType)
	}
	if e.StartTime.IsZero() {
		return fmt.Errorf("start_time is zero for event %s", e.EventID)
	}
	if e.EndTime != nil && e.EndTime.Before(e.StartTime) {
		return fmt.Errorf("end_time is before start_time for event %s", e.EventID)
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("payload is empty for event %s", e.EventID)
	}
	return nil
}

// FormatTime は生ログ用の時刻文字列を返す。
func FormatTime(t time.Time) string {
	return t.Format(TimeLayout)
}

// ParseTime は生ログの時刻文字列を復元する。RFC3339 も受け付ける。
func ParseTime(s string) (time.Time, error) {
	if t, err := time.Parse(TimeLayout, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse time %q: %w", s, err)
	}
	return t, nil
}
