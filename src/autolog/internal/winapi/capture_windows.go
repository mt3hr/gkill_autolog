//go:build windows

package winapi

import (
	"fmt"
	"image"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	gdi32 = windows.NewLazySystemDLL("gdi32.dll")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")

	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srcCopy      = 0x00CC0020
	biRGB        = 0
	dibRGBColors = 0

	// dpiAwarenessContextPerMonitorAwareV2 は DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2。
	dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // (HANDLE)-4
)

// bitmapInfoHeader は BITMAPINFOHEADER (40 バイト)。
type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

// bitmapInfo は BITMAPINFO。32bpp では色テーブルを使わないが、
// GetDIBits が書き込む可能性があるので領域だけ確保しておく。
type bitmapInfo struct {
	header bitmapInfoHeader
	colors [3]uint32
}

var dpiOnce sync.Once

// ensureDPIAware はプロセスを DPI 対応にする。
//
// 対応させないと、高 DPI 環境で GetSystemMetrics が拡大率で割った論理サイズを返し、
// 実際の画面より小さく引き伸ばされた画像になる。
func ensureDPIAware() {
	dpiOnce.Do(func() {
		// Windows 10 1703 以降。モニタごとに拡大率が違っても実ピクセルで取れる。
		if err := procSetProcessDpiAwarenessContext.Find(); err == nil {
			if ok, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2); ok != 0 {
				return
			}
		}
		procSetProcessDPIAware.Call()
	})
}

// CaptureVirtualScreen は全モニタを結合した1枚の画像を返す。
//
// 仮想スクリーン全体を1回の BitBlt で取るため、モニタの配置がそのまま反映される。
// モニタが長方形に並んでいない場合、隙間は黒のままになる。
func CaptureVirtualScreen() (*image.RGBA, error) {
	ensureDPIAware()

	left, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	top, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	width, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	height, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)

	if int32(width) <= 0 || int32(height) <= 0 {
		return nil, fmt.Errorf("unexpected virtual screen size %dx%d", int32(width), int32(height))
	}

	screenDC, _, err := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("failed to get screen device context: %w", err)
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, err := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("failed to create memory device context: %w", err)
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, err := procCreateCompatibleBitmap.Call(screenDC, width, height)
	if bitmap == 0 {
		return nil, fmt.Errorf("failed to create bitmap: %w", err)
	}
	defer procDeleteObject.Call(bitmap)

	previous, _, _ := procSelectObject.Call(memDC, bitmap)
	if previous == 0 {
		return nil, fmt.Errorf("failed to select bitmap into device context")
	}
	defer procSelectObject.Call(memDC, previous)

	ok, _, err := procBitBlt.Call(memDC, 0, 0, width, height, screenDC, left, top, srcCopy)
	if ok == 0 {
		return nil, fmt.Errorf("failed to copy screen: %w", err)
	}

	// biHeight を負にしてトップダウン（左上が先頭）で受け取る。
	info := bitmapInfo{header: bitmapInfoHeader{
		biSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		biWidth:       int32(width),
		biHeight:      -int32(height),
		biPlanes:      1,
		biBitCount:    32,
		biCompression: biRGB,
	}}

	pixels := make([]byte, int(width)*int(height)*4)
	copied, _, err := procGetDIBits.Call(
		memDC,
		bitmap,
		0,
		height,
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
	)
	if copied == 0 {
		return nil, fmt.Errorf("failed to read bitmap bits: %w", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	// GDI は BGRA で返す。アルファは信用できないので不透明で埋める。
	for i := 0; i < len(pixels); i += 4 {
		img.Pix[i+0] = pixels[i+2]
		img.Pix[i+1] = pixels[i+1]
		img.Pix[i+2] = pixels[i+0]
		img.Pix[i+3] = 0xFF
	}
	return img, nil
}
