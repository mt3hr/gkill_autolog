//go:build !windows && (!linux || android)

package collect

// 編集前に読む: .claude/skills/autolog-windows-collect/SKILL.md（この領域の不変条件の正本）

import (
	"fmt"
	"log/slog"
	"runtime"
)

// newPlatformCollectors は収集に対応していないプラットフォームでは何も返さない。
//
// Android の収集は android/ の Kotlin アプリが担当し、
// 収集済みイベントは共有ディレクトリの JSONL (inbox パッケージ) 経由で受け取る。
// Termux の autolog は取り込みだけを行う。
//
// ビルドタグに !linux ではなく (!linux || android) と書いてあるのは、
// Go では GOOS=android が linux のビルドタグも満たすため。
// linux だけで分けると Android 向けビルドが Linux の収集を取り込んでしまう。
func newPlatformCollectors(_ *Emitter, _ Options, _ *slog.Logger) ([]collectorFunc, error) {
	return nil, fmt.Errorf("autolog collect is only supported on windows and linux (running on %s): %w",
		runtime.GOOS, ErrUnsupportedPlatform)
}
