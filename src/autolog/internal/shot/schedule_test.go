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

func TestRoundToTick(t *testing.T) {
	cases := []struct {
		name     string
		now      time.Time
		interval time.Duration
		want     time.Time
	}{
		// タイマーが少し早く起きた場合。切り捨てるとひとつ前の撮影時刻に
		// なってしまうので、近いほうの境目へ寄せる。
		{"1時間: 59分台に起きた", at(10, 59, 59), time.Hour, at(11, 0, 0)},
		{"1時間: 少し遅れて起きた", at(11, 0, 2), time.Hour, at(11, 0, 0)},
		{"1時間: ちょうど半分は次へ", at(10, 30, 0), time.Hour, at(11, 0, 0)},
		{"1時間: 半分の手前は手前へ", at(10, 29, 59), time.Hour, at(10, 0, 0)},
		{"15分: 少し早い", at(10, 14, 58), 15 * time.Minute, at(10, 15, 0)},
		{"15分: 少し遅い", at(10, 15, 2), 15 * time.Minute, at(10, 15, 0)},
		{"5分: 少し早い", at(10, 9, 59), 5 * time.Minute, at(10, 10, 0)},
		{"1分: 少し早い", at(10, 8, 59), time.Minute, at(10, 9, 0)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RoundToTick(c.now, c.interval)
			if !got.Equal(c.want) {
				t.Errorf("RoundToTick(%s, %s) = %s, want %s",
					c.now.Format(time.RFC3339), c.interval, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
		})
	}
}

// TestRoundToTickMatchesOldHourlyBehaviour は、毎時00分の固定だったころの
// 丸め（切り捨てたうえで30分以上過ぎていたら1時間足す）と
// 1時間間隔での結果が変わらないことを確かめる。
//
// ちょうど半分の時刻（毎時30分00秒）だけは扱いが違う。
// 旧実装は「30分より後」で判定していたので手前の正時に、
// RoundToTick は次の正時に丸める。撮影はタイマーが境目の近くで起きたときに
// 行うもので、ちょうど半分の時刻に丸めが走ることはないため、
// どちらでも記録は変わらない。ここでは境界として明示的に除く。
func TestRoundToTickMatchesOldHourlyBehaviour(t *testing.T) {
	oldBehaviour := func(now time.Time) time.Time {
		capturedAt := now.Truncate(time.Hour)
		if now.Sub(capturedAt) > 30*time.Minute {
			capturedAt = capturedAt.Add(time.Hour)
		}
		return capturedAt
	}

	now := at(10, 0, 0)
	for range 24 * 60 {
		now = now.Add(time.Minute)
		if now.Sub(now.Truncate(time.Hour)) == 30*time.Minute {
			continue
		}
		if got, want := RoundToTick(now, time.Hour), oldBehaviour(now); !got.Equal(want) {
			t.Fatalf("%s: RoundToTick = %s, 旧実装 = %s",
				now.Format(time.RFC3339), got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
}

// TestRoundToTickProducesDistinctFileNames は、1時間未満の間隔でも
// ファイル名（秒まで）が衝突しないことを確かめる。
// shot.Save は既存ファイルを上書きしないので、衝突すると撮り逃しになる。
func TestRoundToTickProducesDistinctFileNames(t *testing.T) {
	interval := 15 * time.Minute
	seen := map[string]bool{}

	now := at(0, 0, 0)
	for range 24 * 4 {
		stamp := RoundToTick(now, interval).Format("2006-01-02_15-04-05")
		if seen[stamp] {
			t.Fatalf("撮影時刻が重複した: %s", stamp)
		}
		seen[stamp] = true
		now = now.Add(interval)
	}
}
