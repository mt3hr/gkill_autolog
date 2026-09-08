//go:build windows

package shot

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"image"
	"log/slog"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/config"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/winapi"
)

func init() {
	newCapturer = func(_ *config.Config, _ *slog.Logger) (capturer, error) {
		return windowsCapturer{}, nil
	}
}

// windowsCapturer は GDI で仮想スクリーン全体を撮る。
type windowsCapturer struct{}

// Locked は画面がロックされているかを返す。
// OpenInputDesktop が失敗するかどうかで判定する。
func (windowsCapturer) Locked() bool { return winapi.IsSessionLocked() }

// Capture は全モニターを1回の BitBlt で1枚に収める。
func (windowsCapturer) Capture() (*image.RGBA, error) { return winapi.CaptureVirtualScreen() }

func (windowsCapturer) Close() error { return nil }
