package linuxapi

import (
	"os"
	"path/filepath"
	"testing"
)

// writePowerSupply は偽の /sys/class/power_supply を1つ作る。
func writePowerSupply(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

func TestChargingFromSysfs(t *testing.T) {
	tests := []struct {
		name     string
		supplies map[string]map[string]string
		want     bool
		wantOK   bool
	}{
		{
			name: "コンセントにつながっている",
			supplies: map[string]map[string]string{
				"AC":   {"type": "Mains", "online": "1"},
				"BAT0": {"type": "Battery", "status": "Charging"},
			},
			want: true, wantOK: true,
		},
		{
			name: "コンセントが抜けている",
			supplies: map[string]map[string]string{
				"AC":   {"type": "Mains", "online": "0"},
				"BAT0": {"type": "Battery", "status": "Discharging"},
			},
			want: false, wantOK: true,
		},
		{
			name: "満充電でもコンセントにつながっていれば充電中として扱う",
			supplies: map[string]map[string]string{
				"AC":   {"type": "Mains", "online": "1"},
				"BAT0": {"type": "Battery", "status": "Full"},
			},
			want: true, wantOK: true,
		},
		{
			name: "コンセントが複数あればどれか1つでつながっていればよい",
			supplies: map[string]map[string]string{
				"AC":  {"type": "Mains", "online": "0"},
				"AC2": {"type": "Mains", "online": "1"},
			},
			want: true, wantOK: true,
		},
		{
			name: "USB からの給電。方式は区別しない",
			supplies: map[string]map[string]string{
				"ucsi-source": {"type": "USB_PD", "online": "1"},
				"BAT0":        {"type": "Battery", "status": "Charging"},
			},
			want: true, wantOK: true,
		},
		{
			name: "電源側の情報が無ければバッテリの状態から判断する",
			supplies: map[string]map[string]string{
				"BAT0": {"type": "Battery", "status": "Charging"},
			},
			want: true, wantOK: true,
		},
		{
			name: "バッテリの状態が不明なら判断しない",
			supplies: map[string]map[string]string{
				"BAT0": {"type": "Battery", "status": "Unknown"},
			},
			want: false, wantOK: false,
		},
		{
			name:     "電源の情報が無い据え置き機では判断しない",
			supplies: map[string]map[string]string{},
			want:     false, wantOK: false,
		},
		{
			name: "online が読めないコンセントは判断に使わない",
			supplies: map[string]map[string]string{
				"AC": {"type": "Mains"},
			},
			want: false, wantOK: false,
		},
		{
			name: "種別が読めないものは無視する",
			supplies: map[string]map[string]string{
				"weird": {"online": "1"},
				"AC":    {"type": "Mains", "online": "0"},
			},
			want: false, wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, files := range tt.supplies {
				writePowerSupply(t, root, name, files)
			}

			got, ok := chargingFromSysfs(root)
			if ok != tt.wantOK {
				t.Fatalf("判断できたか = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("充電中 = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChargingFromSysfsMissingRoot(t *testing.T) {
	// sysfs が無い環境（コンテナなど）でも落ちない。
	if _, ok := chargingFromSysfs(filepath.Join(t.TempDir(), "ない")); ok {
		t.Error("電源の情報が無いのに判断できたことになっている")
	}
}
