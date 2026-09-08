package linuxapi

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"slices"
	"testing"
)

func TestBuildCaptureArgs(t *testing.T) {
	tests := []struct {
		name       string
		template   []string
		want       []string
		wantStdout bool
	}{
		{
			name:     "出力先を置き換える",
			template: []string{"grim", "{}"},
			want:     []string{"grim", "/tmp/shot.png"}, wantStdout: false,
		},
		{
			name:     "引数の一部でも置き換える",
			template: []string{"spectacle", "-b", "-n", "-o", "{}"},
			want:     []string{"spectacle", "-b", "-n", "-o", "/tmp/shot.png"}, wantStdout: false,
		},
		{
			name:     "くっついた形も置き換える",
			template: []string{"tool", "--out={}"},
			want:     []string{"tool", "--out=/tmp/shot.png"}, wantStdout: false,
		},
		{
			name:     "印が無ければ標準出力から読む",
			template: []string{"grim", "-"},
			want:     []string{"grim", "-"}, wantStdout: true,
		},
		{
			name:     "印が複数あればすべて置き換える",
			template: []string{"tool", "{}", "{}"},
			want:     []string{"tool", "/tmp/shot.png", "/tmp/shot.png"}, wantStdout: false,
		},
		{
			name:     "空のひな型",
			template: nil,
			want:     []string{}, wantStdout: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, useStdout := buildCaptureArgs(tt.template, "/tmp/shot.png")
			if !slices.Equal(argv, tt.want) {
				t.Errorf("引数 = %v, want %v", argv, tt.want)
			}
			if useStdout != tt.wantStdout {
				t.Errorf("標準出力から読むか = %v, want %v", useStdout, tt.wantStdout)
			}
		})
	}
}

func TestDecodeCapturedImage(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	source.Set(0, 0, color.NRGBA{R: 255, A: 255})
	source.Set(2, 1, color.NRGBA{B: 255, A: 255})

	t.Run("PNG", func(t *testing.T) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, source); err != nil {
			t.Fatalf("png.Encode: %v", err)
		}

		got, err := decodeCapturedImage(&buf)
		if err != nil {
			t.Fatalf("decodeCapturedImage: %v", err)
		}
		if got.Bounds().Dx() != 3 || got.Bounds().Dy() != 2 {
			t.Errorf("大きさ = %v, want 3x2", got.Bounds())
		}
		r, _, _, _ := got.At(0, 0).RGBA()
		if r == 0 {
			t.Error("色が失われている")
		}
	})

	t.Run("JPEG", func(t *testing.T) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, source, nil); err != nil {
			t.Fatalf("jpeg.Encode: %v", err)
		}
		if _, err := decodeCapturedImage(&buf); err != nil {
			t.Errorf("decodeCapturedImage: %v", err)
		}
	})

	t.Run("画像でないものは読めない", func(t *testing.T) {
		if _, err := decodeCapturedImage(bytes.NewReader([]byte("これは画像ではない"))); err == nil {
			t.Error("画像でないものを読めたことになっている")
		}
	})
}
