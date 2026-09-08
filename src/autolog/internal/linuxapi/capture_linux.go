//go:build linux && !android

package linuxapi

import (
	"fmt"
	"image"
	"strings"
)

// screenCapturer は画面を1枚撮る手段。
type screenCapturer interface {
	Backend() string
	Capture() (*image.RGBA, error)
	Close() error
}

// Capturer は画面を撮る。
type Capturer struct {
	backend screenCapturer
}

// NewCapturer は撮影の手段を選ぶ。
//
// 順に:
//  1. 設定されたコマンド
//  2. X11（Wayland では使わない）
//  3. PATH から見つけたコマンド（grim / spectacle / gnome-screenshot）
//
// xdg-desktop-portal は既定にしない。GNOME では毎回、KDE では初回に
// 許可のダイアログが出るため、1時間おきの無人の撮影には使えない。
func NewCapturer(method, command string) (*Capturer, error) {
	env := DetectEnvironment()

	switch strings.ToLower(strings.TrimSpace(method)) {
	case "command":
		backend, err := newCommandCapturer(command)
		if err != nil {
			return nil, err
		}
		return &Capturer{backend: backend}, nil
	case "x11":
		backend, err := newX11Capturer(env.Display)
		if err != nil {
			return nil, err
		}
		return &Capturer{backend: backend}, nil
	case "", "auto":
		// 下へ。
	default:
		return nil, fmt.Errorf("%s の値が正しくない: %q", "AUTOLOG_LINUX_SCREENSHOT_METHOD", method)
	}

	if command != "" {
		backend, err := newCommandCapturer(command)
		if err != nil {
			return nil, err
		}
		return &Capturer{backend: backend}, nil
	}

	if !env.IsWayland() && env.Display != "" {
		if backend, err := newX11Capturer(env.Display); err == nil {
			return &Capturer{backend: backend}, nil
		}
	}

	backend, err := newCommandCapturer("")
	if err != nil {
		return nil, fmt.Errorf(
			"画面を撮る手段が無い (session_type=%s, compositor=%s)。"+
				"%s に撮影のコマンドを設定すること: %w",
			env.SessionType, env.Compositor, "AUTOLOG_LINUX_SCREENSHOT_COMMAND", err)
	}
	return &Capturer{backend: backend}, nil
}

// Backend は選ばれた手段の名前を返す。
func (c *Capturer) Backend() string { return c.backend.Backend() }

// Capture は全モニターを結合した1枚を返す。
func (c *Capturer) Capture() (*image.RGBA, error) { return c.backend.Capture() }

// Close は掴んだ接続を離す。
func (c *Capturer) Close() error { return c.backend.Close() }
