package shot

import (
	"testing"
	"time"
)

// jst は撮影時刻の丸めを壁時計で確かめるための地域。
// UTC との差が時間単位なので、1時間を割り切る間隔なら切りの良い時刻に揃う。
var jst = time.FixedZone("JST", 9*60*60)

func at(hour, minute, second int) time.Time {
	return time.Date(2026, 7, 29, hour, minute, second, 0, jst)
}

func TestNextTick(t *testing.T) {
	cases := []struct {
		name     string
		now      time.Time
		interval time.Duration
		want     time.Time
	}{
		{"1時間: 正時のすこし後", at(10, 0, 1), time.Hour, at(11, 0, 0)},
		{"1時間: 正時ちょうど", at(10, 0, 0), time.Hour, at(11, 0, 0)},
		{"1時間: 直前", at(10, 59, 59), time.Hour, at(11, 0, 0)},
		{"30分: 前半", at(10, 5, 0), 30 * time.Minute, at(10, 30, 0)},
		{"30分: 後半", at(10, 40, 0), 30 * time.Minute, at(11, 0, 0)},
		{"15分", at(10, 7, 0), 15 * time.Minute, at(10, 15, 0)},
		{"5分", at(10, 7, 0), 5 * time.Minute, at(10, 10, 0)},
		{"1分", at(10, 7, 30), time.Minute, at(10, 8, 0)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NextTick(c.now, c.interval)
			if !got.Equal(c.want) {
				t.Errorf("NextTick(%s, %s) = %s, want %s",
					c.now.Format(time.RFC3339), c.interval, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

// TestNextTickAlwaysAdvances は次の撮影時刻が必ず今より後になることを確かめる。
// ここが崩れると time.Until が負になり、撮影ループが空回りする。
func TestNextTickAlwaysAdvances(t *testing.T) {
	intervals := []time.Duration{
		10 * time.Second, time.Minute, 5 * time.Minute,
		15 * time.Minute, 30 * time.Minute, time.Hour, 24 * time.Hour,
	}
	now := at(10, 37, 42)

	for _, interval := range intervals {
		next := NextTick(now, interval)
		if !next.After(now) {
			t.Errorf("interval %s: NextTick が進んでいない (%s -> %s)",
				interval, now.Format(time.RFC3339), next.Format(time.RFC3339))
		}
		if d := next.Sub(now); d > interval {
			t.Errorf("interval %s: 次の撮影まで %s あり、間隔より長い", interval, d)
		}
	}
}

func TestCaptureTime(t *testing.T) {
	cases := []struct {
		name     string
		tick     time.Time
		now      time.Time
		interval time.Duration
		want     time.Time
	}{
		// タイマーの起床が数秒ずれても、狙った区切りの時刻で記録する。
		{"1時間: 少し早く起きた", at(11, 0, 0), at(10, 59, 59), time.Hour, at(11, 0, 0)},
		{"1時間: 少し遅れて起きた", at(11, 0, 0), at(11, 0, 2), time.Hour, at(11, 0, 0)},
		{"1時間: ちょうど", at(11, 0, 0), at(11, 0, 0), time.Hour, at(11, 0, 0)},
		{"1時間: 許容の上限(1分)まで", at(11, 0, 0), at(11, 1, 0), time.Hour, at(11, 0, 0)},
		// スリープ復帰などで大きく遅れたら、実際に撮れた時刻をそのまま使う。
		// 区切りへ丸めると、撮っていない時刻の画像として記録してしまう。
		{"1時間: 復帰で大きく遅れた", at(13, 0, 0), at(15, 47, 12), time.Hour, at(15, 47, 12)},
		{"1時間: 1分を超えた遅れ", at(11, 0, 0), at(11, 1, 1), time.Hour, at(11, 1, 1)},
		// 短い間隔では許容も間隔の半分に縮む。
		{"10秒: 3秒の遅れは丸める", at(10, 0, 10), at(10, 0, 13), 10 * time.Second, at(10, 0, 10)},
		{"10秒: 6秒の遅れは実時刻", at(10, 0, 10), at(10, 0, 16), 10 * time.Second, at(10, 0, 16)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CaptureTime(c.tick, c.now, c.interval)
			if !got.Equal(c.want) {
				t.Errorf("CaptureTime(%s, %s, %s) = %s, want %s",
					c.tick.Format(time.RFC3339), c.now.Format(time.RFC3339), c.interval,
					got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}
