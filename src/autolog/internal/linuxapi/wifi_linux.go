//go:build linux && !android

package linuxapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	nmDest      = "org.freedesktop.NetworkManager"
	nmPath      = "/org/freedesktop/NetworkManager"
	iwdDest     = "net.connman.iwd"
	wpaDest     = "fi.w1.wpa_supplicant1"
	wpaPath     = "/fi/w1/wpa_supplicant1"
	wifiCmdWait = 5 * time.Second
)

// ssidCommandCandidates は SSID を1行で出すコマンドの自動検出の候補。
//
// 決め打ちではなく「PATH に何があるか」という環境からの導出で、
// AUTOLOG_LINUX_SSID_COMMAND を設定すれば完全に差し替えられる。
var ssidCommandCandidates = [][]string{
	{"iwgetid", "-r"},
}

// WifiSource は接続中の SSID を観測する。
//
// 手段は起動時に一度だけ選ぶ。ポーリングのたびに選び直すと、
// 一時的に落ちているだけの手段を捨ててしまう。
type WifiSource struct {
	backend string
	command []string
}

// NewWifiSource は SSID の観測手段を選ぶ。command が空なら自動で探す。
func NewWifiSource(command string) (*WifiSource, error) {
	// 明示的に設定されたコマンドが最優先。標準の手段で取れない環境の逃げ道。
	if argv := strings.Fields(command); len(argv) > 0 {
		return &WifiSource{backend: "command:" + argv[0], command: argv}, nil
	}

	if conn, err := systemBus.get(); err == nil {
		for _, backend := range []struct {
			name string
			dest string
		}{
			{name: "networkmanager", dest: nmDest},
			{name: "iwd", dest: iwdDest},
			{name: "wpa_supplicant", dest: wpaDest},
		} {
			if hasOwner(conn, backend.dest) {
				return &WifiSource{backend: backend.name}, nil
			}
		}
	}

	for _, argv := range ssidCommandCandidates {
		if path, err := exec.LookPath(argv[0]); err == nil {
			found := slices.Clone(argv)
			found[0] = path
			return &WifiSource{backend: "command:" + argv[0], command: found}, nil
		}
	}

	return nil, fmt.Errorf("SSIDを取る手段が無い: %w", ErrUnavailable)
}

// Backend は選ばれた手段の名前を返す。起動時のログに載せる。
func (s *WifiSource) Backend() string { return s.backend }

// CurrentSSID は接続中の SSID を返す。
//
// 2つ目の戻り値が false かつエラーが nil のときは、取得できたうえで
// つながっていないことが分かった状態。エラーを返したときは
// 状態が分からない。両者を混ぜると、つながったままなのに
// 偽の切断イベントが生ログに残る。
//
// 有線 LAN は対象外（要件 §9.1）。無線の機器だけを見る。
// BSSID は読まない。
func (s *WifiSource) CurrentSSID() (string, bool, error) {
	if len(s.command) > 0 {
		return s.ssidFromCommand()
	}
	switch s.backend {
	case "networkmanager":
		return networkManagerSSID()
	case "iwd":
		return iwdSSID()
	case "wpa_supplicant":
		return wpaSupplicantSSID()
	}
	return "", false, fmt.Errorf("SSIDを取る手段が無い: %w", ErrUnavailable)
}

// ssidFromCommand は設定されたコマンドを実行して SSID を読む。
func (s *WifiSource) ssidFromCommand() (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wifiCmdWait)
	defer cancel()

	out, err := exec.CommandContext(ctx, s.command[0], s.command[1:]...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		// iwgetid はつながっていないと終了コード 255 を返す。
		// 出力が空なら「取れたうえで未接続」として扱う。
		if errors.As(err, &exitErr) && len(strings.TrimSpace(string(out))) == 0 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("SSIDのコマンドが失敗した: %w", err)
	}

	ssid, ok := parseSSIDCommandOutput(out)
	if !ok {
		return "", false, nil
	}
	return ssid, true, nil
}

// networkManagerSSID は NetworkManager から SSID を読む。
func networkManagerSSID() (string, bool, error) {
	conn, err := systemBus.get()
	if err != nil {
		return "", false, err
	}

	value, err := busProperty(conn, nmDest, nmPath, nmDest, "Devices")
	if err != nil {
		return "", false, wrapWlanError(err)
	}
	paths, ok := value.Value().([]dbus.ObjectPath)
	if !ok {
		return "", false, fmt.Errorf("NetworkManager の Devices を読めない")
	}

	// 無線の機器が複数あるときに結果がぶれないよう、オブジェクトパスで並べる。
	slices.Sort(paths)

	for _, path := range paths {
		const deviceIfc = nmDest + ".Device"
		deviceType, err := busProperty(conn, nmDest, path, deviceIfc, "DeviceType")
		if err != nil {
			continue
		}
		// NM_DEVICE_TYPE_WIFI = 2。有線は対象外。
		if kind, ok := deviceType.Value().(uint32); !ok || kind != 2 {
			continue
		}
		state, err := busProperty(conn, nmDest, path, deviceIfc, "State")
		if err != nil {
			continue
		}
		// NM_DEVICE_STATE_ACTIVATED = 100。
		if activated, ok := state.Value().(uint32); !ok || activated != 100 {
			continue
		}

		ap, err := busProperty(conn, nmDest, path, deviceIfc+".Wireless", "ActiveAccessPoint")
		if err != nil {
			return "", false, wrapWlanError(err)
		}
		apPath, ok := ap.Value().(dbus.ObjectPath)
		if !ok || apPath == "" || apPath == "/" {
			continue
		}

		ssid, err := busProperty(conn, nmDest, apPath, nmDest+".AccessPoint", "Ssid")
		if err != nil {
			// つながってはいるので、未接続として返さない。
			return "", false, fmt.Errorf("%w: %w", ErrSSIDUnknown, err)
		}
		raw, ok := ssid.Value().([]byte)
		if !ok {
			return "", false, ErrSSIDUnknown
		}
		name, err := ssidFromBytes(raw)
		if err != nil {
			return "", false, err
		}
		return name, true, nil
	}

	// 無線の機器が無い、またはどれもつながっていない。
	return "", false, nil
}

// iwdSSID は iwd から SSID を読む。
func iwdSSID() (string, bool, error) {
	conn, err := systemBus.get()
	if err != nil {
		return "", false, err
	}

	objects, err := managedObjects(conn, iwdDest, "/")
	if err != nil {
		return "", false, wrapWlanError(err)
	}

	paths := slices.Sorted(maps.Keys(objects))
	for _, path := range paths {
		station, ok := objects[path][iwdDest+".Station"]
		if !ok {
			continue
		}
		if state, ok := station["State"].Value().(string); !ok || state != "connected" {
			continue
		}
		network, ok := station["ConnectedNetwork"].Value().(dbus.ObjectPath)
		if !ok || network == "" || network == "/" {
			continue
		}
		name, ok := objects[network][iwdDest+".Network"]["Name"].Value().(string)
		if !ok || name == "" {
			return "", false, ErrSSIDUnknown
		}
		return name, true, nil
	}
	return "", false, nil
}

// wpaSupplicantSSID は wpa_supplicant から SSID を読む。
func wpaSupplicantSSID() (string, bool, error) {
	conn, err := systemBus.get()
	if err != nil {
		return "", false, err
	}

	value, err := busProperty(conn, wpaDest, wpaPath, wpaDest, "Interfaces")
	if err != nil {
		return "", false, wrapWlanError(err)
	}
	paths, ok := value.Value().([]dbus.ObjectPath)
	if !ok {
		return "", false, fmt.Errorf("wpa_supplicant の Interfaces を読めない")
	}
	slices.Sort(paths)

	for _, path := range paths {
		bss, err := busProperty(conn, wpaDest, path, wpaDest+".Interface", "CurrentBSS")
		if err != nil {
			continue
		}
		bssPath, ok := bss.Value().(dbus.ObjectPath)
		if !ok || bssPath == "" || bssPath == "/" {
			continue
		}
		ssid, err := busProperty(conn, wpaDest, bssPath, wpaDest+".BSS", "SSID")
		if err != nil {
			return "", false, fmt.Errorf("%w: %w", ErrSSIDUnknown, err)
		}
		raw, ok := ssid.Value().([]byte)
		if !ok {
			return "", false, ErrSSIDUnknown
		}
		name, err := ssidFromBytes(raw)
		if err != nil {
			return "", false, err
		}
		return name, true, nil
	}
	return "", false, nil
}

// wrapWlanError は権限で断られた応答を winapi と同じ形のエラーへ写す。
func wrapWlanError(err error) error {
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		switch dbusErr.Name {
		case "org.freedesktop.DBus.Error.AccessDenied",
			"org.freedesktop.DBus.Error.AuthFailed",
			"org.freedesktop.DBus.Error.InteractiveAuthorizationRequired":
			return fmt.Errorf("%w: %w", ErrWlanPermissionDenied, err)
		}
	}
	return err
}
