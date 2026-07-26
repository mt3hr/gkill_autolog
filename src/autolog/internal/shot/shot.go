// Package shot は毎時のスクリーンショットを撮って保存する。
//
// 撮った画像は Claude へ渡さない。保存先を gkill の IDF リポジトリとして
// 取り込ませることで Kyou になる（要件 §10）。
//
// gkill の IDF は登録時のファイル mtime を RelatedTime にするため、
// 保存後に必ず mtime を撮影時刻へ合わせる。
package shot

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"github.com/HugoSmits86/nativewebp"
)

// ErrLocked は画面がロックされていて撮影しなかったことを表す。
var ErrLocked = errors.New("screen is locked")

// Extension は保存するファイルの拡張子。
const Extension = ".webp"

// Result は撮影の結果。
type Result struct {
	// Path は保存したファイルのパス。
	Path string
	// CapturedAt は撮影時刻。ファイルの mtime もこの値に合わせてある。
	CapturedAt time.Time
	// Width, Height は画像の大きさ。
	Width, Height int
	// Bytes は保存したファイルの大きさ。
	Bytes int64
}

// Save は画像を WebP として保存し、mtime を撮影時刻へ合わせる。
//
// dir は置き場（例: $AUTOLOG_HOME/screenshots）。日付では分けない。
// 撮り逃した時間の画像を後から補完してはならないため、
// 同じ名前のファイルが既にある場合は上書きせずエラーにする。
func Save(img *image.RGBA, dir, fileName string, capturedAt time.Time) (*Result, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, fileName)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("screenshot already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to check %s: %w", path, err)
	}

	// 書き込み途中のファイルを gkill の IDF 走査に拾われないよう、
	// 一時ファイルへ書いてから置き換える。
	tempPath := path + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", tempPath, err)
	}

	if err := nativewebp.Encode(file, img, nil); err != nil {
		return nil, errors.Join(
			fmt.Errorf("failed to encode webp: %w", err),
			file.Close(),
			os.Remove(tempPath),
		)
	}
	if err := file.Close(); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to close %s: %w", tempPath, err), os.Remove(tempPath))
	}
	if err := os.Rename(tempPath, path); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to rename %s: %w", tempPath, err), os.Remove(tempPath))
	}

	// gkill の IDF は mtime を RelatedTime にする。
	// 実際の撮影時刻にしておかないと、取り込んだ時刻が記録されてしまう。
	if err := os.Chtimes(path, capturedAt, capturedAt); err != nil {
		return nil, fmt.Errorf("failed to set modification time of %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", path, err)
	}

	bounds := img.Bounds()
	return &Result{
		Path:       path,
		CapturedAt: capturedAt,
		Width:      bounds.Dx(),
		Height:     bounds.Dy(),
		Bytes:      info.Size(),
	}, nil
}
