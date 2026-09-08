package linuxapi

import (
	"errors"
	"testing"
)

func TestSSIDFromBytes(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    string
		wantErr error
	}{
		{name: "そのまま", in: []byte("Home"), want: "Home"},
		{name: "日本語", in: []byte("自宅"), want: "自宅"},
		{name: "末尾のNUL埋めを落とす", in: []byte("Home\x00\x00\x00"), want: "Home"},
		{name: "前後の空白を落とす", in: []byte("  Home  "), want: "Home"},
		{name: "空は未知", in: []byte(""), wantErr: ErrSSIDUnknown},
		{name: "NULだけなら未知", in: []byte("\x00\x00"), wantErr: ErrSSIDUnknown},
		{name: "文字として読めなければ未知", in: []byte{0xff, 0xfe, 0xfd}, wantErr: ErrSSIDUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ssidFromBytes(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("エラー = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ssidFromBytes: %v", err)
			}
			if got != tt.want {
				t.Errorf("SSID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSSIDCommandOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "1行", in: "Home\n", want: "Home", ok: true},
		{name: "改行なし", in: "Home", want: "Home", ok: true},
		{name: "複数行なら最初の行", in: "Home\nOther\n", want: "Home", ok: true},
		{name: "先頭の空行は飛ばす", in: "\n\nHome\n", want: "Home", ok: true},
		{name: "空なら未接続", in: "", ok: false},
		{name: "空白だけなら未接続", in: "  \n\n", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSSIDCommandOutput([]byte(tt.in))
			if ok != tt.ok {
				t.Fatalf("つながっているか = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("SSID = %q, want %q", got, tt.want)
			}
		})
	}
}
