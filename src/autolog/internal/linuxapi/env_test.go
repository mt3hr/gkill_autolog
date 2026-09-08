package linuxapi

import "testing"

// fakeEnv は環境変数の読み手。
func fakeEnv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestEnvironmentDetectsCompositor(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want string
	}{
		{
			name: "sway は IPC ソケットで分かる",
			vars: map[string]string{"SWAYSOCK": "/run/user/1000/sway-ipc.sock", "XDG_CURRENT_DESKTOP": "sway"},
			want: compositorSway,
		},
		{
			name: "i3 のソケットも sway と同じ扱い",
			vars: map[string]string{"I3SOCK": "/run/user/1000/i3/ipc.sock"},
			want: compositorSway,
		},
		{
			name: "Hyprland は実行ごとの識別子で分かる",
			vars: map[string]string{"HYPRLAND_INSTANCE_SIGNATURE": "abc123"},
			want: compositorHyprland,
		},
		{
			name: "コロン区切りなら先頭を採る",
			vars: map[string]string{"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"},
			want: "ubuntu",
		},
		{
			name: "XDG_CURRENT_DESKTOP が無ければ DESKTOP_SESSION",
			vars: map[string]string{"DESKTOP_SESSION": "plasma"},
			want: "plasma",
		},
		{
			name: "手がかりが無ければ不明",
			vars: map[string]string{},
			want: compositorUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := environmentFrom(fakeEnv(tt.vars)).Compositor; got != tt.want {
				t.Errorf("合成器 = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnvironmentWaylandBeatsXWayland(t *testing.T) {
	// Wayland では XWayland のために DISPLAY が入っていることが多い。
	// DISPLAY があるからと X11 の経路を選ぶと、ネイティブの Wayland
	// クライアントが前面のとき _NET_ACTIVE_WINDOW が古い値を返し続け、
	// 「ずっと同じアプリを使っていた」という偽の記録になる。
	env := environmentFrom(fakeEnv(map[string]string{
		"XDG_SESSION_TYPE": "wayland",
		"WAYLAND_DISPLAY":  "wayland-0",
		"DISPLAY":          ":0",
	}))

	if !env.IsWayland() {
		t.Error("Wayland のセッションだと判定されていない")
	}
	if env.IsX11() {
		t.Error("XWayland の DISPLAY を X11 のセッションだと判定している")
	}
}

func TestEnvironmentX11(t *testing.T) {
	env := environmentFrom(fakeEnv(map[string]string{
		"XDG_SESSION_TYPE": "x11",
		"DISPLAY":          ":0",
	}))

	if !env.IsX11() {
		t.Error("X11 のセッションだと判定されていない")
	}
	if env.IsWayland() {
		t.Error("X11 なのに Wayland だと判定している")
	}
}

func TestEnvironmentWaylandWithoutSessionType(t *testing.T) {
	// XDG_SESSION_TYPE を設定しない環境もある。
	// その場合は WAYLAND_DISPLAY の有無で決める。
	env := environmentFrom(fakeEnv(map[string]string{"WAYLAND_DISPLAY": "wayland-0"}))
	if !env.IsWayland() {
		t.Error("WAYLAND_DISPLAY があるのに Wayland だと判定されていない")
	}
}

func TestEnvironmentIsUserSession(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want bool
	}{
		{name: "X11", vars: map[string]string{"DISPLAY": ":0"}, want: true},
		{name: "Wayland", vars: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, want: true},
		{
			name: "画面は無いが対話セッション",
			vars: map[string]string{"XDG_SESSION_TYPE": "tty", "XDG_SESSION_ID": "3"},
			want: true,
		},
		{
			name: "セッションIDが無ければ人が使っているとは言えない",
			vars: map[string]string{"XDG_SESSION_TYPE": "tty"},
			want: false,
		},
		{name: "cron やサービスからの実行", vars: map[string]string{}, want: false},
		{
			name: "systemd のユーザーセッション外",
			vars: map[string]string{"XDG_SESSION_TYPE": "unspecified", "XDG_SESSION_ID": "5"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := environmentFrom(fakeEnv(tt.vars)).IsUserSession(); got != tt.want {
				t.Errorf("人が使っているセッションか = %v, want %v", got, tt.want)
			}
		})
	}
}
