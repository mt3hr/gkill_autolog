package linuxapi

import "testing"

// keysWith は指定したビットだけを立てたキーの状態を作る。
func keysWith(bits ...int) [32]byte {
	var keys [32]byte
	for _, bit := range bits {
		keys[bit/8] |= 1 << (bit % 8)
	}
	return keys
}

func TestPressedSince(t *testing.T) {
	tests := []struct {
		name       string
		oldKeys    [32]byte
		newKeys    [32]byte
		oldButtons uint16
		newButtons uint16
		want       []InputKind
	}{
		{
			name:    "キーを押した",
			newKeys: keysWith(38),
			want:    []InputKind{InputKey},
		},
		{
			name:    "押しっぱなしは数えない",
			oldKeys: keysWith(38),
			newKeys: keysWith(38),
			want:    nil,
		},
		{
			name:    "離しただけは数えない",
			oldKeys: keysWith(38),
			newKeys: [32]byte{},
			want:    nil,
		},
		{
			name:       "左クリック",
			newButtons: x11Button1Mask,
			want:       []InputKind{InputClick},
		},
		{
			name:       "ホイール",
			newButtons: x11Button5Mask,
			want:       []InputKind{InputWheel},
		},
		{
			name:       "ボタンを離しただけは数えない",
			oldButtons: x11Button1Mask,
			newButtons: 0,
			want:       nil,
		},
		{
			// 要件 §6.2: マウス移動だけでは操作扱いにしない。
			// 修飾キーのビット (下位8ビット) が動いてもボタンではない。
			name:       "修飾キーのビットはクリックではない",
			oldButtons: 0,
			newButtons: 0x01,
			want:       nil,
		},
		{
			name:       "キーとクリックが同時",
			newKeys:    keysWith(38),
			newButtons: x11Button1Mask,
			want:       []InputKind{InputKey, InputClick},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pressedSince(tt.oldKeys, tt.newKeys, tt.oldButtons, tt.newButtons)
			if len(got) != len(tt.want) {
				t.Fatalf("数えた種別 = %v, want %v", got, tt.want)
			}
			for _, kind := range tt.want {
				if _, ok := got[kind]; !ok {
					t.Errorf("%q を数えていない", kind)
				}
			}
		})
	}
}
