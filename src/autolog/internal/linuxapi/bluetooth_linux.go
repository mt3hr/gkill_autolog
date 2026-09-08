//go:build linux && !android

package linuxapi

import (
	"fmt"
	"slices"

	"github.com/godbus/dbus/v5"
)

const (
	bluezDest      = "org.bluez"
	bluezDeviceIfc = "org.bluez.Device1"
)

// BluetoothSource は接続中の Bluetooth 機器を観測する。
type BluetoothSource struct{}

// NewBluetoothSource は Bluetooth の観測手段を用意する。
//
// bluetoothd が居ない機械では ErrUnavailable を返す。
// アダプタが刺さっていないだけなら手段はあるので、空の一覧を返す側になる
// （winapi の「非搭載環境は空 + nil」と同じ扱い）。
func NewBluetoothSource() (*BluetoothSource, error) {
	conn, err := systemBus.get()
	if err != nil {
		return nil, fmt.Errorf("システムバスへ繋げない: %w", err)
	}
	if !hasOwner(conn, bluezDest) {
		return nil, fmt.Errorf("%s がバスに居ない: %w", bluezDest, ErrUnavailable)
	}
	return &BluetoothSource{}, nil
}

// Backend は選ばれた手段の名前を返す。
func (s *BluetoothSource) Backend() string { return "bluez" }

// Connected は接続中の機器名を返す。比較を安定させるため名前順に並べる。
//
// 機器名の先頭の `LE_` は落とさない。同じ機器がクラシックと Low Energy の
// 両方でつながったときにまとめるのは normalize の役目
// （internal/normalize/state.go の bluetoothDeviceName）で、
// ここで落とすと両側で二重に処理される。
func (s *BluetoothSource) Connected() ([]string, error) {
	conn, err := systemBus.get()
	if err != nil {
		return nil, err
	}

	objects, err := managedObjects(conn, bluezDest, "/")
	if err != nil {
		return nil, err
	}

	var names []string
	for _, interfaces := range objects {
		device, ok := interfaces[bluezDeviceIfc]
		if !ok {
			continue
		}
		if connected, ok := device["Connected"].Value().(bool); !ok || !connected {
			continue
		}
		if name := bluezDeviceName(device); name != "" {
			names = append(names, name)
		}
	}

	slices.Sort(names)
	// 同じ名前の機器を2台つないでいると、名前での差分検出が
	// 「1台切れて1台つながった」を延々と出す。1つにまとめる。
	return slices.Compact(names), nil
}

// bluezDeviceName は機器の表示名を返す。
//
// Alias は BlueZ が必ず埋める（機器名を取れていなければアドレスが入る）ので
// こちらを先に見る。利用者が付け替えた名前もこちらに入る。
func bluezDeviceName(device map[string]dbus.Variant) string {
	for _, key := range []string{"Alias", "Name"} {
		if name, ok := device[key].Value().(string); ok && name != "" {
			return name
		}
	}
	return ""
}
