//go:build android

package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// InitLocalTimezone は time.Local を端末のタイムゾーンに合わせる。
//
// Go は Android では time.Local を UTC に固定する
// (標準ライブラリ zoneinfo_android.go の initLocal が localLoc = *UTC のまま)。
// そのままだと記録した時刻がすべて +00:00 になり、
// PC 側が書いた +09:00 の行と混ざる。
//
// 生ログも gkill も時刻を文字列で持ち、その文字列で並べ替えるため、
// オフセットが混ざると並び順が時刻順と一致しなくなる。
// 絶対時刻としては正しくても、後から直すのが難しい壊れ方をする。
//
// time.LoadLocation 自体は Android でも動く（tzdata を読める）ので、
// どのタイムゾーンかを自分で決めて time.Local に入れてやればよい。
func InitLocalTimezone() error {
	name := strings.TrimSpace(os.Getenv("TZ"))
	if name == "" {
		name = androidSystemTimezone()
	}
	if name == "" {
		return fmt.Errorf("端末のタイムゾーンが分からない。TZ を設定してください")
	}

	location, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("タイムゾーン %q を読めない: %w", name, err)
	}

	time.Local = location
	return nil
}

// androidSystemTimezone は Android の設定からタイムゾーン名を取る。
//
// Android は環境変数 TZ を設定しない。bionic の libc はシステムプロパティを直接見るが、
// Go はそれを見ないので getprop で取り出す。
func androidSystemTimezone() string {
	output, err := exec.Command("getprop", "persist.sys.timezone").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
