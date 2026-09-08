package linuxapi

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeDesktopEntry は .desktop を1つ置く。
func writeDesktopEntry(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestParseDesktopEntryReadsMainGroupOnly(t *testing.T) {
	const content = `# コメント
[Desktop Entry]
Type=Application
Name=Google Chrome
Name[ja]=Google Chrome ブラウザ
Exec=/usr/bin/google-chrome-stable %U
StartupWMClass=Google-chrome

[Desktop Action new-window]
Name=新しいウィンドウ
Exec=/usr/bin/google-chrome-stable --new-window
`
	entry := parseDesktopEntry(strings.NewReader(content))

	if entry["Name"] != "Google Chrome" {
		t.Errorf("Name = %q", entry["Name"])
	}
	if entry["Name[ja]"] != "Google Chrome ブラウザ" {
		t.Errorf("Name[ja] = %q", entry["Name[ja]"])
	}
	if entry["StartupWMClass"] != "Google-chrome" {
		t.Errorf("StartupWMClass = %q", entry["StartupWMClass"])
	}
	// 別のグループの Name で上書きされてはいけない。
	if entry["Name"] == "新しいウィンドウ" {
		t.Error("[Desktop Action] の Name を読んでしまっている")
	}
}

func TestLocaleCandidates(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{in: "ja_JP.UTF-8", want: []string{"ja_JP", "ja"}},
		{in: "ja_JP", want: []string{"ja_JP", "ja"}},
		{in: "ja", want: []string{"ja"}},
		{in: "sr_RS.UTF-8@latin", want: []string{"sr_RS@latin", "sr_RS", "sr@latin", "sr"}},
		{in: "C", want: nil},
		{in: "POSIX", want: nil},
		{in: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := localeCandidates(tt.in); !slices.Equal(got, tt.want) {
				t.Errorf("localeCandidates(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestLookupDesktopNameByFileName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "applications")
	writeDesktopEntry(t, dir, "org.gnome.Nautilus.desktop", `[Desktop Entry]
Type=Application
Name=Files
Name[ja]=ファイル
Exec=nautilus
`)

	// Wayland の app_id は .desktop の名前と揃えるのが本来の形。
	name, ok := lookupDesktopName([]string{dir}, desktopKeys{AppID: "org.gnome.Nautilus"}, []string{"ja"})
	if !ok || name != "ファイル" {
		t.Errorf("表示名 = %q, %v, want ファイル, true", name, ok)
	}

	// 言語の指定が無ければ素の Name。
	name, ok = lookupDesktopName([]string{dir}, desktopKeys{AppID: "org.gnome.Nautilus"}, nil)
	if !ok || name != "Files" {
		t.Errorf("表示名 = %q, %v, want Files, true", name, ok)
	}
}

func TestLookupDesktopNameIgnoresCase(t *testing.T) {
	// X11 の WM_CLASS は先頭が大文字になることが多い。
	dir := filepath.Join(t.TempDir(), "applications")
	writeDesktopEntry(t, dir, "code.desktop", `[Desktop Entry]
Type=Application
Name=Visual Studio Code
Exec=/usr/share/code/code
`)

	name, ok := lookupDesktopName([]string{dir}, desktopKeys{AppID: "Code"}, nil)
	if !ok || name != "Visual Studio Code" {
		t.Errorf("表示名 = %q, %v, want Visual Studio Code, true", name, ok)
	}
}

func TestLookupDesktopNameByStartupWMClass(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "applications")
	writeDesktopEntry(t, dir, "google-chrome.desktop", `[Desktop Entry]
Type=Application
Name=Google Chrome
Exec=/usr/bin/google-chrome-stable %U
StartupWMClass=Google-chrome
`)

	name, ok := lookupDesktopName([]string{dir}, desktopKeys{AppID: "google-chrome", Class: "Google-chrome"}, nil)
	if !ok || name != "Google Chrome" {
		t.Errorf("表示名 = %q, %v, want Google Chrome, true", name, ok)
	}
}

func TestLookupDesktopNameByExec(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "applications")
	writeDesktopEntry(t, dir, "org.example.Term.desktop", `[Desktop Entry]
Type=Application
Name=端末
Exec=/usr/bin/xterm -e $SHELL
`)

	name, ok := lookupDesktopName([]string{dir}, desktopKeys{AppID: "XTerm", ExecName: "xterm"}, nil)
	if !ok || name != "端末" {
		t.Errorf("表示名 = %q, %v, want 端末, true", name, ok)
	}
}

func TestLookupDesktopNameReturnsNothingWhenUnknown(t *testing.T) {
	// 引けなければ空。推測して埋めない（要件 §6.4 は言い換えを禁じている）。
	dir := filepath.Join(t.TempDir(), "applications")
	writeDesktopEntry(t, dir, "other.desktop", `[Desktop Entry]
Type=Application
Name=別のアプリ
Exec=other
`)

	if name, ok := lookupDesktopName([]string{dir}, desktopKeys{AppID: "unknown-app", ExecName: "unknown-app"}, nil); ok {
		t.Errorf("引けないはずが %q を返した", name)
	}
}

func TestLookupDesktopNamePrefersEarlierDir(t *testing.T) {
	// 利用者のディレクトリがシステムより先に来る。
	root := t.TempDir()
	userDir := filepath.Join(root, "user", "applications")
	systemDir := filepath.Join(root, "system", "applications")
	writeDesktopEntry(t, userDir, "app.desktop", "[Desktop Entry]\nType=Application\nName=利用者の設定\nExec=app\n")
	writeDesktopEntry(t, systemDir, "app.desktop", "[Desktop Entry]\nType=Application\nName=システムの設定\nExec=app\n")

	name, ok := lookupDesktopName([]string{userDir, systemDir}, desktopKeys{AppID: "app"}, nil)
	if !ok || name != "利用者の設定" {
		t.Errorf("表示名 = %q, %v, want 利用者の設定, true", name, ok)
	}
}

func TestLookupDesktopNameWithoutDirs(t *testing.T) {
	if _, ok := lookupDesktopName(nil, desktopKeys{AppID: "app"}, nil); ok {
		t.Error("探索先が無いのに引けたことになっている")
	}
}
