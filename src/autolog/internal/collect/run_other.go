//go:build !windows

package collect

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// Run は Windows 以外では使えない。
// Android の収集は android/ の Kotlin アプリが担当し、
// 収集済みイベントは共有ディレクトリの JSONL (inbox パッケージ) 経由で受け取る。
func Run(_ context.Context, _ *rawlog.Store, _ rawlog.Device, _ *slog.Logger) error {
	return fmt.Errorf("autolog collect is only supported on windows (running on %s)", runtime.GOOS)
}
