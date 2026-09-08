package linuxapi

// X11 のポインタの状態に入るボタンのビット。
const (
	x11Button1Mask uint16 = 1 << 8  // 左
	x11Button2Mask uint16 = 1 << 9  // 中
	x11Button3Mask uint16 = 1 << 10 // 右
	x11Button4Mask uint16 = 1 << 11 // ホイール上
	x11Button5Mask uint16 = 1 << 12 // ホイール下
)

// pressedSince は前回から新しく押されたものの種別を返す。
//
// 離した側は数えない。押された瞬間だけを操作として扱う。
func pressedSince(oldKeys, newKeys [32]byte, oldButtons, newButtons uint16) map[InputKind]struct{} {
	kinds := map[InputKind]struct{}{}

	for i := range newKeys {
		if newKeys[i]&^oldKeys[i] != 0 {
			kinds[InputKey] = struct{}{}
			break
		}
	}

	pressed := newButtons &^ oldButtons
	if pressed&(x11Button1Mask|x11Button2Mask|x11Button3Mask) != 0 {
		kinds[InputClick] = struct{}{}
	}
	if pressed&(x11Button4Mask|x11Button5Mask) != 0 {
		kinds[InputWheel] = struct{}{}
	}
	return kinds
}
