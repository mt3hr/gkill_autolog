//go:build windows

package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"errors"
	"log/slog"
	"slices"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

// newPlatformCollectors は Windows で回す収集を組み立てる。
//
// 収集そのもののロジックは OS を知らないタグ無しのファイルにあり、
// ここは winapi との配線だけを行う。
func newPlatformCollectors(emitter *Emitter, _ Options, logger *slog.Logger) ([]collectorFunc, error) {
	probes := &windowsProbes{logger: logger}
	netPower := newNetPowerCollector(emitter, logger, netPowerProbes{
		ssid:      probes.pollSSID,
		bluetooth: probes.pollBluetooth,
		charging:  probes.pollCharging,
	})

	// スリープ復帰をセッションの収集から接続の収集へ伝える。
	// suspend で normalize が接続区間を閉じるため、復帰後は状態を取り直して
	// 「つながっているもの」を記録し直す必要がある。ポーリング間隔からの
	// 自前検知 (sleepGapThreshold) では30秒未満のスリープを取りこぼす。
	session := newSessionCollector[winapi.SessionEventKind](emitter, logger, winapi.NewSessionWatcher())
	session.onResume = netPower.requestReobserve

	window := newWindowCollector[winapi.InputKind](emitter, logger, winapi.NewInputHook(), foregroundWindowOf)

	return []collectorFunc{window.run, session.run, netPower.run}, nil
}

// foregroundWindowOf は winapi の前面ウィンドウを collect 側の型へ写す。
func foregroundWindowOf() (foregroundWindow, bool, error) {
	info, ok, err := winapi.GetForegroundWindow()
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

// windowsProbes は接続状態の観測を winapi へ委ねる。
//
// 失敗したときの案内は Windows 固有 (位置情報の許可) なので、
// 共通側 (netpower.go) へは持ち上げずここに置く。
type windowsProbes struct {
	logger *slog.Logger

	// wifiPermissionWarned は位置情報の許可が無い旨の警告を一度だけ出すためのフラグ。
	wifiPermissionWarned bool
}

// pollSSID は現在の SSID を返す。
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
func (p *windowsProbes) pollSSID() (string, bool) {
	ssid, ok, err := winapi.CurrentSSID()
	switch {
	case errors.Is(err, winapi.ErrWlanPermissionDenied):
		if !p.wifiPermissionWarned {
			p.wifiPermissionWarned = true
			p.logger.Warn("位置情報が許可されていないため Wi-Fi の収集を行わない。" +
				"設定 > プライバシーとセキュリティ > 位置情報 で" +
				"「位置情報サービス」と「デスクトップ アプリが位置情報にアクセスできるようにする」を有効にすること")
		}
		return "", false
	case err != nil:
		p.logger.Warn("SSIDを取得できなかった", "error", err)
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
func (p *windowsProbes) pollBluetooth() ([]string, bool) {
	devices, err := winapi.ConnectedBluetoothDevices()
	if err != nil {
		p.logger.Warn("Bluetooth機器を列挙できなかった", "error", err)
		return nil, false
	}
	slices.Sort(devices)
	return devices, true
}

// pollCharging は充電中かを返す。
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
func (p *windowsProbes) pollCharging() (bool, bool) {
	charging, err := winapi.IsCharging()
	if err != nil {
		p.logger.Warn("充電状態を取得できなかった", "error", err)
		return false, false
	}
	return charging, true
}
