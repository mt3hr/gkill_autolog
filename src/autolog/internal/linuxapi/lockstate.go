package linuxapi

import "sync"

// lockState は画面ロックの状態を1つに保つ。
//
// ロックの通知は複数の経路から来る。logind の LockedHint と
// org.freedesktop.ScreenSaver の ActiveChanged は同じ操作で両方とも飛ぶので、
// そのまま流すと lock が2件並ぶ。同じ状態への遷移は捨てる。
//
// スクリーンショットのロック判定 (shot) もこの状態を見る。collect が
// 動いていれば SessionWatcher が更新し続けているため、撮影のたびに
// D-Bus を叩き直さずに済む。
type lockState struct {
	mu     sync.Mutex
	known  bool
	locked bool
}

// sharedLockState は collect と shot で共有する現在のロック状態。
//
// 同じプロセスで両方が動く（cmd_collect.go が1つの errgroup で回す）ので、
// パッケージ変数で共有できる。
var sharedLockState lockState

// seed は初期状態を置く。イベントは出さない。
//
// 起動時に logind と ScreenSaver へ現在の状態を聞いて置く。
// ここでイベントを出すと、収集を始めただけでロック・解除が記録される。
func (s *lockState) seed(locked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.known = true
	s.locked = locked
}

// apply は新しい観測を反映し、記録すべき変化を返す。
//
// 2つ目の戻り値が false なら記録しない（状態が変わっていない）。
func (s *lockState) apply(locked bool) (SessionEventKind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.known && s.locked == locked {
		return "", false
	}
	s.known = true
	s.locked = locked
	if locked {
		return SessionLock, true
	}
	return SessionUnlock, true
}

// get は現在のロック状態を返す。2つ目の戻り値が false なら分かっていない。
func (s *lockState) get() (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked, s.known
}

// reset は状態を「分かっていない」へ戻す。監視が切れたときに使う。
func (s *lockState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.known = false
	s.locked = false
}
