package linuxapi

import (
	"fmt"
	"image"
	"image/draw"
	"io"
	"strings"

	// 撮影コマンドが吐く形式を読めるようにする。
	_ "image/jpeg"
	_ "image/png"
)

// captureOutputToken は撮影コマンドの引数のうち、出力先へ置き換える印。
const captureOutputToken = "{}"

// buildCaptureArgs は撮影コマンドの引数を組み立てる。
//
// 値は空白で分けた argv として扱い、sh -c は通さない。
// シェルの解釈の違いを持ち込まないためで、込み入ったことをしたい場合は
// ラッパのスクリプトを書いて設定でそれを指す。
//
// {} を出力先のパスへ置き換える。{} が無ければ標準出力から読む
// （grim - のような使い方）。
func buildCaptureArgs(template []string, outPath string) (argv []string, useStdout bool) {
	argv = make([]string, 0, len(template))
	useStdout = true
	for _, arg := range template {
		if strings.Contains(arg, captureOutputToken) {
			useStdout = false
			arg = strings.ReplaceAll(arg, captureOutputToken, outPath)
		}
		argv = append(argv, arg)
	}
	return argv, useStdout
}

// decodeCapturedImage は撮影コマンドの出した画像を読む。
//
// PNG と JPEG を読む。WebP への変換は shot.Save が行うので、
// ここでは *image.RGBA まで戻せばよい。
func decodeCapturedImage(r io.Reader) (*image.RGBA, error) {
	decoded, format, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("撮影した画像を読めない: %w", err)
	}

	if rgba, ok := decoded.(*image.RGBA); ok {
		return rgba, nil
	}

	bounds := decoded.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, fmt.Errorf("撮影した画像の大きさが正しくない (%s)", format)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), decoded, bounds.Min, draw.Src)
	return rgba, nil
}
