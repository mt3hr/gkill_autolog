package linuxapi

import "testing"

func TestLockStateDropsRepeatedTransitions(t *testing.T) {
	// logind と ScreenSaver の両方から同じ操作の通知が来る。
	// そのまま流すと lock が2件並び、normalize が同じ区間を二重に閉じる。
	var s lockState

	if kind, ok := s.apply(true); !ok || kind != SessionLock {
		t.Fatalf("最初のロック = %q, %v, want lock, true", kind, ok)
	}
	if kind, ok := s.apply(true); ok {
		t.Errorf("同じ状態への遷移で %q を記録した, want 記録しない", kind)
	}
	if kind, ok := s.apply(false); !ok || kind != SessionUnlock {
		t.Fatalf("解除 = %q, %v, want unlock, true", kind, ok)
	}
	if _, ok := s.apply(false); ok {
		t.Error("同じ状態への解除を記録した, want 記録しない")
	}
}

func TestLockStateSeedDoesNotEmit(t *testing.T) {
	// 起動時に現在の状態を置くだけ。ここで記録すると、
	// 収集を始めただけでロック・解除が生ログに載る。
	var s lockState
	s.seed(true)

	if locked, known := s.get(); !known || !locked {
		t.Fatalf("種を置いた後の状態 = %v, %v, want true, true", locked, known)
	}
	if kind, ok := s.apply(true); ok {
		t.Errorf("種と同じ状態で %q を記録した, want 記録しない", kind)
	}
	if kind, ok := s.apply(false); !ok || kind != SessionUnlock {
		t.Errorf("種のあとの解除 = %q, %v, want unlock, true", kind, ok)
	}
}

func TestLockStateUnknownUntilObserved(t *testing.T) {
	// 一度も観測していなければ「分かっていない」。
	// 分からないまま「ロックされていない」と答えると、
	// ロック画面のスクリーンショットが撮られる。
	var s lockState
	if _, known := s.get(); known {
		t.Error("観測前なのに状態が分かっていることになっている")
	}

	s.apply(false)
	if locked, known := s.get(); !known || locked {
		t.Errorf("解除を観測した後 = %v, %v, want false, true", locked, known)
	}

	s.reset()
	if _, known := s.get(); known {
		t.Error("監視が切れたのに状態が分かっていることになっている")
	}
}

func TestLockStateFirstObservationAlwaysEmits(t *testing.T) {
	// 種を置いていなければ、最初の観測は必ず記録する。
	// 収集の開始前からロックされていた場合の解除を取りこぼさない。
	var s lockState
	if kind, ok := s.apply(false); !ok || kind != SessionUnlock {
		t.Errorf("最初の観測 = %q, %v, want unlock, true", kind, ok)
	}
}
