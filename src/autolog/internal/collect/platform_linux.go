//go:build linux && !android

package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/linuxapi"
)

// newPlatformCollectors は Linux で回す収集を組み立てる。
//
// 取れないものがあっても、取れるものは収集する。デスクトップ環境ごとに
// 取れるものが違うので、1つ欠けただけで全部やめると使える環境が狭くなる。
// 使えなかった経路は起動時に1度だけ警告する。
//
// ビルドタグに linux ではなく linux && !android と書いてあるのは、
// Go では GOOS=android が linux のビルドタグも満たすため。
// linux だけで分けると Android 向けビルドがこのファイルを取り込み、
// Termux の autolog が収集を始めてしまう（収集は Kotlin アプリの役目）。
func newPlatformCollectors(emitter *Emitter, opts Options, logger *slog.Logger) ([]collectorFunc, error) {
	env := linuxapi.DetectEnvironment()

	// 画面付きのログインセッションでなければ収集しない。
	//
	// ここで collector_start / collector_stop だけを記録すると、
	// 人が使っていない時間に「端末利用」の TimeIs が生える。
	// 収集できないことより、事実でない記録が作られることのほうが害が大きい。
	// 呼び出し側 (cmd_collect.go) は Chrome の受け口と定期撮影を続ける。
	if !env.IsUserSession() {
		return nil, fmt.Errorf(
			"画面付きのログインセッションではないため操作ログを収集しない"+
				" (DISPLAY・WAYLAND_DISPLAY・logind のセッションのどれも取れない): %w",
			ErrUnsupportedPlatform)
	}

	logger.Info("Linux の収集を用意する",
		"session_type", env.SessionType, "compositor", env.Compositor)

	// セッションの監視は D-Bus に1本も繋がらなくても起動する。
	// collector_start / collector_stop は normalize の唯一の「観測の切れ目」で
	// (internal/normalize/state.go)、失うと Wi-Fi・Bluetooth・充電の
	// 開いた区間が永久に閉じず、電源が入っていなかった時間までつながる。
	watcher := linuxapi.NewSessionWatcher()
	session := newSessionCollector(emitter, logger, watcher)

	probes, available := newLinuxProbes(opts.Linux, logger)

	collectors := []collectorFunc{keepAlive("session", logger, session.run)}

	if window, input, ok := newLinuxWindowInputs(opts.Linux, env, logger); ok {
		collector := newWindowCollector(emitter, logger, input, linuxForegroundWindow(window))
		collectors = append(collectors, keepAlive("window", logger, func(ctx context.Context) error {
			defer window.Close()
			return collector.run(ctx)
		}))
	}

	if available {
		netPower := newNetPowerCollector(emitter, logger, probes)
		// スリープ復帰をセッションの収集から接続の収集へ伝える。
		// suspend で normalize が接続区間を閉じるため、復帰後は状態を取り直して
		// 「つながっているもの」を記録し直す必要がある。
		session.onResume = netPower.requestReobserve
		collectors = append(collectors, keepAlive("netpower", logger, netPower.run))
	} else {
		logger.Warn("Wi-Fi・Bluetooth・充電のどれも観測できないため、接続の記録を行わない")
	}

	// 監視の途中で分かった不足は起動時にまとめて出す。
	for _, warning := range watcher.Warnings() {
		logger.Warn(warning)
	}

	return collectors, nil
}

// newLinuxWindowInputs はウィンドウの収集に要る2つの手段を用意する。
//
// **両方が揃ったときだけ起動する。** 入力だけあってウィンドウを取れないと、
// アプリ名が空の入力イベントが溜まる。normalize の segmentTitle は
// その場合ウィンドウタイトルへ落ちるので (internal/normalize/window.go)、
// 要件 §6.4 が禁じている「ウィンドウタイトルが TimeIs のタイトルになる」
// 状態を量産してしまう。片方しか無いなら記録しないほうがよい。
func newLinuxWindowInputs(opts LinuxOptions, env linuxapi.Environment, logger *slog.Logger) (*linuxapi.WindowSource, eventSource[linuxapi.InputKind], bool) {
	window, err := linuxapi.NewWindowSource(opts.WindowCommand)
	if err != nil {
		logger.Warn("前面ウィンドウを取れないため、アプリ利用を記録しない",
			"session_type", env.SessionType, "compositor", env.Compositor, "error", err)
		return nil, nil, false
	}

	input, err := newLinuxInputHook(opts, env, logger)
	if err != nil {
		window.Close()
		logger.Warn("入力を観測できないため、アプリ利用を記録しない", "error", err)
		return nil, nil, false
	}

	logger.Info("アプリ利用の観測手段", "window", window.Backend())
	return window, input, true
}

// newLinuxInputHook は入力の観測手段を選ぶ。
func newLinuxInputHook(opts LinuxOptions, env linuxapi.Environment, logger *slog.Logger) (eventSource[linuxapi.InputKind], error) {
	method := strings.ToLower(strings.TrimSpace(opts.InputMethod))

	if method == "none" {
		return nil, fmt.Errorf("入力の観測を行わない設定になっている: %w", linuxapi.ErrUnavailable)
	}

	// evdev が主。X11 と Wayland のどちらでも動く唯一の手段。
	if method == "" || method == "auto" || method == "evdev" {
		hook, err := linuxapi.NewInputHook(opts.InputDevices)
		if err == nil {
			logger.Info("入力の観測手段", "backend", hook.Backend())
			return hook, nil
		}
		if method == "evdev" {
			return nil, err
		}
		logger.Warn("evdev を読めないため X11 の状態を見る方法へ切り替える", "error", err)
	}

	// X11 の退避手段。追加の権限は要らないが、ホイールだけの操作を取りこぼす。
	if method == "" || method == "auto" || method == "x11" {
		if env.IsWayland() {
			return nil, fmt.Errorf("Wayland では X11 の入力を観測できない: %w", linuxapi.ErrUnavailable)
		}
		hook, err := linuxapi.NewX11InputHook(env.Display)
		if err != nil {
			return nil, err
		}
		logger.Info("入力の観測手段", "backend", hook.Backend())
		logger.Warn("evdev ではなく X11 の状態を見ているため、" +
			"ホイールだけの操作を取りこぼす。利用者を input グループへ入れると精度が上がる " +
			"(sudo usermod -aG input $USER)")
		return hook, nil
	}

	return nil, fmt.Errorf("%s の値が正しくない: %q", "AUTOLOG_LINUX_INPUT_METHOD", opts.InputMethod)
}

// linuxForegroundWindow は linuxapi の前面ウィンドウを collect 側の型へ写す。
func linuxForegroundWindow(source *linuxapi.WindowSource) foregroundWindowFunc {
	return func() (foregroundWindow, bool, error) {
		info, ok, err := source.Foreground()
		if err != nil || !ok {
			return foregroundWindow{}, false, err
		}
		return foregroundWindow{
			AppName:        info.AppName,
			AppDisplayName: info.AppDisplayName,
			WindowTitle:    info.WindowTitle,
			ProcessPath:    info.ProcessPath,
			PID:            info.PID,
		}, true, nil
	}
}

// keepAlive は片方の収集が壊れても他を巻き添えにしない。
//
// errgroup は1つが non-nil を返すと group ごと畳む。Windows のコレクタは
// 事実上返らないので問題にならなかったが、Linux では D-Bus の切断や
// X の停止が普通に起きる。壊れた理由を残して ctx が終わるまで居座り、
// 残りの収集を続けさせる。
func keepAlive(name string, logger *slog.Logger, fn collectorFunc) collectorFunc {
	return func(ctx context.Context) error {
		if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("収集が停止した。ほかの収集は続ける", "collector", name, "error", err)
			<-ctx.Done()
		}
		return nil
	}
}

// newLinuxProbes は接続状態の観測関数を用意する。
//
// 2つ目の戻り値が false のときは3つとも手段が無く、接続の収集をする意味が無い。
// 手段の無い種別は「常に取得失敗」を返す静かな関数に差し替える。
// 共通側 (netpower.go) は失敗した種別を何もしないだけなので、
// 15秒ごとに警告が出続けることがなくなる。
func newLinuxProbes(opts LinuxOptions, logger *slog.Logger) (netPowerProbes, bool) {
	probes := netPowerProbes{
		ssid:      func() (string, bool) { return "", false },
		bluetooth: func() ([]string, bool) { return nil, false },
		charging:  func() (bool, bool) { return false, false },
	}
	var available bool

	if wifi, err := linuxapi.NewWifiSource(opts.SSIDCommand); err != nil {
		logger.Warn("Wi-Fi の SSID を取る手段が無いため記録しない", "error", err)
	} else {
		logger.Info("Wi-Fi の観測手段", "backend", wifi.Backend())
		state := &linuxProbeState{logger: logger}
		probes.ssid = func() (string, bool) { return state.pollSSID(wifi) }
		available = true
	}

	if bluetooth, err := linuxapi.NewBluetoothSource(); err != nil {
		logger.Warn("Bluetooth を取る手段が無いため記録しない", "error", err)
	} else {
		logger.Info("Bluetooth の観測手段", "backend", bluetooth.Backend())
		state := &linuxProbeState{logger: logger}
		probes.bluetooth = func() ([]string, bool) { return state.pollBluetooth(bluetooth) }
		available = true
	}

	if power, err := linuxapi.NewPowerSource(); err != nil {
		logger.Warn("充電状態を取る手段が無いため記録しない", "error", err)
	} else {
		logger.Info("充電状態の観測手段", "backend", power.Backend())
		state := &linuxProbeState{logger: logger}
		probes.charging = func() (bool, bool) { return state.pollCharging(power) }
		available = true
	}

	return probes, available
}

// linuxProbeState は同じ警告を繰り返し出さないための覚え書き。
type linuxProbeState struct {
	logger           *slog.Logger
	permissionWarned bool
}

// pollSSID は現在の SSID を返す。
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
func (s *linuxProbeState) pollSSID(wifi *linuxapi.WifiSource) (string, bool) {
	ssid, ok, err := wifi.CurrentSSID()
	switch {
	case errors.Is(err, linuxapi.ErrWlanPermissionDenied):
		if !s.permissionWarned {
			s.permissionWarned = true
			s.logger.Warn("Wi-Fi の情報を見る権限が無いため SSID を記録しない。" +
				"polkit の設定で NetworkManager の参照を許可すること")
		}
		return "", false
	case err != nil:
		// 接続中で SSID だけ読めない場合もここに来る。
		// 「つながっていない」として扱うと偽の切断イベントが残るので、
		// 状態が分からないものとして返す。
		s.logger.Warn("SSIDを取得できなかった", "error", err)
		return "", false
	case !ok:
		// 取得はできて、つながっていないことが分かった。
		return "", true
	}
	return ssid, true
}

// pollBluetooth は接続中の Bluetooth 機器名を返す。
// 比較を安定させるため名前順に並べる。
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
func (s *linuxProbeState) pollBluetooth(bluetooth *linuxapi.BluetoothSource) ([]string, bool) {
	devices, err := bluetooth.Connected()
	if err != nil {
		s.logger.Warn("Bluetooth機器を列挙できなかった", "error", err)
		return nil, false
	}
	slices.Sort(devices)
	return devices, true
}

// pollCharging は充電中かを返す。
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
func (s *linuxProbeState) pollCharging(power *linuxapi.PowerSource) (bool, bool) {
	charging, err := power.IsCharging()
	if err != nil {
		s.logger.Warn("充電状態を取得できなかった", "error", err)
		return false, false
	}
	return charging, true
}
