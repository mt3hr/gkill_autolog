//go:build windows

package winapi

import (
	"fmt"
	"unsafe"
)

var procGetSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")

// systemPowerStatus は SYSTEM_POWER_STATUS。
// バッテリー残量は要件により記録しないが、構造体のレイアウトを合わせるため定義は残す。
type systemPowerStatus struct {
	acLineStatus        byte
	batteryFlag         byte
	batteryLifePercent  byte
	systemStatusFlag    byte
	batteryLifeTime     uint32
	batteryFullLifeTime uint32
}

// acLineOnline は AC 接続済みを表す ACLineStatus の値。255 は不明。
const (
	acLineOffline = 0
	acLineOnline  = 1
)

// IsCharging は AC に接続されているかを返す。
//
// 「充電中」の厳密な判定 (BatteryFlag の充電ビット) ではなく AC 接続の有無を見る。
// 満充電になった時点で充電ビットが落ちると TimeIs が細切れになるため、
// 要件 §9.3「急速・USB・ワイヤレスなどの方式は区別しない」に沿って接続期間として扱う。
// 残量と充電方式は取得しない。
func IsCharging() (bool, error) {
	var status systemPowerStatus
	ret, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return false, fmt.Errorf("failed to get system power status: %w", err)
	}
	switch status.acLineStatus {
	case acLineOnline:
		return true, nil
	case acLineOffline:
		return false, nil
	default:
		// 255 (不明)。デスクトップ機など。状態変化として扱わないよう未接続を返す。
		return false, nil
	}
}
