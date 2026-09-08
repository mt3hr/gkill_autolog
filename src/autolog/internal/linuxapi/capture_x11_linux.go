//go:build linux && !android

package linuxapi

import (
	"fmt"
	"image"

	"github.com/jezek/xgb/xproto"
)

// x11ReplyHeaderSize は応答の固定部分。帯の大きさを決めるときに差し引く。
const x11ReplyHeaderSize = 32

// x11Capturer は X11 の画面を1枚に撮る。
type x11Capturer struct {
	conn *x11Conn
}

func newX11Capturer(display string) (*x11Capturer, error) {
	if display == "" {
		return nil, x11DisplayRequired()
	}
	conn, err := newX11Conn(display)
	if err != nil {
		return nil, err
	}
	return &x11Capturer{conn: conn}, nil
}

func (c *x11Capturer) Backend() string { return "x11" }

func (c *x11Capturer) Close() error {
	c.conn.Close()
	return nil
}

// Capture は全モニターを結合した1枚を返す（要件 §10）。
//
// root ウィンドウは Xinerama / RandR の環境で全モニターを覆っているので、
// そのまま撮れば結合した1枚になる。
func (c *x11Capturer) Capture() (*image.RGBA, error) {
	setup := xproto.Setup(c.conn.conn)
	if setup == nil {
		return nil, fmt.Errorf("X サーバの情報を読めない")
	}

	width := int(c.conn.screen.WidthInPixels)
	height := int(c.conn.screen.HeightInPixels)
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("画面の大きさを読めない (%dx%d)", width, height)
	}

	bitsPerPixel, err := x11BitsPerPixel(setup, c.conn.screen.RootDepth)
	if err != nil {
		return nil, err
	}
	// 32bpp 以外は並びを推測しない。推測して撮ると色の壊れた画像が
	// 毎時保存され続け、あとから直しようがない。
	if bitsPerPixel != 32 {
		return nil, fmt.Errorf("1画素 %d ビットの画面には対応していない", bitsPerPixel)
	}
	bytesPerPixel := bitsPerPixel / 8

	// 応答は1回あたりの上限に収まる必要がある。4K や複数モニタでは必ず超える。
	maxReplyBytes := int(setup.MaximumRequestLength)*4 - x11ReplyHeaderSize
	stripes := imageStripes(width, height, maxReplyBytes, bytesPerPixel)
	if len(stripes) == 0 {
		return nil, fmt.Errorf("画面を読み取る範囲を決められない")
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for _, s := range stripes {
		reply, err := xproto.GetImage(c.conn.conn, xproto.ImageFormatZPixmap,
			xproto.Drawable(c.conn.root), 0, int16(s.Y), uint16(width), uint16(s.Height),
			0xffffffff).Reply()
		if err != nil {
			return nil, fmt.Errorf("画面を読み取れない (y=%d, h=%d): %w", s.Y, s.Height, err)
		}
		if err := copyZPixmapStripe(img, reply.Data, s, width, bytesPerPixel); err != nil {
			return nil, err
		}
	}
	return img, nil
}

// copyZPixmapStripe は読み取った帯を画像へ写す。
//
// ZPixmap の 32bpp はバイト順が B, G, R, 未使用。透過は入っていないので
// 不透明で埋める（winapi の BGRA→RGBA と同じ扱い）。
func copyZPixmapStripe(img *image.RGBA, data []byte, s stripe, width, bytesPerPixel int) error {
	need := width * s.Height * bytesPerPixel
	if len(data) < need {
		return fmt.Errorf("画面の読み取りが足りない (%d バイト, want %d)", len(data), need)
	}

	for row := range s.Height {
		src := row * width * bytesPerPixel
		dst := img.PixOffset(0, s.Y+row)
		for x := range width {
			b := data[src+x*bytesPerPixel+0]
			g := data[src+x*bytesPerPixel+1]
			r := data[src+x*bytesPerPixel+2]
			img.Pix[dst+x*4+0] = r
			img.Pix[dst+x*4+1] = g
			img.Pix[dst+x*4+2] = b
			img.Pix[dst+x*4+3] = 0xff
		}
	}
	return nil
}

// x11BitsPerPixel は画面の深さに対応する1画素のビット数を返す。
func x11BitsPerPixel(setup *xproto.SetupInfo, depth byte) (int, error) {
	for _, format := range setup.PixmapFormats {
		if format.Depth == depth {
			return int(format.BitsPerPixel), nil
		}
	}
	return 0, fmt.Errorf("深さ %d に対応する画素の形式が無い", depth)
}
