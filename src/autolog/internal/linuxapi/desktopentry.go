package linuxapi

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// desktopNameCache は .desktop から引いた表示名を覚えておく。
//
// 前面ウィンドウの取得は1秒ごとに走る。そのたびにディレクトリを
// 走査し直すと重い。**引けなかったことも覚える**（winapi の
// FileDescription のキャッシュと同じ形）。
type desktopNameCache struct {
	mu      sync.Mutex
	dirs    []string
	locales []string
	names   map[string]string
}

// newDesktopNameCache は探索先と言語を環境から決めて覚え書きを作る。
func newDesktopNameCache() *desktopNameCache {
	return &desktopNameCache{
		dirs:    desktopDirs(),
		locales: localeCandidates(currentLocale()),
		names:   map[string]string{},
	}
}

// lookup は表示名を引く。引けなければ空文字。
func (c *desktopNameCache) lookup(keys desktopKeys) string {
	key := keys.AppID + "\x00" + keys.Class + "\x00" + keys.ExecName
	if key == "\x00\x00" {
		return ""
	}

	c.mu.Lock()
	cached, ok := c.names[key]
	c.mu.Unlock()
	if ok {
		return cached
	}

	name, _ := lookupDesktopName(c.dirs, keys, c.locales)

	c.mu.Lock()
	c.names[key] = name
	c.mu.Unlock()
	return name
}

// desktopKeys は .desktop を引くための手がかり。
type desktopKeys struct {
	// AppID は Wayland の app_id、または X11 の WM_CLASS の1つ目。
	AppID string
	// Class は X11 の WM_CLASS の2つ目。
	Class string
	// ExecName は実行ファイル名。
	ExecName string
}

// parseDesktopEntry は .desktop の [Desktop Entry] を読む。
//
// キーはローカライズの接尾辞を含めたまま返す（例: "Name[ja]"）。
// 他のグループ（[Desktop Action ...]）は読まない。
func parseDesktopEntry(r io.Reader) map[string]string {
	entry := map[string]string{}

	var inMainGroup bool
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inMainGroup = line == "[Desktop Entry]"
			continue
		}
		if !inMainGroup {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		entry[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return entry
}

// localeCandidates は言語の指定から Name の接尾辞の候補を優先順に返す。
//
// Desktop Entry の仕様どおり lang_COUNTRY@MODIFIER → lang_COUNTRY →
// lang@MODIFIER → lang の順。文字符号 (.UTF-8) は候補に使わない。
func localeCandidates(lang string) []string {
	lang = strings.TrimSpace(lang)
	// 文字符号を落とす。
	if index := strings.Index(lang, "."); index >= 0 {
		if at := strings.Index(lang[index:], "@"); at >= 0 {
			lang = lang[:index] + lang[index+at:]
		} else {
			lang = lang[:index]
		}
	}
	if lang == "" || lang == "C" || lang == "POSIX" {
		return nil
	}

	base, modifier, hasModifier := strings.Cut(lang, "@")
	language, _, hasCountry := strings.Cut(base, "_")

	var candidates []string
	if hasModifier {
		candidates = append(candidates, base+"@"+modifier)
	}
	candidates = append(candidates, base)
	if hasCountry {
		if hasModifier {
			candidates = append(candidates, language+"@"+modifier)
		}
		candidates = append(candidates, language)
	}
	return candidates
}

// currentLocale は表示に使う言語を返す。
func currentLocale() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// localizedName は言語に合った Name を返す。
func localizedName(entry map[string]string, locales []string) string {
	for _, locale := range locales {
		if name := entry["Name["+locale+"]"]; name != "" {
			return name
		}
	}
	return entry["Name"]
}

// lookupDesktopName は .desktop からアプリの表示名を引く。
//
// winapi の FileDescription に当たるもの。ELF にはバージョン情報が無いので、
// 要件 §6.4 の「表示上のアプリ名」を持っているのは .desktop だけ。
//
// 引けなければ空文字を返す。**推測して埋めない。**
// WM_CLASS の "Google-chrome" を "Google Chrome" に整形するような
// 言い換えは要件 §6.4 が禁じている。
func lookupDesktopName(dirs []string, keys desktopKeys, locales []string) (string, bool) {
	files := listDesktopFiles(dirs)
	if len(files) == 0 {
		return "", false
	}

	candidates := make([]string, 0, 3)
	for _, key := range []string{keys.AppID, keys.Class, keys.ExecName} {
		if key != "" {
			candidates = append(candidates, key)
		}
	}

	// 1. ファイル名の完全一致。app_id は .desktop の名前と揃えるのが本来の形。
	for _, candidate := range candidates {
		for _, file := range files {
			if filepath.Base(file) == candidate+".desktop" {
				if name := desktopNameOf(file, locales); name != "" {
					return name, true
				}
			}
		}
	}

	// 2. 大小を無視した一致。WM_CLASS は先頭が大文字になることが多い。
	for _, candidate := range candidates {
		for _, file := range files {
			if strings.EqualFold(filepath.Base(file), candidate+".desktop") {
				if name := desktopNameOf(file, locales); name != "" {
					return name, true
				}
			}
		}
	}

	// 3. StartupWMClass と Exec で引く。ここだけは中身を読んで探す。
	for _, file := range files {
		entry := readDesktopEntry(file)
		if entry == nil || entry["Type"] != "Application" {
			continue
		}
		if matchesWMClass(entry, keys) || matchesExec(entry, keys.ExecName) {
			if name := localizedName(entry, locales); name != "" {
				return name, true
			}
		}
	}

	return "", false
}

// matchesWMClass は StartupWMClass が手がかりと一致するかを返す。
func matchesWMClass(entry map[string]string, keys desktopKeys) bool {
	wmClass := entry["StartupWMClass"]
	if wmClass == "" {
		return false
	}
	return strings.EqualFold(wmClass, keys.AppID) || strings.EqualFold(wmClass, keys.Class)
}

// matchesExec は Exec の先頭の実行ファイル名が一致するかを返す。
func matchesExec(entry map[string]string, execName string) bool {
	if execName == "" {
		return false
	}
	fields := strings.Fields(entry["Exec"])
	if len(fields) == 0 {
		return false
	}
	// env や flatpak 経由の起動は先頭が実行ファイルにならないので当たらない。
	// 当たらないときは引かないだけで、推測はしない。
	return filepath.Base(fields[0]) == execName
}

// desktopNameOf は1つの .desktop から表示名を読む。
func desktopNameOf(path string, locales []string) string {
	entry := readDesktopEntry(path)
	if entry == nil {
		return ""
	}
	return localizedName(entry, locales)
}

// readDesktopEntry は .desktop を読む。読めなければ nil。
func readDesktopEntry(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	return parseDesktopEntry(file)
}

// listDesktopFiles は探索先の .desktop を並べる。先に来たディレクトリが優先。
func listDesktopFiles(dirs []string) []string {
	var files []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".desktop") {
				continue
			}
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files
}

// desktopDirs は .desktop を探す場所を返す。
//
// XDG の仕様どおり環境変数から導く。Flatpak や Snap の置き場も
// XDG_DATA_DIRS へ入るので、個別に決め打ちしない。
func desktopDirs() []string {
	var dirs []string

	home := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".local", "share")
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "applications"))
	}

	dataDirs := strings.TrimSpace(os.Getenv("XDG_DATA_DIRS"))
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for dir := range strings.SplitSeq(dataDirs, ":") {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			dirs = append(dirs, filepath.Join(trimmed, "applications"))
		}
	}
	return dirs
}
