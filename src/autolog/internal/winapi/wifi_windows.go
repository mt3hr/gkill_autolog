//go:build windows

package winapi

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrWlanPermissionDenied は位置情報の許可が無くて SSID を取得できないことを表す。
//
// Windows 11 では SSID の取得に位置情報へのアクセス許可が要る。
// 「設定 > プライバシーとセキュリティ > 位置情報」で位置情報サービスと
// 「デスクトップ アプリが位置情報にアクセスできるようにする」を有効にすると取得できる。
// 許可が無い場合、WlanQueryInterface は ERROR_ACCESS_DENIED を返す。
var ErrWlanPermissionDenied = errors.New("wlan: location permission is required to read SSID")

var (
	wlanapi = windows.NewLazySystemDLL("wlanapi.dll")

	procWlanOpenHandle     = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle    = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface = wlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory     = wlanapi.NewProc("WlanFreeMemory")
)

const (
	wlanClientVersion = 2

	// wlanIntfOpcodeCurrentConnection は現在の接続情報を取り出す opcode。
	wlanIntfOpcodeCurrentConnection = 7

	// wlanInterfaceStateConnected は接続済みを表す WLAN_INTERFACE_STATE。
	wlanInterfaceStateConnected = 1

	// errorAccessDenied は ERROR_ACCESS_DENIED。位置情報の許可が無いときに返る。
	errorAccessDenied = 5
)

// wlanInterfaceInfo は WLAN_INTERFACE_INFO (532 バイト)。
type wlanInterfaceInfo struct {
	interfaceGUID           windows.GUID
	strInterfaceDescription [256]uint16
	isState                 uint32
}

// wlanConnectionAttributes は WLAN_CONNECTION_ATTRIBUTES の先頭部分。
// SSID までしか使わないため、以降のフィールドは定義しない（読み取り専用なので安全）。
type wlanConnectionAttributes struct {
	isState            uint32
	wlanConnectionMode uint32
	strProfileName     [256]uint16
	// ここから WLAN_ASSOCIATION_ATTRIBUTES.dot11Ssid
	ssidLength uint32
	ssid       [32]byte
}

// CurrentSSID は現在接続している Wi-Fi の SSID を返す。
// 未接続、または無線アダプタが無い場合は ok = false。
//
// BSSID は取得しない。要件 §9.1 により生ログにも保持しないため。
// 有線 LAN は対象外なので、ここでは無線インタフェースだけを見る。
func CurrentSSID() (string, bool, error) {
	var handle windows.Handle
	var negotiatedVersion uint32
	ret, _, _ := procWlanOpenHandle.Call(
		wlanClientVersion,
		0,
		uintptr(unsafe.Pointer(&negotiatedVersion)),
		uintptr(unsafe.Pointer(&handle)),
	)
	if ret != 0 {
		// WLAN AutoConfig サービスが止まっている場合など。無線なしとして扱う。
		return "", false, nil
	}
	defer procWlanCloseHandle.Call(uintptr(handle), 0)

	var listPtr unsafe.Pointer
	ret, _, _ = procWlanEnumInterfaces.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&listPtr)))
	if ret != 0 || listPtr == nil {
		return "", false, nil
	}
	defer procWlanFreeMemory.Call(uintptr(listPtr))

	// WLAN_INTERFACE_INFO_LIST: dwNumberOfItems(4), dwIndex(4), InterfaceInfo[]
	count := *(*uint32)(listPtr)
	const interfaceInfoOffset = 8
	infoSize := unsafe.Sizeof(wlanInterfaceInfo{})

	for i := range count {
		info := (*wlanInterfaceInfo)(unsafe.Add(listPtr, interfaceInfoOffset+uintptr(i)*infoSize))
		if info.isState != wlanInterfaceStateConnected {
			continue
		}
		ssid, ok, err := queryCurrentSSID(handle, info.interfaceGUID)
		if err != nil {
			return "", false, err
		}
		if ok {
			return ssid, true, nil
		}
	}
	return "", false, nil
}

func queryCurrentSSID(handle windows.Handle, guid windows.GUID) (string, bool, error) {
	var dataSize uint32
	var dataPtr unsafe.Pointer
	ret, _, _ := procWlanQueryInterface.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&guid)),
		wlanIntfOpcodeCurrentConnection,
		0,
		uintptr(unsafe.Pointer(&dataSize)),
		uintptr(unsafe.Pointer(&dataPtr)),
		0,
	)
	if ret == errorAccessDenied {
		return "", false, ErrWlanPermissionDenied
	}
	if ret != 0 || dataPtr == nil {
		// 接続直後や切断直後は取得できないことがある。エラーにはしない。
		return "", false, nil
	}
	defer procWlanFreeMemory.Call(uintptr(dataPtr))

	if uintptr(dataSize) < unsafe.Sizeof(wlanConnectionAttributes{}) {
		return "", false, fmt.Errorf("unexpected WLAN_CONNECTION_ATTRIBUTES size %d", dataSize)
	}

	attrs := (*wlanConnectionAttributes)(dataPtr)
	if attrs.isState != wlanInterfaceStateConnected {
		return "", false, nil
	}
	length := attrs.ssidLength
	if length == 0 || length > uint32(len(attrs.ssid)) {
		return "", false, nil
	}
	// SSID はバイト列。UTF-8 として解釈できる想定だが、そうでなくても生値のまま扱う。
	return string(attrs.ssid[:length]), true, nil
}
