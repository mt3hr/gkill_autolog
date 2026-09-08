//go:build linux && !android

package linuxapi

import (
	"fmt"
)

const upowerDest = "org.freedesktop.UPower"

// PowerSource は充電状態を観測する。
type PowerSource struct {
	backend string
}

// NewPowerSource は充電状態の観測手段を選ぶ。
//
// sysfs を先に見る。依存が増えず、同期的で、D-Bus が使えない環境
// (コンテナ、systemd のユーザーセッション外) でも動くため。
//
// 電源の情報が無い据え置き機では ErrUnavailable を返す。
// 「ずっと充電中」という TimeIs を作っても意味が無い。
func NewPowerSource() (*PowerSource, error) {
	if _, ok := chargingFromSysfs(sysfsPowerSupply); ok {
		return &PowerSource{backend: "sysfs"}, nil
	}

	conn, err := systemBus.get()
	if err == nil && hasOwner(conn, upowerDest) {
		if _, err := upowerCharging(); err == nil {
			return &PowerSource{backend: "upower"}, nil
		}
	}
	return nil, fmt.Errorf("充電状態を取る手段が無い: %w", ErrUnavailable)
}

// Backend は選ばれた手段の名前を返す。起動時のログに載せる。
func (s *PowerSource) Backend() string { return s.backend }

// IsCharging は AC につながっているかを返す。
//
// Windows と揃えて「充電中」ではなく AC 接続の有無を見る。
// 満充電で充電が止まっても区間を切らないため（要件 §9.3）。
func (s *PowerSource) IsCharging() (bool, error) {
	if s.backend == "sysfs" {
		charging, ok := chargingFromSysfs(sysfsPowerSupply)
		if !ok {
			return false, fmt.Errorf("電源の情報を読めない: %w", ErrUnavailable)
		}
		return charging, nil
	}
	return upowerCharging()
}

// upowerCharging は UPower の OnBattery から AC 接続の有無を返す。
func upowerCharging() (bool, error) {
	conn, err := systemBus.get()
	if err != nil {
		return false, err
	}
	value, err := busProperty(conn, upowerDest, "/org/freedesktop/UPower", upowerDest, "OnBattery")
	if err != nil {
		return false, err
	}
	onBattery, ok := value.Value().(bool)
	if !ok {
		return false, fmt.Errorf("UPower の OnBattery を真偽値として読めない")
	}
	return !onBattery, nil
}
