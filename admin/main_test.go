package main

import (
	"database/sql"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTemplatesParse 验证总览与三个管理页共用的模板定义能在服务启动前完整解析。
func TestTemplatesParse(t *testing.T) {
	if _, err := template.ParseFS(templateFiles, "templates/*.html"); err != nil {
		t.Fatalf("解析后台模板失败: %v", err)
	}
}

// TestLoadConfigMySQL 验证参数 path 为后台配置文件、其 router-config 指向 MySQL 存储配置时，
// loadConfig 返回 mysql 驱动及由嵌套 storage.mysql 字段组成的 DSN，防止 YAML 字段被意外忽略。
func TestLoadConfigMySQL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	routerConfigPath := filepath.Join(dir, "storage.yaml")
	writeTestConfig(t, routerConfigPath, `storage:
  driver: mysql
  mysql:
    path: db.internal
    port: "3307"
    username: router
    password: secret
    db-name: hermes
    config: charset=utf8mb4&parseTime=True
`)
	adminConfigPath := filepath.Join(dir, "admin.yaml")
	writeTestConfig(t, adminConfigPath, `router-config: `+routerConfigPath+`
auth:
  username: admin
  password-hash: hash
  session-secret: 12345678901234567890123456789012
`)

	_, driver, dsn, err := loadConfig(adminConfigPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if driver != "mysql" {
		t.Errorf("driver = %q, want mysql", driver)
	}
	const wantDSN = "router:secret@tcp(db.internal:3307)/hermes?charset=utf8mb4&parseTime=True"
	if dsn != wantDSN {
		t.Errorf("dsn = %q, want %q", dsn, wantDSN)
	}
}

// TestLoadConfigRejectsUnsupportedStorage 验证不支持的 storage.driver 会返回明确错误，
// 参数为后台配置及其 Router 存储配置，出参仅检查错误信息以避免无效配置继续按 MySQL 处理。
func TestLoadConfigRejectsUnsupportedStorage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	routerConfigPath := filepath.Join(dir, "storage.yaml")
	writeTestConfig(t, routerConfigPath, "storage:\n  driver: postgres\n")
	adminConfigPath := filepath.Join(dir, "admin.yaml")
	writeTestConfig(t, adminConfigPath, `router-config: `+routerConfigPath+`
auth:
  username: admin
  password-hash: hash
  session-secret: 12345678901234567890123456789012
`)

	_, _, _, err := loadConfig(adminConfigPath)
	if err == nil || !strings.Contains(err.Error(), "不支持的 storage.driver: postgres") {
		t.Fatalf("loadConfig() error = %v, want unsupported-driver error", err)
	}
}

// TestLoadConfigNormalizesStorageDriver 验证带空格和大小写差异的 sqlite 驱动配置，
// 输入为后台配置及其 Router 存储配置，输出应为与 Router 相同的 sqlite 驱动和 DSN。
func TestLoadConfigNormalizesStorageDriver(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	routerConfigPath := filepath.Join(dir, "storage.yaml")
	writeTestConfig(t, routerConfigPath, "storage:\n  driver: ' SQLite '\n  sqlite-path: /tmp/router.db\n")
	adminConfigPath := filepath.Join(dir, "admin.yaml")
	writeTestConfig(t, adminConfigPath, `router-config: `+routerConfigPath+`
auth:
  username: admin
  password-hash: hash
  session-secret: 12345678901234567890123456789012
`)

	_, driver, dsn, err := loadConfig(adminConfigPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if driver != "sqlite" {
		t.Errorf("driver = %q, want sqlite", driver)
	}
	if !strings.HasPrefix(dsn, "file:/tmp/router.db?") {
		t.Errorf("dsn = %q, want sqlite DSN", dsn)
	}
}

// TestMigrateSQLiteCreatesFinalSchema 验证空 SQLite 数据库调用后台建表后，
// 所有后台与 Router 共用表均可查询，保证任一服务先启动时最终结构完整。
func TestMigrateSQLiteCreatesFinalSchema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:admin-migrate-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("打开 SQLite: %v", err)
	}
	defer db.Close()

	a := &app{db: db, driver: "sqlite"}
	if err := a.migrate(); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	for _, table := range []string{"ekko_container", "feishu_user_route", "feishu_ekko_session", "feishu_event_dedup", "feishu_unmatched_identity", "feishu_external_route"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("表 %s 未创建: %v", table, err)
		}
	}
}

// writeTestConfig 将 content 写入 path；用于构造 loadConfig 的独立文件输入，写入失败即终止对应测试。
func writeTestConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试配置 %s: %v", path, err)
	}
}
