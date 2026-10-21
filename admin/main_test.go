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

// TestParseRouteCSV 验证一份 CSV 同时包含内部、外部路由时，解析结果能保留身份字段与启用状态；
// 输入为约定表头的 CSV 文本，输出为两条结构化路由，避免批量导入误判路由类型。
func TestParseRouteCSV(t *testing.T) {
	input := `type,feishu_user_id,app_id,open_id,container_id,username,enabled
internal,user_a,,,container_a,alice,true
external,,cli_a,ou_a,container_b,bob,false
`
	routes, err := parseRouteCSV(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseRouteCSV() error = %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("路由数量 = %d，want 2", len(routes))
	}
	if routes[0].routeType != "internal" || routes[0].feishuUserID != "user_a" || !routes[0].enabled {
		t.Errorf("内部路由 = %+v，不符合输入", routes[0])
	}
	if routes[1].routeType != "external" || routes[1].appID != "cli_a" || routes[1].openID != "ou_a" || routes[1].enabled {
		t.Errorf("外部路由 = %+v，不符合输入", routes[1])
	}
}

// TestParseRouteCSVRejectsInvalidInternalRoute 验证内部路由误填外部身份时会在写库前被拒绝；
// 输入为带 open_id 的内部路由，输出应为描述行号的校验错误。
func TestParseRouteCSVRejectsInvalidInternalRoute(t *testing.T) {
	input := "type,feishu_user_id,app_id,open_id,container_id,username,enabled\ninternal,user_a,,ou_a,container_a,alice,true\n"
	_, err := parseRouteCSV(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "第 2 行内部路由") {
		t.Fatalf("parseRouteCSV() error = %v，want internal route validation error", err)
	}
}

// TestSaveImportedRoutesCreatesContainers 验证批量保存会在同一事务中写入两类路由并自动创建缺失容器；
// 输入为已校验的内部和外部路由，输出通过数据库记录检查容器和身份绑定是否存在。
func TestSaveImportedRoutesCreatesContainers(t *testing.T) {
	db, err := sql.Open("sqlite", "file:admin-import-test?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatalf("打开 SQLite: %v", err)
	}
	defer db.Close()
	a := &app{db: db, driver: "sqlite"}
	if err := a.migrate(); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	imported := []importedRoute{
		{line: 2, routeType: "internal", feishuUserID: "user_a", containerID: "container_a", username: "alice", enabled: true},
		{line: 3, routeType: "external", appID: "cli_a", openID: "ou_a", containerID: "container_b", username: "bob", enabled: false},
	}
	if err := a.saveImportedRoutes(imported); err != nil {
		t.Fatalf("saveImportedRoutes() error = %v", err)
	}
	for _, containerID := range []string{"container_a", "container_b"} {
		var profile string
		if err := db.QueryRow(`SELECT profile FROM ekko_container WHERE container_id=?`, containerID).Scan(&profile); err != nil || profile != "default" {
			t.Errorf("容器 %s profile = %q, err = %v，want default", containerID, profile, err)
		}
	}
	var internalCount, externalCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM feishu_user_route WHERE feishu_user_id='user_a'`).Scan(&internalCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM feishu_external_route WHERE app_id='cli_a' AND open_id='ou_a'`).Scan(&externalCount)
	if internalCount != 1 || externalCount != 1 {
		t.Errorf("路由数 internal=%d external=%d，want 1,1", internalCount, externalCount)
	}
}

// writeTestConfig 将 content 写入 path；用于构造 loadConfig 的独立文件输入，写入失败即终止对应测试。
func writeTestConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试配置 %s: %v", path, err)
	}
}
