// Package config は gkill_autolog の実行時設定と配置場所を解決する。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// 環境変数名。
const (
	EnvHome                = "AUTOLOG_HOME"
	EnvDevice              = "AUTOLOG_DEVICE"
	EnvIngestAddr          = "AUTOLOG_INGEST_ADDR"
	EnvIngestToken         = "AUTOLOG_INGEST_TOKEN"
	EnvScreenshotDir       = "AUTOLOG_SCREENSHOT_DIR"
	EnvGkillBaseURL        = "GKILL_BASE_URL"
	EnvGkillUser           = "GKILL_USER"
	EnvGkillPasswordSHA256 = "GKILL_PASSWORD_SHA256"
	EnvGkillInsecure       = "GKILL_INSECURE"
	EnvAllowedDevices      = "AUTOLOG_ALLOWED_DEVICES"

	// EnvUsageTitle は端末利用 TimeIs のタイトル。
	// 「Windows利用」のように端末ごとに呼び分けたいときに設定する。
	EnvUsageTitle = "AUTOLOG_USAGE_TITLE"

	// EnvScreenshotInterval はスクリーンショットの撮影間隔。
	// 1h / 30m / 15m のように書く。数値だけなら秒として読む。
	EnvScreenshotInterval = "AUTOLOG_SCREENSHOT_INTERVAL"

	// EnvSharedDir は収集アプリとの受け渡しに使うディレクトリ。
	// config.env より先に決まる必要があるため、環境変数からしか設定できない。
	EnvSharedDir = "AUTOLOG_SHARED_DIR"

	// Linux の収集手段に関する設定。
	//
	// どれも既定は空で、空なら環境から手段を導く（何が動いているか、
	// PATH に何があるか）。特定のデスクトップ環境の決め打ちは置かない。
	// 標準の手段で取れない環境のための逃げ道として用意する。

	// EnvLinuxWindowCommand は前面ウィンドウを JSON で出すコマンド。
	// GNOME・KDE の Wayland にはウィンドウを取る標準の手段が無い。
	EnvLinuxWindowCommand = "AUTOLOG_LINUX_WINDOW_COMMAND"
	// EnvLinuxInputMethod は入力の観測手段。auto / evdev / x11 / none。
	EnvLinuxInputMethod = "AUTOLOG_LINUX_INPUT_METHOD"
	// EnvLinuxInputDevices は使う /dev/input/event* をカンマ区切りで固定する。
	EnvLinuxInputDevices = "AUTOLOG_LINUX_INPUT_DEVICES"
	// EnvLinuxSSIDCommand は SSID を1行で出すコマンド。
	EnvLinuxSSIDCommand = "AUTOLOG_LINUX_SSID_COMMAND"
	// EnvLinuxScreenshotMethod は撮影の手段。auto / x11 / command / portal。
	EnvLinuxScreenshotMethod = "AUTOLOG_LINUX_SCREENSHOT_METHOD"
	// EnvLinuxScreenshotCommand は画面を撮って {} のパスへ書くコマンド。
	// {} が無ければ標準出力から読む。
	EnvLinuxScreenshotCommand = "AUTOLOG_LINUX_SCREENSHOT_COMMAND"

	// EnvAutoUserPrefix は端末別ユーザー名の接頭辞。
	// 実際のユーザー名は 接頭辞 + 端末名（例: 接頭辞が myuser_auto_ なら myuser_auto_Laptop）。
	EnvAutoUserPrefix = "GKILL_AUTO_USER_PREFIX"
	// EnvAutoPasswordSHA256 は端末別ユーザー共通のパスワード。
	EnvAutoPasswordSHA256 = "GKILL_AUTO_PASSWORD_SHA256"
)

// ConfigFileName は環境変数の代わりに設定を書けるファイル。
//
// Android では環境変数を設定しにくく、収集アプリと Termux の双方から
// 同じ設定を読む必要があるため、$AUTOLOG_HOME に置いたファイルからも読めるようにする。
// 環境変数が優先で、このファイルは未設定の項目だけを埋める。
const ConfigFileName = "config.env"

// 既定値。
const (
	DefaultIngestAddr   = "127.0.0.1:19921"
	DefaultGkillBaseURL = "https://127.0.0.1:9999"
	// DefaultAndroidSharedDir は Android で収集アプリと autolog が受け渡しに使う場所。
	//
	// 収集アプリ (com.mt3hr.gkill_autolog) と autolog (Termux) は別のアプリなので、
	// 互いのアプリ専用領域は見えない。共有ストレージだけが接点になる。
	//
	// ここに置くのは受け渡しに要るものだけで、raw.db と台帳は置かない。
	// 共有ストレージは FUSE で、複数プロセスから SQLite を開くとロックが効かないため。
	DefaultAndroidSharedDir = "/sdcard/gkill_autolog"
	// DefaultScreenshotInterval はスクリーンショットの撮影間隔の既定値。
	// 元は毎時00分の固定だったので、設定しなければそのときと同じ動きになる。
	DefaultScreenshotInterval = time.Hour
	// MinScreenshotInterval, MaxScreenshotInterval は撮影間隔の下限と上限。
	//
	// 下限を設けるのは、画面全体の取り込みと WebP への変換が軽くないため。
	// 上限は1日。これより長い間隔は「撮らない」と変わらないので、
	// 止めたいときは --no-screenshot を使う。
	MinScreenshotInterval = 10 * time.Second
	MaxScreenshotInterval = 24 * time.Hour
)

// Config は実行時設定。
type Config struct {
	// Home は生ログ・台帳・ログの置き場所。
	Home string
	// SharedDir は収集アプリとの受け渡しに使うディレクトリ。Android でのみ使う。
	// 空なら受け渡しは不要（Windows は収集も取り込みも同じプロセス群で完結する）。
	SharedDir string
	// Device はこの端末の名前。gkill の device 名と揃える。
	Device rawlog.Device
	// IngestAddr は Chrome 拡張からの受信アドレス。127.0.0.1 のみに bind する。
	IngestAddr string
	// IngestToken は Chrome 拡張との共有トークン。
	IngestToken string
	// ScreenshotDir はスクリーンショットの置き場。日付では分けない。
	// ここから先へ運ぶのは同期スクリプトの dvnf move の役目。
	ScreenshotDir string
	// ScreenshotInterval は定期撮影の間隔。
	// 撮影時刻はこの間隔で丸めるので、1h なら毎時00分になる。
	ScreenshotInterval time.Duration
	// GkillBaseURL, GkillUser, GkillPasswordSHA256 は取り込み先 gkill の接続情報。
	GkillBaseURL        string
	GkillUser           string
	GkillPasswordSHA256 string
	// GkillInsecure は TLS 証明書の検証を省くかどうか。
	// gkill を自己署名証明書の HTTPS で動かしている場合に要る。
	GkillInsecure bool
	// AllowedDevices は取り込み（inbox の JSONL）が受け付ける端末名。
	// 空ならこの端末の分だけを受け付ける。この端末は常に含まれる。
	AllowedDevices []rawlog.Device

	// UsageTitle は端末利用 TimeIs のタイトル。空なら normalize の既定値。
	UsageTitle string

	// AutoUserPrefix は端末別ユーザー名の接頭辞。
	// 空なら端末別ユーザーを使わず、GkillUser へまとめて書き込む。
	AutoUserPrefix string
	// AutoPasswordSHA256 は端末別ユーザー共通のパスワード。
	AutoPasswordSHA256 string

	// Linux は Linux の収集手段に関する設定。他の OS では使わない。
	Linux LinuxConfig
}

// LinuxConfig は Linux の収集手段に関する設定。
//
// 空が既定で、空なら環境から手段を導く。
type LinuxConfig struct {
	// WindowCommand は前面ウィンドウを JSON で出すコマンド。
	WindowCommand string
	// InputMethod は入力の観測手段。auto / evdev / x11 / none。
	InputMethod string
	// InputDevices は使う /dev/input/event* を固定するときに指定する。
	InputDevices []string
	// SSIDCommand は SSID を1行で出すコマンド。
	SSIDCommand string
	// ScreenshotMethod は撮影の手段。auto / x11 / command / portal。
	ScreenshotMethod string
	// ScreenshotCommand は画面を撮って {} のパスへ書くコマンド。
	ScreenshotCommand string
}

// UseAutoUsers は端末別ユーザーへ書き分ける設定かどうかを返す。
func (c *Config) UseAutoUsers() bool {
	return c.AutoUserPrefix != "" && c.AutoPasswordSHA256 != ""
}

// GkillUserFor は指定した端末の書き込み先ユーザー名を返す。
//
// 端末別ユーザーを使わない設定なら、どの端末でも GkillUser を返す。
func (c *Config) GkillUserFor(device rawlog.Device) string {
	if !c.UseAutoUsers() {
		return c.GkillUser
	}
	return c.AutoUserPrefix + string(device)
}

// GkillPasswordFor は指定した端末の書き込み先ユーザーのパスワードを返す。
func (c *Config) GkillPasswordFor(device rawlog.Device) string {
	if !c.UseAutoUsers() {
		return c.GkillPasswordSHA256
	}
	return c.AutoPasswordSHA256
}

// Load は環境変数と $AUTOLOG_HOME/config.env から設定を組み立てる。
func Load() (*Config, error) {
	home, err := resolveHome()
	if err != nil {
		return nil, err
	}

	sharedDir := resolveSharedDir()

	// 共有ディレクトリの設定を先に読み、ホーム側の設定で上書きする。
	// Android は収集アプリと同じ設定を共有ディレクトリに置く。
	fileSettings := map[string]string{}
	for _, dir := range []string{sharedDir, home} {
		if dir == "" {
			continue
		}
		settings, err := loadConfigFile(filepath.Join(dir, ConfigFileName))
		if err != nil {
			return nil, err
		}
		maps.Copy(fileSettings, settings)
	}

	// 環境変数を優先し、無い項目だけをファイルで埋める。
	lookup := func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fileSettings[key]
	}
	lookupOr := func(key, fallback string) string {
		if v := lookup(key); v != "" {
			return v
		}
		return fallback
	}

	device := rawlog.Device(lookupOr(EnvDevice, defaultDeviceName()))
	if device == "" {
		// 決め打ちの名前で続行しない。端末名は Kyou の create_device や
		// ファイル名に刻まれ、間違ったまま集めた分は後から直せない。
		return nil, fmt.Errorf("%s が設定されておらず、ホスト名からも端末名を決められない。%s を設定してください",
			EnvDevice, EnvDevice)
	}
	if err := rawlog.ValidateDeviceName(device); err != nil {
		return nil, fmt.Errorf("%s: %w", EnvDevice, err)
	}

	allowedDevices, err := resolveAllowedDevices(lookup(EnvAllowedDevices), device)
	if err != nil {
		return nil, err
	}

	// 撮影先は日付で分けずフラットにする。
	//
	// 置き場から先へ運ぶのは同期スクリプトの `dvnf move` の役目で、
	// dvnf はディレクトリを渡すと移動先に入れ子で作ってしまう
	// (gkill/dvnf/cmd/move.go の move が target/元ディレクトリ名 へ入れる)。
	// ファイルを直接移動させたいので、ここでは日付ディレクトリを作らない。
	screenshotDir := lookup(EnvScreenshotDir)
	if screenshotDir == "" {
		screenshotDir = filepath.Join(home, "screenshots")
	}

	return &Config{
		Home:                home,
		SharedDir:           sharedDir,
		Device:              device,
		IngestAddr:          lookupOr(EnvIngestAddr, DefaultIngestAddr),
		IngestToken:         lookup(EnvIngestToken),
		ScreenshotDir:       screenshotDir,
		ScreenshotInterval:  ClampScreenshotInterval(ParseDuration(lookup(EnvScreenshotInterval), DefaultScreenshotInterval)),
		GkillBaseURL:        lookupOr(EnvGkillBaseURL, DefaultGkillBaseURL),
		GkillUser:           lookup(EnvGkillUser),
		GkillPasswordSHA256: lookup(EnvGkillPasswordSHA256),
		GkillInsecure:       isTruthy(lookup(EnvGkillInsecure)),
		AllowedDevices:      allowedDevices,
		UsageTitle:          lookup(EnvUsageTitle),
		AutoUserPrefix:      lookup(EnvAutoUserPrefix),
		AutoPasswordSHA256:  lookup(EnvAutoPasswordSHA256),
		Linux: LinuxConfig{
			WindowCommand:     lookup(EnvLinuxWindowCommand),
			InputMethod:       lookup(EnvLinuxInputMethod),
			InputDevices:      splitList(lookup(EnvLinuxInputDevices)),
			SSIDCommand:       lookup(EnvLinuxSSIDCommand),
			ScreenshotMethod:  lookup(EnvLinuxScreenshotMethod),
			ScreenshotCommand: lookup(EnvLinuxScreenshotCommand),
		},
	}, nil
}

// splitList はカンマ区切りの設定値を分ける。空の要素は落とす。
func splitList(value string) []string {
	var items []string
	for item := range strings.SplitSeq(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

// loadConfigFile は config.env を読む。
//
// KEY=VALUE を1行ずつ書いた形式。# で始まる行と空行は無視する。
// ファイルが無いのは正常（PC は環境変数だけで動く）。
func loadConfigFile(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("設定ファイルを読めない %s: %w", path, err)
	}

	settings := map[string]string{}
	for line := range strings.SplitSeq(string(content), "\n") {
		// Windows のエディタで保存すると先頭に BOM が付くことがある。
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		settings[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return settings, nil
}

// resolveAllowedDevices は受け付ける端末名を決める。
//
// AUTOLOG_ALLOWED_DEVICES にカンマ区切りで書く。
// 未設定ならこの機械の分だけを受け付ける。
// 自分自身は常に含める（自機のイベントを弾かないため）。
func resolveAllowedDevices(raw string, localDevice rawlog.Device) ([]rawlog.Device, error) {
	var names []string
	if raw != "" {
		names = strings.Split(raw, ",")
	}

	devices := make([]rawlog.Device, 0, len(names)+1)
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		device := rawlog.Device(trimmed)
		if err := rawlog.ValidateDeviceName(device); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvAllowedDevices, err)
		}
		if !slices.Contains(devices, device) {
			devices = append(devices, device)
		}
	}

	if !slices.Contains(devices, localDevice) {
		devices = append(devices, localDevice)
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%s に有効な端末名がない", EnvAllowedDevices)
	}
	return devices, nil
}

// defaultDeviceName はこの機械の端末名の既定値を返す。
//
// gkill の端末名と揃えるのが本来なので、AUTOLOG_DEVICE で明示するのが望ましい。
// 未設定のときはホスト名を使う。端末名に使えない文字は落とす。
// 決め打ちの名前は置かない。ホスト名を使えなければ空を返し、
// 呼び出し側が AUTOLOG_DEVICE の設定を求めてエラーにする。
func defaultDeviceName() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}

	var builder strings.Builder
	for _, r := range hostname {
		// アンダースコアは <名前>_<端末>_<日付> の区切りと衝突する。
		if r == '_' || r == '.' || unicode.IsSpace(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// resolveSharedDir は収集アプリとの受け渡し場所を決める。
//
// config.env をここから読むため、config.env では設定できない。
func resolveSharedDir() string {
	if dir := os.Getenv(EnvSharedDir); dir != "" {
		return dir
	}
	if runtime.GOOS == "android" {
		return DefaultAndroidSharedDir
	}
	return ""
}

func resolveHome() (string, error) {
	if home := os.Getenv(EnvHome); home != "" {
		return home, nil
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return filepath.Join(localAppData, "gkill_autolog"), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%s を解決できない: %w", EnvHome, err)
	}
	return filepath.Join(userHome, ".gkill_autolog"), nil
}

// isTruthy は環境変数を真偽値として読む。gkill の MCP サーバと同じ判定にする。
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// RawDBPath は生ログDBのパス。
func (c *Config) RawDBPath() string { return filepath.Join(c.Home, "raw.db") }

// LedgerDBPath は書き込み済み台帳DBのパス。
func (c *Config) LedgerDBPath() string { return filepath.Join(c.Home, "ledger.db") }

// LogDir は実行ログの出力先。
func (c *Config) LogDir() string { return filepath.Join(c.Home, "logs") }

// WorkDir は normalize / write が中間ファイルを置く場所。
func (c *Config) WorkDir() string { return filepath.Join(c.Home, "work") }

// InboxDir は収集アプリが置いた JSONL の生ログを読む場所。
//
// 共有ディレクトリを使わない構成（Windows）では空を返し、取り込みは何もしない。
func (c *Config) InboxDir() string {
	if c.SharedDir == "" {
		return ""
	}
	return filepath.Join(c.SharedDir, "events")
}

// ScreenshotDirFor はスクリーンショットの置き場を返す。
//
// 日付では分けない。運び出しは SyncDatas.ps1 の `dvnf move` が行い、
// dvnf 側が AutoScreenshot_<端末>_<YYYYMMDD> を作って日付ごとにまとめてくれる。
// ここで日付ディレクトリを作ると移動先で入れ子になってしまう。
func (c *Config) ScreenshotDirFor(_ time.Time) string {
	return c.ScreenshotDir
}

// ScreenshotFileName は撮影時刻に対応するファイル名を返す。
// 例: <端末名>_2026-07-25_21-00-00.webp
func (c *Config) ScreenshotFileName(t time.Time, ext string) string {
	return fmt.Sprintf("%s_%s%s", c.Device, t.Format("2006-01-02_15-04-05"), ext)
}

// EnsureDirs は書き込み先ディレクトリを作成する。
func (c *Config) EnsureDirs() error {
	for _, dir := range []string{c.Home, c.LogDir(), c.WorkDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("ディレクトリを作れない %s: %w", dir, err)
		}
	}
	return nil
}

// ResolveIngestToken は Chrome 拡張との共有トークンを返す。
// 環境変数が未設定なら $AUTOLOG_HOME/ingest_token.txt から読み、無ければ生成して保存する。
func (c *Config) ResolveIngestToken() (string, error) {
	if c.IngestToken != "" {
		return c.IngestToken, nil
	}
	return c.resolvePersistedToken("ingest_token.txt")
}

// resolvePersistedToken はトークンファイルを読む。無ければ新しく作る。
// 手で設定させると初回導入が煩雑になるため、既定では自動生成にする。
func (c *Config) resolvePersistedToken(fileName string) (string, error) {
	path := filepath.Join(c.Home, fileName)

	switch content, err := os.ReadFile(path); {
	case err == nil:
		if token := strings.TrimSpace(string(content)); token != "" {
			return token, nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("トークンファイルを読めない %s: %w", path, err)
	}

	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("トークンを生成できない: %w", err)
	}
	token := hex.EncodeToString(buf[:])

	if err := os.MkdirAll(c.Home, 0o755); err != nil {
		return "", fmt.Errorf("ディレクトリを作れない %s: %w", c.Home, err)
	}
	// 共有トークンなので所有者だけが読めるようにする。
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("トークンファイルを書けない %s: %w", path, err)
	}
	return token, nil
}

// TokenPath は共有トークンを保存しているファイルのパスを返す。
func (c *Config) TokenPath(fileName string) string {
	return filepath.Join(c.Home, fileName)
}

// ParseDuration は設定値を time.Duration として読む。未設定・不正なら fallback を返す。
//
// 環境変数と config.env のどちらから来た値も同じように扱えるよう、
// 値そのものを受け取る。1h / 30m のような書式のほか、
// 単位を書き忘れやすいので数値だけなら秒として読む。
func ParseDuration(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if d, err := time.ParseDuration(value); err == nil {
		return d
	}
	if n, err := strconv.Atoi(value); err == nil {
		return time.Duration(n) * time.Second
	}
	return fallback
}

// ClampScreenshotInterval は撮影間隔を扱える範囲へ丸める。
//
// 0 以下は「設定し忘れ」として既定値に戻す。
// 極端に短い値で機械を使い潰さないよう上下限で押さえる。
func ClampScreenshotInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return DefaultScreenshotInterval
	}
	return min(max(interval, MinScreenshotInterval), MaxScreenshotInterval)
}
