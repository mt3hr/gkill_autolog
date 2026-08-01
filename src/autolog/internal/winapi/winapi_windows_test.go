//go:build windows

package winapi

import (
	"errors"
	"testing"
	"unsafe"
)

// Win32 構造体はサイズが合っていないと API が失敗するか、
// 最悪の場合は隣接メモリを壊す。定義がずれたらここで気づけるようにする。
func TestStructSizesMatchWin32(t *testing.T) {
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"WNDCLASSEXW", unsafe.Sizeof(wndClassExW{}), 80},
		{"SYSTEM_POWER_STATUS", unsafe.Sizeof(systemPowerStatus{}), 12},
		{"WLAN_INTERFACE_INFO", unsafe.Sizeof(wlanInterfaceInfo{}), 532},
		{"SP_DEVINFO_DATA", unsafe.Sizeof(spDevInfoData{}), 32},
		{"DEVPROPKEY", unsafe.Sizeof(devPropKey{}), 20},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("sizeof(%s) = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

// WLAN_CONNECTION_ATTRIBUTES は先頭部分しか定義していないため、
// SSID を読むオフセットが仕様どおりであることを個別に確認する。
func TestWlanConnectionAttributesSSIDOffsets(t *testing.T) {
	var attrs wlanConnectionAttributes
	if got, want := unsafe.Offsetof(attrs.ssidLength), uintptr(520); got != want {
		t.Errorf("offsetof(ssidLength) = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(attrs.ssid), uintptr(524); got != want {
		t.Errorf("offsetof(ssid) = %d, want %d", got, want)
	}
}

func TestSpDevInfoDataOffsets(t *testing.T) {
	var info spDevInfoData
	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"ClassGuid", unsafe.Offsetof(info.classGUID), 4},
		{"DevInst", unsafe.Offsetof(info.devInst), 20},
		{"Reserved", unsafe.Offsetof(info.reserved), 24},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("offsetof(SP_DEVINFO_DATA.%s) = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
}

// 読み取り専用 API が実機で呼べることを確認する。
// 値そのものは環境依存なので、パニックせず妥当な形で返ることだけを見る。
func TestReadOnlyAPIsDoNotFail(t *testing.T) {
	t.Run("GetForegroundWindow", func(t *testing.T) {
		info, ok, err := GetForegroundWindow()
		if err != nil {
			t.Fatalf("GetForegroundWindow: %v", err)
		}
		t.Logf("ok=%v app=%q display=%q title=%q pid=%d",
			ok, info.AppName, info.AppDisplayName, info.WindowTitle, info.PID)
	})

	t.Run("IsCharging", func(t *testing.T) {
		charging, err := IsCharging()
		if err != nil {
			t.Fatalf("IsCharging: %v", err)
		}
		t.Logf("charging=%v", charging)
	})

	t.Run("CurrentSSID", func(t *testing.T) {
		ssid, ok, err := CurrentSSID()
		if errors.Is(err, ErrWlanPermissionDenied) {
			// 環境側の設定であってコードの不具合ではない。
			// 位置情報を許可すると取得できるようになる。
			t.Skipf("位置情報が許可されていないため SSID を取得できない: %v", err)
		}
		if err != nil {
			t.Fatalf("CurrentSSID: %v", err)
		}
		t.Logf("ok=%v ssid=%q", ok, ssid)
	})

	t.Run("ConnectedBluetoothDevices", func(t *testing.T) {
		devices, err := ConnectedBluetoothDevices()
		if err != nil {
			t.Fatalf("ConnectedBluetoothDevices: %v", err)
		}
		t.Logf("devices=%q", devices)
	})

	t.Run("IsSessionLocked", func(t *testing.T) {
		t.Logf("locked=%v", IsSessionLocked())
	})
}
