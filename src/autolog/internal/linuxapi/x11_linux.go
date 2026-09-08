//go:build linux && !android

package linuxapi

import (
	"fmt"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// x11Conn は X サーバへの接続と、そこで引いた名前の覚え書き。
//
// 1秒ごとのサンプリングで繋ぎ直さないよう持ち回す。
type x11Conn struct {
	conn   *xgb.Conn
	screen *xproto.ScreenInfo
	root   xproto.Window

	mu    sync.Mutex
	atoms map[string]xproto.Atom
}

// newX11Conn は X サーバへ繋ぐ。display が空なら $DISPLAY を使う。
func newX11Conn(display string) (*x11Conn, error) {
	var (
		conn *xgb.Conn
		err  error
	)
	if display == "" {
		conn, err = xgb.NewConn()
	} else {
		conn, err = xgb.NewConnDisplay(display)
	}
	if err != nil {
		return nil, fmt.Errorf("X サーバへ繋げない: %w", err)
	}

	setup := xproto.Setup(conn)
	if setup == nil || len(setup.Roots) == 0 {
		conn.Close()
		return nil, fmt.Errorf("X サーバの画面情報を読めない")
	}
	screen := setup.DefaultScreen(conn)

	return &x11Conn{
		conn:   conn,
		screen: screen,
		root:   screen.Root,
		atoms:  map[string]xproto.Atom{},
	}, nil
}

// Close は接続を閉じる。
func (c *x11Conn) Close() {
	if c != nil && c.conn != nil {
		c.conn.Close()
	}
}

// atom は名前を X の識別子へ引く。無ければ 0 を返す。
//
// 引いた結果は覚えておく。1秒ごとに問い合わせる価値のあるものではない。
func (c *x11Conn) atom(name string) xproto.Atom {
	c.mu.Lock()
	cached, ok := c.atoms[name]
	c.mu.Unlock()
	if ok {
		return cached
	}

	var atom xproto.Atom
	reply, err := xproto.InternAtom(c.conn, true, uint16(len(name)), name).Reply()
	if err == nil && reply != nil {
		atom = reply.Atom
	}

	c.mu.Lock()
	c.atoms[name] = atom
	c.mu.Unlock()
	return atom
}

// property はウィンドウのプロパティを読む。無ければ nil。
func (c *x11Conn) property(window xproto.Window, name string, maxLen uint32) *xproto.GetPropertyReply {
	atom := c.atom(name)
	if atom == 0 {
		return nil
	}
	reply, err := xproto.GetProperty(c.conn, false, window, atom,
		xproto.GetPropertyTypeAny, 0, maxLen).Reply()
	if err != nil || reply == nil || reply.ValueLen == 0 {
		return nil
	}
	return reply
}

// propertyString はプロパティを文字列として読む。
func (c *x11Conn) propertyString(window xproto.Window, name string) string {
	reply := c.property(window, name, 1024)
	if reply == nil {
		return ""
	}
	return string(reply.Value)
}

// propertyCardinal はプロパティを数値として読む。
func (c *x11Conn) propertyCardinal(window xproto.Window, name string) (uint32, bool) {
	reply := c.property(window, name, 1)
	if reply == nil || len(reply.Value) < 4 {
		return 0, false
	}
	return xgb.Get32(reply.Value), true
}

// propertyWindow はプロパティをウィンドウの識別子として読む。
func (c *x11Conn) propertyWindow(window xproto.Window, name string) (xproto.Window, bool) {
	value, ok := c.propertyCardinal(window, name)
	if !ok || value == 0 {
		return 0, false
	}
	return xproto.Window(value), true
}
