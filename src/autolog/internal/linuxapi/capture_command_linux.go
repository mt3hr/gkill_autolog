//go:build linux && !android

package linuxapi

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// captureCmdWait は撮影コマンドの待ち時間。
// 画面が大きいと時間がかかるので、入力の観測より長く取る。
const captureCmdWait = 30 * time.Second

// screenshotCommandCandidates は撮影コマンドの自動検出の候補。
//
// 決め打ちではなく「PATH に何があるか」という環境からの導出で、
// AUTOLOG_LINUX_SCREENSHOT_COMMAND を設定すれば完全に差し替えられる。
// どれも「全画面を1枚」で撮る指定にしてある（要件 §10）。
var screenshotCommandCandidates = [][]string{
	{"grim", "{}"}, // wlroots (sway / Hyprland / river)
	{"spectacle", "-b", "-n", "-f", "-o", "{}"}, // KDE
	{"gnome-screenshot", "-f", "{}"},            // GNOME
}

// commandCapturer は外部コマンドに撮らせる。
//
// Wayland にはクライアントから画面を読む標準の手段が無いので、
// 合成器ごとの道具に任せる。
type commandCapturer struct {
	argv []string
}

// newCommandCapturer は撮影コマンドを決める。command が空なら PATH から探す。
func newCommandCapturer(command string) (*commandCapturer, error) {
	if argv := strings.Fields(command); len(argv) > 0 {
		return &commandCapturer{argv: argv}, nil
	}

	for _, candidate := range screenshotCommandCandidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		argv := append([]string{path}, candidate[1:]...)
		return &commandCapturer{argv: argv}, nil
	}

	return nil, fmt.Errorf("画面を撮るコマンドが見つからない: %w", ErrUnavailable)
}

func (c *commandCapturer) Backend() string { return "command:" + filepath.Base(c.argv[0]) }

func (c *commandCapturer) Close() error { return nil }

// Capture はコマンドに撮らせて画像を読む。
func (c *commandCapturer) Capture() (*image.RGBA, error) {
	// 中間ファイルは一時ディレクトリへ作る。
	// スクリーンショットの置き場へ作ると gkill の IDF 走査に拾われる。
	file, err := os.CreateTemp("", "autolog-shot-*.png")
	if err != nil {
		return nil, fmt.Errorf("撮影の一時ファイルを作れない: %w", err)
	}
	outPath := file.Name()
	_ = file.Close()
	defer os.Remove(outPath)

	argv, useStdout := buildCaptureArgs(c.argv, outPath)

	ctx, cancel := context.WithTimeout(context.Background(), captureCmdWait)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if useStdout {
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("撮影のコマンドが失敗した (%s): %w", strings.TrimSpace(stderr.String()), err)
		}
		return decodeCapturedImage(bytes.NewReader(out))
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("撮影のコマンドが失敗した (%s): %w", strings.TrimSpace(stderr.String()), err)
	}

	saved, err := os.Open(outPath)
	if err != nil {
		return nil, fmt.Errorf("撮影した画像を開けない: %w", err)
	}
	defer saved.Close()
	return decodeCapturedImage(saved)
}
