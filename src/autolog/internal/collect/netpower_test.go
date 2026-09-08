package collect

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// newTestNetPower は観測関数を差し替えた netPowerCollector を作る。
// 出したイベントは Emitter のキューから直接読む (Run は動かさない)。
func newTestNetPower(t *testing.T) *netPowerCollector {
	t.Helper()
	store, err := rawlog.OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	emitter := NewEmitter(store, rawlog.Device("TestDevice"), slog.New(slog.DiscardHandler))
	// 既定では何も繋がっていない状態にしておく。テストごとに上書きする。
	return newNetPowerCollector(emitter, slog.New(slog.DiscardHandler), netPowerProbes{
		ssid:      func() (string, bool) { return "", true },
		bluetooth: func() ([]string, bool) { return nil, true },
		charging:  func() (bool, bool) { return false, true },
	})
}

// drainEvents はキューに溜まったイベントを取り出す。
func drainEvents(c *netPowerCollector) []*rawlog.Event {
	var events []*rawlog.Event
	for {
		select {
		case e := <-c.emitter.events:
			events = append(events, e)
		default:
			return events
		}
	}
}

func TestNetPowerReobserveSkipsFailedProbe(t *testing.T) {
	// スリープ復帰の取り直しで取得に失敗したとき、スリープ前の値を
	// 「いま接続した」として記録し直さないこと。復帰直後は WLAN サービスが
	// 起き切っておらず失敗しやすく、移動後だと実際には切れているのに
	// 旧 SSID の偽の接続区間ができてしまう。
	c := newTestNetPower(t)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))

	c.ssidFn = func() (string, bool) { return "Home", true }
	c.poll(now)
	events := drainEvents(c)
	if len(events) != 1 || events[0].EventType != rawlog.EventWifi {
		t.Fatalf("初回の接続記録 = %d 件, want Wi-Fi 1 件", len(events))
	}

	// スリープ復帰。取り直しの最初のポーリングは取得に失敗する。
	c.dropObservations("復帰したため接続状態を取り直す")
	c.ssidFn = func() (string, bool) { return "", false }
	c.poll(now.Add(pollInterval))
	if events := drainEvents(c); len(events) != 0 {
		t.Fatalf("取得に失敗したのに %d 件記録された (スリープ前の値の偽記録)", len(events))
	}

	// 取得に成功し、つながっていないと分かった。偽の接続開始を出していないので
	// 偽の切断も出ない。
	c.ssidFn = func() (string, bool) { return "", true }
	c.poll(now.Add(2 * pollInterval))
	if events := drainEvents(c); len(events) != 0 {
		t.Fatalf("未接続と分かった時点で %d 件記録された, want 0", len(events))
	}

	// 別の場所で別の SSID につながった。切断イベントなしで接続だけが載る。
	c.ssidFn = func() (string, bool) { return "Cafe", true }
	c.poll(now.Add(3 * pollInterval))
	events = drainEvents(c)
	if len(events) != 1 {
		t.Fatalf("再接続の記録 = %d 件, want 1", len(events))
	}
	payload, err := rawlog.DecodePayload[rawlog.WifiPayload](events[0])
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.SSID != "Cafe" || !payload.Connected {
		t.Errorf("payload = %+v, want Cafe の接続", payload)
	}
}

func TestNetPowerFailedProbeKeepsQuietInDiffMode(t *testing.T) {
	// 通常運転中の取得失敗では、偽の切断・再接続を出さないこと。
	c := newTestNetPower(t)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))

	c.ssidFn = func() (string, bool) { return "Home", true }
	c.bluetoothFn = func() ([]string, bool) { return []string{"Headphones"}, true }
	c.chargingFn = func() (bool, bool) { return true, true }
	c.poll(now)
	if events := drainEvents(c); len(events) != 3 {
		t.Fatalf("初回の記録 = %d 件, want 3 (Wi-Fi, Bluetooth, 充電)", len(events))
	}

	// 3種別とも取得に失敗。何も出さず、状態も変えない。
	c.ssidFn = func() (string, bool) { return "", false }
	c.bluetoothFn = func() ([]string, bool) { return nil, false }
	c.chargingFn = func() (bool, bool) { return false, false }
	c.poll(now.Add(pollInterval))
	if events := drainEvents(c); len(events) != 0 {
		t.Fatalf("取得失敗で %d 件記録された, want 0", len(events))
	}

	// 回復して同じ状態なら差分なし。
	c.ssidFn = func() (string, bool) { return "Home", true }
	c.bluetoothFn = func() ([]string, bool) { return []string{"Headphones"}, true }
	c.chargingFn = func() (bool, bool) { return true, true }
	c.poll(now.Add(2 * pollInterval))
	if events := drainEvents(c); len(events) != 0 {
		t.Fatalf("状態が変わっていないのに %d 件記録された, want 0", len(events))
	}
}

func TestNetPowerLongGapTriggersReobserve(t *testing.T) {
	// ポーリングの間隔が sleepGapThreshold を超えたら、差分ではなく
	// 「いまつながっているもの」を接続開始として記録し直すこと。
	// suspend で閉じた区間の続きがここで再開される。
	c := newTestNetPower(t)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))

	c.ssidFn = func() (string, bool) { return "Home", true }
	c.poll(now)
	if events := drainEvents(c); len(events) != 1 {
		t.Fatalf("初回の記録 = %d 件, want 1", len(events))
	}

	// 同じ SSID のままでも、間隔が空いたら接続開始を出し直す。
	c.poll(now.Add(sleepGapThreshold + time.Second))
	events := drainEvents(c)
	if len(events) != 1 {
		t.Fatalf("取り直しの記録 = %d 件, want 1", len(events))
	}
	payload, err := rawlog.DecodePayload[rawlog.WifiPayload](events[0])
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if payload.SSID != "Home" || !payload.Connected {
		t.Errorf("payload = %+v, want Home の接続", payload)
	}
}
