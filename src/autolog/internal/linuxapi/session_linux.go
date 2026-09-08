//go:build linux && !android

package linuxapi

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	logindDest        = "org.freedesktop.login1"
	logindManagerPath = "/org/freedesktop/login1"
	logindManagerIfc  = "org.freedesktop.login1.Manager"
	logindSessionIfc  = "org.freedesktop.login1.Session"
	screenSaverDest   = "org.freedesktop.ScreenSaver"
	gnomeScreenSaver  = "org.gnome.ScreenSaver"

	// sessionInhibitFlushGrace はサスペンド・シャットダウンを遅らせる時間。
	//
	// PrepareForSleep(true) はサスペンドの直前に飛ぶ。そのまま返すと
	// suspend のイベントが書き出される前に機械が眠り、normalize が
	// 接続区間を閉じるマーカーを失う。遅延インヒビタで少しだけ止めて、
	// Emitter が書き終わる時間を作る。
	//
	// systemd の既定 InhibitDelayMaxSec は5秒。それを超えると
	// 待っても無駄に眠らせるだけになるので、内側に収める。
	// Windows の finalFlushBusyWait (3秒) と同じ考え方。
	sessionInhibitFlushGrace = 2 * time.Second
)

// SessionWatcher はロック・解除・サスペンド・復帰・シャットダウンを観測する。
//
// **D-Bus に1本も繋がらなくても作れること。** 観測できるものが無くても
// sessionCollector は起動し、collector_start / collector_stop を記録する
// 必要がある。これが normalize の唯一の「観測の切れ目」で
// (internal/normalize/state.go)、失うと Wi-Fi・Bluetooth・充電の
// 開いた区間が永久に閉じない。
type SessionWatcher struct {
	events   chan SessionEventKind
	warnings []string
}

// NewSessionWatcher は監視を作る。繋がらない経路があっても失敗しない。
func NewSessionWatcher() *SessionWatcher {
	return &SessionWatcher{events: make(chan SessionEventKind, 32)}
}

// Events は観測した変化を流す。Run が終わるときに閉じる。
func (w *SessionWatcher) Events() <-chan SessionEventKind { return w.events }

// Warnings は使えなかった経路の説明を返す。Run のあとで読む。
func (w *SessionWatcher) Warnings() []string { return w.warnings }

// emit は観測を流す。詰まっていたら捨てる。
// 収集を遅らせないことを優先する（winapi のフックと同じ約束）。
func (w *SessionWatcher) emit(kind SessionEventKind) {
	select {
	case w.events <- kind:
	default:
	}
}

// emitLockChange はロック状態の変化を、重複を落としてから流す。
func (w *SessionWatcher) emitLockChange(locked bool) {
	if kind, ok := sharedLockState.apply(locked); ok {
		w.emit(kind)
	}
}

// Run は監視を回す。ctx が終わるまで返らない。
func (w *SessionWatcher) Run(ctx context.Context) error {
	defer close(w.events)
	defer sharedLockState.reset()

	systemSignals := make(chan *dbus.Signal, 32)
	sessionSignals := make(chan *dbus.Signal, 32)

	inhibit := w.startLogind(systemSignals)
	if inhibit != nil {
		defer inhibit.release()
	}
	w.startScreenSaver(sessionSignals)

	for {
		select {
		case <-ctx.Done():
			return nil
		case signal, ok := <-systemSignals:
			if !ok {
				systemSignals = nil
				continue
			}
			w.handleLogindSignal(signal, inhibit)
		case signal, ok := <-sessionSignals:
			if !ok {
				sessionSignals = nil
				continue
			}
			w.handleScreenSaverSignal(signal)
		}
	}
}

// startLogind は logind の監視を始める。使えなければ nil を返す。
func (w *SessionWatcher) startLogind(signals chan *dbus.Signal) *sleepInhibitor {
	conn, err := systemBus.get()
	if err != nil {
		w.warnings = append(w.warnings,
			fmt.Sprintf("システムバスへ繋げないため、ロックとサスペンドを記録しない: %v", err))
		return nil
	}
	if !hasOwner(conn, logindDest) {
		w.warnings = append(w.warnings,
			"systemd-logind が居ないため、ロックとサスペンドを記録しない")
		return nil
	}

	// サスペンドとシャットダウンの予告。
	for _, member := range []string{"PrepareForSleep", "PrepareForShutdown"} {
		if err := conn.AddMatchSignal(
			dbus.WithMatchSender(logindDest),
			dbus.WithMatchInterface(logindManagerIfc),
			dbus.WithMatchMember(member),
		); err != nil {
			w.warnings = append(w.warnings, fmt.Sprintf("%s を購読できない: %v", member, err))
		}
	}

	// 自分のセッションのロック状態。
	if path, err := ownSessionPath(conn); err != nil {
		w.warnings = append(w.warnings, fmt.Sprintf("自分のセッションを特定できない: %v", err))
	} else {
		if value, err := busProperty(conn, logindDest, path, logindSessionIfc, "LockedHint"); err == nil {
			if locked, ok := value.Value().(bool); ok {
				// 起動時の状態は種として置くだけ。ここで記録すると、
				// 収集を始めただけでロック・解除が生ログに載る。
				sharedLockState.seed(locked)
			}
		}
		if err := conn.AddMatchSignal(
			dbus.WithMatchSender(logindDest),
			dbus.WithMatchObjectPath(path),
			dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
			dbus.WithMatchMember("PropertiesChanged"),
		); err != nil {
			w.warnings = append(w.warnings, fmt.Sprintf("セッションの状態を購読できない: %v", err))
		}
	}

	conn.Signal(signals)

	inhibit := &sleepInhibitor{conn: conn}
	if err := inhibit.take(); err != nil {
		w.warnings = append(w.warnings,
			fmt.Sprintf("サスペンドを遅らせられないため、直前の記録を書けないことがある: %v", err))
	}
	return inhibit
}

// startScreenSaver はスクリーンセーバの監視を始める。
//
// logind の LockedHint を更新しないロッカーがあるため、こちらも見る。
// 同じ操作で両方から通知が来るが、lockState が重複を落とす。
func (w *SessionWatcher) startScreenSaver(signals chan *dbus.Signal) {
	conn, err := sessionBus.get()
	if err != nil {
		w.warnings = append(w.warnings,
			fmt.Sprintf("セッションバスへ繋げないため、画面ロックの検出が弱くなる: %v", err))
		return
	}

	for _, dest := range []string{screenSaverDest, gnomeScreenSaver} {
		if err := conn.AddMatchSignal(
			dbus.WithMatchInterface(dest),
			dbus.WithMatchMember("ActiveChanged"),
		); err != nil {
			w.warnings = append(w.warnings, fmt.Sprintf("%s を購読できない: %v", dest, err))
		}
	}

	// 起動時の状態を種として置く。
	if hasOwner(conn, screenSaverDest) {
		var active bool
		if err := conn.Object(screenSaverDest, "/org/freedesktop/ScreenSaver").
			Call(screenSaverDest+".GetActive", 0).Store(&active); err == nil {
			sharedLockState.seed(active)
		}
	}

	conn.Signal(signals)
}

// handleLogindSignal は logind からの通知を処理する。
func (w *SessionWatcher) handleLogindSignal(signal *dbus.Signal, inhibit *sleepInhibitor) {
	switch signal.Name {
	case logindManagerIfc + ".PrepareForSleep":
		starting, ok := signalBool(signal)
		if !ok {
			return
		}
		if starting {
			w.emit(SessionSuspend)
			// 書き出しの時間を作ってから眠らせる。
			inhibit.releaseAfter(sessionInhibitFlushGrace)
			return
		}
		w.emit(SessionResume)
		// 次のサスペンドに備えて取り直す。
		if err := inhibit.take(); err != nil {
			w.warnings = append(w.warnings, fmt.Sprintf("サスペンドの遅延を取り直せない: %v", err))
		}

	case logindManagerIfc + ".PrepareForShutdown":
		if starting, ok := signalBool(signal); ok && starting {
			w.emit(SessionShutdown)
			inhibit.releaseAfter(sessionInhibitFlushGrace)
		}

	case "org.freedesktop.DBus.Properties.PropertiesChanged":
		if len(signal.Body) < 2 {
			return
		}
		if iface, ok := signal.Body[0].(string); !ok || iface != logindSessionIfc {
			return
		}
		changed, ok := signal.Body[1].(map[string]dbus.Variant)
		if !ok {
			return
		}
		if locked, ok := changed["LockedHint"].Value().(bool); ok {
			w.emitLockChange(locked)
		}
	}
}

// handleScreenSaverSignal はスクリーンセーバからの通知を処理する。
func (w *SessionWatcher) handleScreenSaverSignal(signal *dbus.Signal) {
	switch signal.Name {
	case screenSaverDest + ".ActiveChanged", gnomeScreenSaver + ".ActiveChanged":
		if active, ok := signalBool(signal); ok {
			w.emitLockChange(active)
		}
	}
}

// signalBool は本体が真偽値1つの通知からその値を取り出す。
func signalBool(signal *dbus.Signal) (bool, bool) {
	if len(signal.Body) == 0 {
		return false, false
	}
	value, ok := signal.Body[0].(bool)
	return value, ok
}

// ownSessionPath は自分が属する logind のセッションのパスを返す。
func ownSessionPath(conn *dbus.Conn) (dbus.ObjectPath, error) {
	manager := conn.Object(logindDest, logindManagerPath)

	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		var path dbus.ObjectPath
		if err := manager.Call(logindManagerIfc+".GetSession", 0, id).Store(&path); err == nil {
			return path, nil
		}
	}

	var path dbus.ObjectPath
	err := manager.Call(logindManagerIfc+".GetSessionByPID", 0, uint32(os.Getpid())).Store(&path)
	if err != nil {
		return "", fmt.Errorf("logind のセッションを引けない: %w", err)
	}
	return path, nil
}

// sleepInhibitor はサスペンドとシャットダウンを少しだけ遅らせる権利を持つ。
//
// logind の遅延インヒビタは file descriptor で表され、閉じると解放される。
type sleepInhibitor struct {
	conn *dbus.Conn
	file *os.File
}

// take は遅延インヒビタを取る。
func (i *sleepInhibitor) take() error {
	if i == nil || i.file != nil {
		return nil
	}

	var fd dbus.UnixFD
	err := i.conn.Object(logindDest, logindManagerPath).
		Call(logindManagerIfc+".Inhibit", 0,
			"sleep:shutdown", "gkill_autolog", "操作ログを書き出す", "delay").
		Store(&fd)
	if err != nil {
		return err
	}
	i.file = os.NewFile(uintptr(fd), "logind-inhibit")
	return nil
}

// release は遅延インヒビタを手放す。手放すと機械が眠れるようになる。
func (i *sleepInhibitor) release() {
	if i == nil || i.file == nil {
		return
	}
	_ = i.file.Close()
	i.file = nil
}

// releaseAfter は少し待ってから手放す。書き出しの時間を作るために使う。
func (i *sleepInhibitor) releaseAfter(wait time.Duration) {
	if i == nil {
		return
	}
	time.Sleep(wait)
	i.release()
}

// IsSessionLocked は画面がロックされているかを返す。
//
// collect が動いていれば SessionWatcher が状態を更新し続けているので、
// D-Bus を叩かずに即答する。autolog screenshot を単体で叩いたときだけ
// その場で1回問い合わせる。
//
// どの手段でも分からなければ false（ロックされていない）を返す。
// ロック画面を撮っても漏れるのはロック画面そのものだが、
// 撮らない側に倒すと撮影が丸ごと止まる。
func IsSessionLocked() bool {
	if locked, known := sharedLockState.get(); known {
		return locked
	}
	if locked, ok := queryLocked(); ok {
		return locked
	}
	return false
}

// queryLocked は現在のロック状態を1回だけ問い合わせる。
func queryLocked() (bool, bool) {
	if conn, err := systemBus.get(); err == nil && hasOwner(conn, logindDest) {
		if path, err := ownSessionPath(conn); err == nil {
			if value, err := busProperty(conn, logindDest, path, logindSessionIfc, "LockedHint"); err == nil {
				if locked, ok := value.Value().(bool); ok {
					return locked, true
				}
			}
		}
	}

	if conn, err := sessionBus.get(); err == nil && hasOwner(conn, screenSaverDest) {
		var active bool
		if err := conn.Object(screenSaverDest, "/org/freedesktop/ScreenSaver").
			Call(screenSaverDest+".GetActive", 0).Store(&active); err == nil {
			return active, true
		}
	}
	return false, false
}
