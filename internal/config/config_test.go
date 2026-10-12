package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFile 验证所有运行配置均从 YAML 读取，并生成正确 MySQL DSN。
func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "feishu:\n  stream-enabled: false\n  apps:\n    - name: 测试机器人\n      app-id: cli_test\n      app-secret: secret\n    - name: 第二机器人\n      app-id: cli_test_2\n      app-secret: secret2\nmysql:\n  path: 127.0.0.1\n  port: '3306'\n  username: root\n  password: secret\n  db-name: gva\n  config: charset=utf8mb4&parseTime=True&loc=Local\nplatform:\n  base-url: http://platform/\n  username: hub\n  password: secret\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if cfg.Platform.BaseURL != "http://platform" || len(cfg.Feishu.Apps) != 2 || cfg.Feishu.Apps[0].AppID != "cli_test" {
		t.Fatalf("config = %#v", cfg)
	}
	if cfg.Feishu.IsStreamEnabled() {
		t.Fatal("stream-enabled: false was not applied")
	}
	if cfg.MySQL.DSN() != "root:secret@tcp(127.0.0.1:3306)/gva?charset=utf8mb4&parseTime=True&loc=Local" {
		t.Fatal("unexpected MySQL DSN")
	}
}

// TestLoadFileSupportsLegacySingleApp 验证升级时原单机器人配置仍可启动。
func TestLoadFileSupportsLegacySingleApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "feishu:\n  app-id: cli_test\n  app-secret: secret\nmysql:\n  path: 127.0.0.1\n  port: '3306'\n  username: root\n  password: secret\n  db-name: gva\nplatform:\n  base-url: http://platform\n  username: hub\n  password: secret\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(cfg.Feishu.Apps) != 1 || cfg.Feishu.Apps[0].AppID != "cli_test" {
		t.Fatalf("legacy apps = %#v", cfg.Feishu.Apps)
	}
}
