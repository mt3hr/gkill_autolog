package linuxapi

import "testing"

func TestImageStripesCoversWholeImage(t *testing.T) {
	tests := []struct {
		name          string
		width         int
		height        int
		maxReplyBytes int
		wantStripes   int
	}{
		{
			name: "1枚に収まる", width: 100, height: 100,
			maxReplyBytes: 1 << 20, wantStripes: 1,
		},
		{
			name: "割り切れる", width: 100, height: 100,
			maxReplyBytes: 100 * 4 * 10, wantStripes: 10,
		},
		{
			name: "割り切れず最後の帯だけ短い", width: 100, height: 105,
			maxReplyBytes: 100 * 4 * 10, wantStripes: 11,
		},
		{
			name: "1行しか入らない", width: 100, height: 5,
			maxReplyBytes: 100 * 4, wantStripes: 5,
		},
		{
			name: "1行すら入らない超横長", width: 10000, height: 3,
			maxReplyBytes: 1000, wantStripes: 3,
		},
		{
			// 4K を2枚並べた仮想画面。1枚では既定の最大応答長を必ず超える。
			// 1行 30720 バイト、1回に 1048560 バイトなので 34 行ずつ、
			// 2160 行を覆うのに 64 回。
			name: "4K 2枚", width: 7680, height: 2160,
			maxReplyBytes: 262140 * 4, wantStripes: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripes := imageStripes(tt.width, tt.height, tt.maxReplyBytes, 4)
			if len(stripes) != tt.wantStripes {
				t.Fatalf("帯の数 = %d, want %d", len(stripes), tt.wantStripes)
			}

			// 帯が隙間なく、重なりなく、全体を覆っていること。
			next := 0
			for i, s := range stripes {
				if s.Y != next {
					t.Fatalf("帯[%d] の開始 = %d, want %d（隙間か重なりがある）", i, s.Y, next)
				}
				if s.Height <= 0 {
					t.Fatalf("帯[%d] の高さ = %d, want 1 以上", i, s.Height)
				}
				next += s.Height
			}
			if next != tt.height {
				t.Errorf("覆った高さ = %d, want %d", next, tt.height)
			}
		})
	}
}

func TestImageStripesEmpty(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
	}{
		{name: "高さ0", width: 100, height: 0},
		{name: "幅0", width: 0, height: 100},
		{name: "負の高さ", width: 100, height: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if stripes := imageStripes(tt.width, tt.height, 1<<20, 4); len(stripes) != 0 {
				t.Errorf("帯の数 = %d, want 0", len(stripes))
			}
		})
	}
}
