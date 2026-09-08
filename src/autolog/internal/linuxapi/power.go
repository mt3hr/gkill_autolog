package linuxapi

import (
	"os"
	"path/filepath"
	"strings"
)

// sysfsPowerSupply は電源の情報が置かれる場所。
const sysfsPowerSupply = "/sys/class/power_supply"

// chargingFromSysfs は sysfs の電源情報から AC 接続の有無を返す。
//
// 2つ目の戻り値が false のときは判断できない。据え置き機のように
// 電源の情報を持たない機械がこれに当たる。「ずっと充電中」という
// TimeIs を作っても意味が無いので、記録しないほうを選ぶ。
//
// Windows と揃えて「充電中」ではなく **AC 接続の有無**を見る。
// 満充電で充電が止まっても区間を切らないため（要件 §9.3）。
//
// root を引数に取るのは、実機なしで検証できるようにするため。
func chargingFromSysfs(root string) (bool, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, false
	}

	var (
		mains     []string
		otherAC   []string
		batteries []string
	)
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		switch readSysfsString(filepath.Join(dir, "type")) {
		case "Mains":
			mains = append(mains, dir)
		case "Battery":
			batteries = append(batteries, dir)
		case "":
			// 種別が読めないものは判断に使わない。
		default:
			// USB / USB_PD / Wireless など。方式は区別しない（要件 §9.3）。
			otherAC = append(otherAC, dir)
		}
	}

	// 1. コンセント。
	if charging, ok := anyOnline(mains); ok {
		return charging, true
	}
	// 2. USB やワイヤレスからの給電。
	if charging, ok := anyOnline(otherAC); ok {
		return charging, true
	}
	// 3. 電源側の情報が無い機械では、バッテリの状態から判断する。
	for _, dir := range batteries {
		switch readSysfsString(filepath.Join(dir, "status")) {
		case "Charging", "Full":
			return true, true
		case "Discharging", "Not charging":
			return false, true
		}
	}
	return false, false
}

// anyOnline は1つでも給電されていれば true を返す。
// 2つ目の戻り値が false のときは online を1つも読めなかった。
func anyOnline(dirs []string) (bool, bool) {
	var read bool
	for _, dir := range dirs {
		switch readSysfsString(filepath.Join(dir, "online")) {
		case "1":
			return true, true
		case "0":
			read = true
		}
	}
	return false, read
}

// readSysfsString は sysfs の1行を読む。読めなければ空文字を返す。
func readSysfsString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
