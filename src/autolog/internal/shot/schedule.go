package shot

import "time"

// 撮影時刻の決め方。
//
// 撮影は「間隔ぶん経ったら撮る」ではなく「間隔で割った境目に撮る」。
// 毎時00分だったころの動きをそのまま一般化したもので、
// 1h なら毎時00分、15m なら毎時00分・15分・30分・45分になる。
//
// time.Truncate は UTC の基準時刻からの絶対時間で丸める。
// そのため壁時計の切りの良い時刻に揃うのは、UTC とのオフセットが
// 間隔で割り切れる場合に限られる（JST は時間単位なのでほぼ該当）。
// たとえば 30 分ずれた地域で 1h を指定すると毎時 30 分の撮影になるが、
// 間隔そのものは保たれるので記録としては困らない。

// NextTick は now の次の撮影時刻を返す。
func NextTick(now time.Time, interval time.Duration) time.Time {
	return now.Truncate(interval).Add(interval)
}

// CaptureTime は記録する撮影時刻を決める。
//
// タイマーは狙った時刻 (tick) の少し前後に起きるので、通常は tick を使う。
// ファイル名が区切りに揃い、起床の数秒のずれが記録に混ざらない。
//
// スリープ復帰などで大きく遅れて起きた場合は、実際に撮れた時刻 now を使う。
// tick や最寄りの区切りへ丸めると、撮っていない時刻（過去の区切りや、
// 最寄りが先なら未来）の画像として記録することになり、
// その時刻の本来の撮影も「同名ファイルあり」で失敗する。
// ずれの許容は間隔の半分、長くても1分。
func CaptureTime(tick, now time.Time, interval time.Duration) time.Time {
	tolerance := min(interval/2, time.Minute)
	if d := now.Sub(tick); d < -tolerance || d > tolerance {
		return now
	}
	return tick
}
