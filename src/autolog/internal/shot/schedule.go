package shot

import "time"

// 撮影時刻の決め方。
//
// 撮影は「間隔ぶん経ったら撮る」ではなく「間隔で割った境目に撮る」。
// 毎時00分だったころの動きをそのまま一般化したもので、
// 1h なら毎時00分、15m なら毎時00分・15分・30分・45分になる。
//
// time.Truncate は UTC の基準時刻からの絶対時間で丸める。
// そのため壁時計の切りの良い時刻に揃うのは、1時間を割り切る間隔か、
// 時間単位の間隔で、かつ UTC との差が時間単位の地域（JST は該当）に限られる。
// 30分ずれた地域で 15m を指定すると :07 :22 のような時刻になるが、
// 間隔そのものは保たれるので記録としては困らない。

// NextTick は now の次の撮影時刻を返す。
func NextTick(now time.Time, interval time.Duration) time.Time {
	return now.Truncate(interval).Add(interval)
}

// RoundToTick は最寄りの撮影時刻へ丸める。
//
// タイマーは狙った時刻より少し前に起きることがある。
// 1時間間隔なら 59 分台に起きることがあり、そのまま切り捨てると
// ひとつ前の撮影時刻になってしまう。半分ずらしてから切り捨てて、
// 近いほうの境目に寄せる。
func RoundToTick(now time.Time, interval time.Duration) time.Time {
	return now.Add(interval / 2).Truncate(interval)
}
