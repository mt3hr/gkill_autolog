//go:build windows

package winapi

import (
	"fmt"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Bluetooth の接続状態は SetupAPI のデバイスプロパティから取る。
//
// BluetoothFindFirstDevice / BLUETOOTH_DEVICE_INFO.fConnected は使わない。理由は2つ。
//   - Bluetooth Classic しか列挙せず、BLE 機器 (BTHLE\DEV_...) が一切出てこない。
//     実際にマウスやキーボードは BLE で接続されることが多い。
//   - 接続中の機器でも fConnected が 0 のまま更新されないことがある。
//
// 代わりに GUID_DEVCLASS_BLUETOOTH の present なデバイスを列挙し、
// DEVPKEY_Device_IsConnected を見る。ローカルの無線アダプタや
// RFCOMM などの疑似デバイスは常に接続扱いになるため、
// Bluetooth アドレスを持つもの（＝相手側の機器）だけを残す。
var (
	setupapi = windows.NewLazySystemDLL("setupapi.dll")

	procSetupDiGetClassDevsW         = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInfo        = setupapi.NewProc("SetupDiEnumDeviceInfo")
	procSetupDiGetDevicePropertyW    = setupapi.NewProc("SetupDiGetDevicePropertyW")
	procSetupDiDestroyDeviceInfoList = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	digcfPresent = 0x00000002

	devpropTypeBoolean = 0x00000011
	devpropTypeString  = 0x00000012

	// DEVPROP_BOOLEAN の TRUE は -1。
	devpropTrue = -1

	errorNoMoreItems     = 259
	errorInsufficientBuf = 122
	errorNotFound        = 1168
)

// guidDevClassBluetooth は GUID_DEVCLASS_BLUETOOTH {e0cbf06c-cd8b-4647-bb8a-263b43f0f974}。
var guidDevClassBluetooth = windows.GUID{
	Data1: 0xe0cbf06c,
	Data2: 0xcd8b,
	Data3: 0x4647,
	Data4: [8]byte{0xbb, 0x8a, 0x26, 0x3b, 0x43, 0xf0, 0xf9, 0x74},
}

// devPropKey は DEVPROPKEY。
type devPropKey struct {
	fmtid windows.GUID
	pid   uint32
}

var (
	// devpkeyDeviceIsConnected は {83DA6326-97A6-4088-9453-A1923F573B29} 15。
	devpkeyDeviceIsConnected = devPropKey{
		fmtid: windows.GUID{
			Data1: 0x83da6326,
			Data2: 0x97a6,
			Data3: 0x4088,
			Data4: [8]byte{0x94, 0x53, 0xa1, 0x92, 0x3f, 0x57, 0x3b, 0x29},
		},
		pid: 15,
	}

	// devpkeyBluetoothDeviceAddress は {2BD67D8B-8BEB-48D5-87E0-6CDA3428040A} 1。
	// これを持たないものはローカルの無線アダプタや疑似デバイス。
	devpkeyBluetoothDeviceAddress = devPropKey{
		fmtid: windows.GUID{
			Data1: 0x2bd67d8b,
			Data2: 0x8beb,
			Data3: 0x48d5,
			Data4: [8]byte{0x87, 0xe0, 0x6c, 0xda, 0x34, 0x28, 0x04, 0x0a},
		},
		pid: 1,
	}

	// devpkeyName は DEVPKEY_NAME {b725f130-47ef-101a-a5f1-02608c9eebac} 10。
	// Get-PnpDevice の FriendlyName と同じ値。
	devpkeyName = devPropKey{
		fmtid: windows.GUID{
			Data1: 0xb725f130,
			Data2: 0x47ef,
			Data3: 0x101a,
			Data4: [8]byte{0xa5, 0xf1, 0x02, 0x60, 0x8c, 0x9e, 0xeb, 0xac},
		},
		pid: 10,
	}
)

// spDevInfoData は SP_DEVINFO_DATA (x64 で 32 バイト)。
type spDevInfoData struct {
	cbSize    uint32
	classGUID windows.GUID
	devInst   uint32
	reserved  uintptr
}

// ConnectedBluetoothDevices は現在接続中の Bluetooth 機器名を返す。
//
// 機器名だけを扱い、アドレスは返さない（要件により gkill へ保存しないため）。
// Bluetooth 非搭載機など列挙できない環境では空を返し、エラーにはしない。
func ConnectedBluetoothDevices() ([]string, error) {
	if err := procSetupDiGetClassDevsW.Find(); err != nil {
		return nil, nil
	}

	devInfoSet, _, err := procSetupDiGetClassDevsW.Call(
		uintptr(unsafe.Pointer(&guidDevClassBluetooth)),
		0,
		0,
		digcfPresent,
	)
	if devInfoSet == 0 || devInfoSet == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("failed to enumerate bluetooth devices: %w", err)
	}
	defer procSetupDiDestroyDeviceInfoList.Call(devInfoSet)

	var connected []string
	for index := uint32(0); ; index++ {
		devInfo := spDevInfoData{cbSize: uint32(unsafe.Sizeof(spDevInfoData{}))}
		ok, _, callErr := procSetupDiEnumDeviceInfo.Call(
			devInfoSet,
			uintptr(index),
			uintptr(unsafe.Pointer(&devInfo)),
		)
		if ok == 0 {
			if errno, isErrno := callErr.(windows.Errno); isErrno && errno == errorNoMoreItems {
				break
			}
			break
		}

		// 相手側の機器だけを対象にする。アドレスが無いものはアダプタや疑似デバイス。
		if address, err := deviceStringProperty(devInfoSet, &devInfo, devpkeyBluetoothDeviceAddress); err != nil || address == "" {
			continue
		}

		isConnected, err := deviceBoolProperty(devInfoSet, &devInfo, devpkeyDeviceIsConnected)
		if err != nil || !isConnected {
			continue
		}

		name, err := deviceStringProperty(devInfoSet, &devInfo, devpkeyName)
		if err != nil || name == "" {
			continue
		}
		// 同じ機器が複数のインタフェースとして現れることがある。
		if !slices.Contains(connected, name) {
			connected = append(connected, name)
		}
	}
	return connected, nil
}

// deviceBoolProperty は DEVPROP_TYPE_BOOLEAN のプロパティを読む。
func deviceBoolProperty(devInfoSet uintptr, devInfo *spDevInfoData, key devPropKey) (bool, error) {
	var propType uint32
	var value int8
	ok, _, err := procSetupDiGetDevicePropertyW.Call(
		devInfoSet,
		uintptr(unsafe.Pointer(devInfo)),
		uintptr(unsafe.Pointer(&key)),
		uintptr(unsafe.Pointer(&propType)),
		uintptr(unsafe.Pointer(&value)),
		uintptr(unsafe.Sizeof(value)),
		0,
		0,
	)
	if ok == 0 {
		return false, fmt.Errorf("failed to read boolean device property: %w", err)
	}
	if propType != devpropTypeBoolean {
		return false, fmt.Errorf("unexpected device property type %#x (want boolean)", propType)
	}
	return value == devpropTrue, nil
}

// deviceStringProperty は DEVPROP_TYPE_STRING のプロパティを読む。
func deviceStringProperty(devInfoSet uintptr, devInfo *spDevInfoData, key devPropKey) (string, error) {
	var propType uint32
	var requiredSize uint32

	// まず必要なサイズを問い合わせる。バッファ不足エラーが返るのが正常。
	procSetupDiGetDevicePropertyW.Call(
		devInfoSet,
		uintptr(unsafe.Pointer(devInfo)),
		uintptr(unsafe.Pointer(&key)),
		uintptr(unsafe.Pointer(&propType)),
		0,
		0,
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if requiredSize == 0 {
		return "", nil
	}

	buf := make([]uint16, (requiredSize+1)/2)
	ok, _, err := procSetupDiGetDevicePropertyW.Call(
		devInfoSet,
		uintptr(unsafe.Pointer(devInfo)),
		uintptr(unsafe.Pointer(&key)),
		uintptr(unsafe.Pointer(&propType)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(requiredSize),
		uintptr(unsafe.Pointer(&requiredSize)),
		0,
	)
	if ok == 0 {
		return "", fmt.Errorf("failed to read string device property: %w", err)
	}
	if propType != devpropTypeString {
		return "", fmt.Errorf("unexpected device property type %#x (want string)", propType)
	}
	return windows.UTF16ToString(buf), nil
}
