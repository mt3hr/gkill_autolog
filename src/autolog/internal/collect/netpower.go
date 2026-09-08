package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
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

	// reobserve に入ると、次の観測は差分ではなく取り直しになる。スリープ復帰用。
	reobserve chan struct{}

	// 観測関数。プラットフォームごとの実装を受け取り、テストでも差し替える。
	// 2つ目の戻り値が false のときは取得に失敗しており、状態は分からない。
	ssidFn      func() (string, bool)
	bluetoothFn func() ([]string, bool)
	chargingFn  func() (bool, bool)

	// observed* は current* が実際の観測に基づいていて、接続開始の記録も
	// 済んでいることを表す。取り直し (スリープ復帰) では3つとも落とし、
	// 種別ごとに「取得に成功した回」から記録を再開する。
	// 失敗した種別まで前回の値で記録し直すと、実際には切れているのに
	// スリープ前の接続が「いま接続した」事実として偽記録される。
	observedWifi      bool
	observedBluetooth bool
	observedCharging  bool
	currentSSID       string
	currentBluetooth  []string
	currentCharging   bool
	lastPollAt        time.Time
}

func newNetPowerCollector(emitter *Emitter, logger *slog.Logger, probes netPowerProbes) *netPowerCollector {
	return &netPowerCollector{
		emitter:     emitter,
		logger:      logger,
		reobserve:   make(chan struct{}, 1),
		ssidFn:      probes.ssid,
		bluetoothFn: probes.bluetooth,
		chargingFn:  probes.charging,
	}
}

// requestReobserve は接続状態の取り直しを求める。どのゴルーチンから呼んでもよい。
//
// suspend の時点で normalize が接続区間を閉じるため、復帰後に
// 「いまつながっているもの」を接続開始として記録し直さないと、
// 状態が変わっていない機器の接続がいつまでも記録されない。
// ポーリングの間隔から自前で検知もしている (sleepGapThreshold) が、
// それより短いスリープはここでしか拾えない。
func (c *netPowerCollector) requestReobserve() {
	select {
	case c.reobserve <- struct{}{}:
	default:
		// 既に取り直しが予約されている。1回で足りる。
	}
}

// dropObservations は現在の観測を捨て、次の観測を差分ではなく取り直しにする。
func (c *netPowerCollector) dropObservations(reason string, args ...any) {
	if c.observedWifi || c.observedBluetooth || c.observedCharging {
		c.logger.Info(reason, args...)
	}
	c.observedWifi, c.observedBluetooth, c.observedCharging = false, false, false
}

// poll は接続状態を観測し、変化をイベントとして出す。
//
// 取得に失敗した種別はその回は何もしない。失敗を「切断」として扱うと、
// 実際にはつながったままなのに偽の切断・再接続イベントが生ログに残る。
// 取り直し待ちの種別も、失敗した回は前回の値で記録し直したりせず、
// 次に取得へ成功した回から記録を再開する。復帰直後は WLAN サービスが
// 起き切っておらず失敗しやすく、スリープ前の値をそのまま出すと
// 移動後なのに旧 SSID の接続開始が載ってしまう。
func (c *netPowerCollector) poll(now time.Time) {
	// ポーリングの間隔が大きく空いたのはスリープをまたいだとき。
	// 眠っている間の状態は観測できていないので、前回との差分は取らず、
	// いまつながっているものを接続開始として記録し直す。
	// 眠る前の区間は normalize が suspend の時点で閉じる。
	if !c.lastPollAt.IsZero() && now.Sub(c.lastPollAt) > sleepGapThreshold {
		c.dropObservations("ポーリングの間隔が空いたため接続状態を取り直す",
			"gap", now.Sub(c.lastPollAt).String())
	}
	c.lastPollAt = now

	if ssid, ok := c.ssidFn(); ok {
		switch {
		case !c.observedWifi:
			c.observedWifi = true
			c.currentSSID = ssid
			// 収集開始・取り直しの時点でつながっているものを開始として記録する。
			if ssid != "" {
				c.emitWifi(now, ssid, true)
			}
		case ssid != c.currentSSID:
			if c.currentSSID != "" {
				c.emitWifi(now, c.currentSSID, false)
			}
			if ssid != "" {
				c.emitWifi(now, ssid, true)
			}
			c.currentSSID = ssid
		}
	}

	if bluetooth, ok := c.bluetoothFn(); ok {
		if !c.observedBluetooth {
			c.observedBluetooth = true
			c.currentBluetooth = bluetooth
			for _, name := range bluetooth {
				c.emitBluetooth(now, name, true)
			}
		} else {
			for _, name := range c.currentBluetooth {
				if !slices.Contains(bluetooth, name) {
					c.emitBluetooth(now, name, false)
				}
			}
			for _, name := range bluetooth {
				if !slices.Contains(c.currentBluetooth, name) {
					c.emitBluetooth(now, name, true)
				}
			}
			c.currentBluetooth = bluetooth
		}
	}

	if charging, ok := c.chargingFn(); ok {
		switch {
		case !c.observedCharging:
			c.observedCharging = true
			c.currentCharging = charging
			if charging {
				c.emitPower(now, true)
			}
		case charging != c.currentCharging:
			c.emitPower(now, charging)
			c.currentCharging = charging
		}
	}
}

func (c *netPowerCollector) run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	c.poll(time.Now())

	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			c.poll(now)
		case <-c.reobserve:
			// スリープ復帰。眠っている間の状態は観測できていないので、
			// 差分ではなく取り直す。
			c.dropObservations("復帰したため接続状態を取り直す")
			c.poll(time.Now())
		}
	}
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
