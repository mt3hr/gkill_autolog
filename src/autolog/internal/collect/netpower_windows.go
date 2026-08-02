//go:build windows

package collect

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

// pollInterval は Wi-Fi・Bluetooth・充電の状態を見に行く間隔。
//
// 要件の結合条件は Wi-Fi 30 秒・Bluetooth 1 分・充電 30 秒なので、
// それより十分細かければよい。
const pollInterval = 15 * time.Second

// sleepGapThreshold を超えてポーリングが空いたら、スリープをまたいだとみなす。
//
// 感度は高めにしてある。取りすぎても「つながっているものを接続として
// 記録し直す」だけで、normalize が既存の開区間へ吸収するので害がない。
// 逆に取り逃すと、suspend で閉じた区間の続きが次の接続変化まで記録されない。
const sleepGapThreshold = 2 * pollInterval

// netPowerCollector は Wi-Fi・Bluetooth・充電の接続状態の変化を記録する。
// 状態が変わったときだけイベントを出す。
type netPowerCollector struct {
	emitter *Emitter
	logger  *slog.Logger

	// wifiPermissionWarned は位置情報の許可が無い旨の警告を一度だけ出すためのフラグ。
	wifiPermissionWarned bool
}

func newNetPowerCollector(emitter *Emitter, logger *slog.Logger) *netPowerCollector {
	return &netPowerCollector{emitter: emitter, logger: logger}
}

func (c *netPowerCollector) run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var (
		// 初回のポーリングで現在の状態を記録するため、未初期化であることを持っておく。
		initialized      bool
		currentSSID      string
		currentBluetooth []string
		currentCharging  bool
		lastPollAt       time.Time
	)

	poll := func(now time.Time) {
		// ポーリングの間隔が大きく空いたのはスリープをまたいだとき。
		// 眠っている間の状態は観測できていないので、前回との差分は取らず、
		// いまつながっているものを接続開始として記録し直す。
		// 眠る前の区間は normalize が suspend の時点で閉じる。
		if initialized && !lastPollAt.IsZero() && now.Sub(lastPollAt) > sleepGapThreshold {
			c.logger.Info("ポーリングの間隔が空いたため接続状態を取り直す",
				"gap", now.Sub(lastPollAt).String())
			initialized = false
		}
		lastPollAt = now

		// 取得に失敗した回は前回の状態を維持する。
		// 失敗を「切断」として扱うと、実際にはつながったままなのに
		// 偽の切断・再接続イベントが生ログに残ってしまう。
		ssid, ok := c.pollSSID()
		if !ok {
			ssid = currentSSID
		}
		bluetooth, ok := c.pollBluetooth()
		if !ok {
			bluetooth = currentBluetooth
		}
		charging, err := winapi.IsCharging()
		if err != nil {
			c.logger.Warn("充電状態を取得できなかった", "error", err)
			charging = currentCharging
		}

		if !initialized {
			initialized = true
			currentSSID, currentBluetooth, currentCharging = ssid, bluetooth, charging
			// 収集開始時点でつながっているものを開始として記録する。
			if ssid != "" {
				c.emitWifi(now, ssid, true)
			}
			for _, name := range bluetooth {
				c.emitBluetooth(now, name, true)
			}
			if charging {
				c.emitPower(now, true)
			}
			return
		}

		if ssid != currentSSID {
			if currentSSID != "" {
				c.emitWifi(now, currentSSID, false)
			}
			if ssid != "" {
				c.emitWifi(now, ssid, true)
			}
			currentSSID = ssid
		}

		for _, name := range currentBluetooth {
			if !slices.Contains(bluetooth, name) {
				c.emitBluetooth(now, name, false)
			}
		}
		for _, name := range bluetooth {
			if !slices.Contains(currentBluetooth, name) {
				c.emitBluetooth(now, name, true)
			}
		}
		currentBluetooth = bluetooth

		if charging != currentCharging {
			c.emitPower(now, charging)
			currentCharging = charging
		}
	}

	poll(time.Now())

	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			poll(now)
		}
	}
}

// pollSSID は現在の SSID を返す。
// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
func (c *netPowerCollector) pollSSID() (string, bool) {
	ssid, ok, err := winapi.CurrentSSID()
	switch {
	case errors.Is(err, winapi.ErrWlanPermissionDenied):
		if !c.wifiPermissionWarned {
			c.wifiPermissionWarned = true
			c.logger.Warn("位置情報が許可されていないため Wi-Fi の収集を行わない。" +
				"設定 > プライバシーとセキュリティ > 位置情報 で" +
				"「位置情報サービス」と「デスクトップ アプリが位置情報にアクセスできるようにする」を有効にすること")
		}
		return "", false
	case err != nil:
		c.logger.Warn("SSIDを取得できなかった", "error", err)
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
func (c *netPowerCollector) pollBluetooth() ([]string, bool) {
	devices, err := winapi.ConnectedBluetoothDevices()
	if err != nil {
		c.logger.Warn("Bluetooth機器を列挙できなかった", "error", err)
		return nil, false
	}
	slices.Sort(devices)
	return devices, true
}

func (c *netPowerCollector) emitWifi(now time.Time, ssid string, connected bool) {
	c.logger.Info("Wi-Fiの接続状態が変化した", "ssid", ssid, "connected", connected)
	c.emitter.Emit(rawlog.EventWifi, now, nil, rawlog.WifiPayload{SSID: ssid, Connected: connected})
}

func (c *netPowerCollector) emitBluetooth(now time.Time, name string, connected bool) {
	c.logger.Info("Bluetoothの接続状態が変化した", "device_name", name, "connected", connected)
	c.emitter.Emit(rawlog.EventBluetooth, now, nil, rawlog.BluetoothPayload{DeviceName: name, Connected: connected})
}

func (c *netPowerCollector) emitPower(now time.Time, charging bool) {
	c.logger.Info("充電状態が変化した", "charging", charging)
	c.emitter.Emit(rawlog.EventPower, now, nil, rawlog.PowerPayload{Charging: charging})
}
