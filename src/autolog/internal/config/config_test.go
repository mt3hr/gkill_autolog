package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfigFile はテスト用の config.env を書く。
func writeConfigFile(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ConfigFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
}

func TestLoadReadsConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	writeConfigFile(t, home, `# Android 用の設定
GKILL_BASE_URL=http://127.0.0.1:9999
GKILL_AUTO_USER_PREFIX=user_auto_
GKILL_AUTO_PASSWORD_SHA256=abc123
AUTOLOG_DEVICE=Phone
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.GkillBaseURL != "http://127.0.0.1:9999" {
		t.Errorf("GkillBaseURL = %s", cfg.GkillBaseURL)
	}
	if cfg.Device != "Phone" {
		t.Errorf("Device = %s, want Phone", cfg.Device)
	}
	if !cfg.UseAutoUsers() {
		t.Error("端末別ユーザーの設定として認識されなかった")
	}
	if got := cfg.GkillUserFor("Phone"); got != "user_auto_Phone" {
		t.Errorf("GkillUserFor = %s, want user_auto_Phone", got)
	}
}

// 環境変数が config.env より優先されること。
func TestEnvOverridesConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvDevice, "Laptop")

	writeConfigFile(t, home, "AUTOLOG_DEVICE=Phone\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Device != "Laptop" {
		t.Errorf("Device = %s, want Laptop (環境変数が優先されるはず)", cfg.Device)
	}
}

// config.env が無くても動くこと（PC は環境変数だけで動かす）。
func TestLoadWithoutConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GkillBaseURL != DefaultGkillBaseURL {
		t.Errorf("GkillBaseURL = %s, want %s", cfg.GkillBaseURL, DefaultGkillBaseURL)
	}
}

func TestLoadConfigFileIgnoresCommentsAndBlanks(t *testing.T) {
	home := t.TempDir()
	writeConfigFile(t, home, "\n# コメント\n\nGKILL_USER=user\n値のない行\n  GKILL_INSECURE = true  \n")

	settings, err := loadConfigFile(filepath.Join(home, ConfigFileName))
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if settings["GKILL_USER"] != "user" {
		t.Errorf("GKILL_USER = %q", settings["GKILL_USER"])
	}
	// 前後の空白は落とす。
	if settings["GKILL_INSECURE"] != "true" {
		t.Errorf("GKILL_INSECURE = %q", settings["GKILL_INSECURE"])
	}
	if len(settings) != 2 {
		t.Errorf("読み込んだ件数 = %d, want 2: %#v", len(settings), settings)
	}
}

// Windows のエディタで保存した BOM 付きファイルでも先頭行を読めること。
func TestLoadConfigFileHandlesBOM(t *testing.T) {
	home := t.TempDir()
	writeConfigFile(t, home, "\ufeffGKILL_USER=user\n")

	settings, err := loadConfigFile(filepath.Join(home, ConfigFileName))
	if err != nil {
		t.Fatalf("loadConfigFile: %v", err)
	}
	if settings["GKILL_USER"] != "user" {
		t.Errorf("BOM 付きの先頭行を読めていない: %#v", settings)
	}
}
