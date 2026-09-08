//go:build linux && !android

package linuxapi

import (
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// busRef はバスへの接続を1本だけ持ち回す。
//
// 接続状態のポーリングは15秒ごとに走るので、そのたびに繋ぎ直すと
// 無駄が大きい。切れていたら次に使うときに繋ぎ直す。
type busRef struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	connect func() (*dbus.Conn, error)
}

var (
	systemBus  = &busRef{connect: func() (*dbus.Conn, error) { return dbus.ConnectSystemBus() }}
	sessionBus = &busRef{connect: func() (*dbus.Conn, error) { return dbus.ConnectSessionBus() }}
)

// get は使える接続を返す。切れていれば繋ぎ直す。
func (b *busRef) get() (*dbus.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn != nil {
		if b.conn.Connected() {
			return b.conn, nil
		}
		_ = b.conn.Close()
		b.conn = nil
	}

	conn, err := b.connect()
	if err != nil {
		return nil, err
	}
	b.conn = conn
	return conn, nil
}

// hasOwner はその名前の持ち主がバスに居るかを返す。
//
// 手段を選ぶときに使う。居ないサービスへ呼び出しを投げると
// D-Bus が起動を試みることがあるため、先に確かめる。
func hasOwner(conn *dbus.Conn, name string) bool {
	var owned bool
	err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&owned)
	return err == nil && owned
}

// busProperty はプロパティを1つ読む。
func busProperty(conn *dbus.Conn, dest string, path dbus.ObjectPath, iface, name string) (dbus.Variant, error) {
	value, err := conn.Object(dest, path).GetProperty(iface + "." + name)
	if err != nil {
		return dbus.Variant{}, fmt.Errorf("%s.%s を読めない (%s): %w", iface, name, path, err)
	}
	return value, nil
}

// managedObjects は ObjectManager の一覧を返す。
func managedObjects(conn *dbus.Conn, dest string, path dbus.ObjectPath) (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := conn.Object(dest, path).
		Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects)
	if err != nil {
		return nil, fmt.Errorf("%s の一覧を取れない: %w", dest, err)
	}
	return objects, nil
}
