// hermes-router-admin 是独立于消息 Router 的轻量管理服务。
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*.html
var templateFiles embed.FS

type config struct {
	Listen       string `yaml:"listen"`
	RouterConfig string `yaml:"router-config"`
	Auth         struct {
		Username      string `yaml:"username"`
		PasswordHash  string `yaml:"password-hash"`
		SessionSecret string `yaml:"session-secret"`
	} `yaml:"auth"`
}

type routerConfig struct {
	MySQL struct {
		Path, Port, Username, Password, DBName, Config string `yaml:"-"`
	} `yaml:"mysql"`
}

type app struct {
	db        *sql.DB
	cfg       config
	templates *template.Template
}

// loadConfig 加载后台认证配置以及 Router 的数据库连接配置，避免重复存放数据库密码。
func loadConfig(path string) (config, string, error) {
	var cfg config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, "", err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, "", err
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8081"
	}
	if cfg.RouterConfig == "" || cfg.Auth.Username == "" || cfg.Auth.PasswordHash == "" || len(cfg.Auth.SessionSecret) < 32 {
		return cfg, "", errors.New("后台配置缺少 router-config、认证账号、密码哈希或至少 32 字节 session-secret")
	}
	var raw struct {
		MySQL struct {
			Path     string `yaml:"path"`
			Port     string `yaml:"port"`
			Username string `yaml:"username"`
			Password string `yaml:"password"`
			DBName   string `yaml:"db-name"`
			Config   string `yaml:"config"`
		} `yaml:"mysql"`
	}
	rb, err := os.ReadFile(cfg.RouterConfig)
	if err != nil {
		return cfg, "", err
	}
	if err := yaml.Unmarshal(rb, &raw); err != nil {
		return cfg, "", err
	}
	if raw.MySQL.Path == "" || raw.MySQL.Port == "" || raw.MySQL.Username == "" || raw.MySQL.DBName == "" {
		return cfg, "", errors.New("Router 配置的 MySQL 字段不完整")
	}
	return cfg, fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?%s", raw.MySQL.Username, raw.MySQL.Password, raw.MySQL.Path, raw.MySQL.Port, raw.MySQL.DBName, raw.MySQL.Config), nil
}

func main() {
	path := "config.yaml"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	cfg, dsn, err := loadConfig(path)
	if err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}
	a := &app{db: db, cfg: cfg, templates: template.Must(template.ParseFS(templateFiles, "templates/*.html"))}
	if err := a.migrate(); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", a.login)
	mux.HandleFunc("/logout", a.logout)
	mux.HandleFunc("/", a.auth(a.dashboard))
	mux.HandleFunc("/containers", a.auth(a.containers))
	mux.HandleFunc("/routes", a.auth(a.routes))
	mux.HandleFunc("/external-routes", a.auth(a.externalRoutes))
	mux.HandleFunc("/containers/save", a.auth(a.saveContainer))
	mux.HandleFunc("/routes/save", a.auth(a.saveRoute))
	mux.HandleFunc("/external-routes/save", a.auth(a.saveExternalRoute))
	mux.HandleFunc("/external/bind", a.auth(a.bindExternal))
	log.Printf("Hermes Router Admin 已启动: http://127.0.0.1%s", cfg.Listen)
	log.Fatal(http.ListenAndServe(cfg.Listen, secureHeaders(mux)))
}

// migrate 创建后台专用的外部身份待配置与绑定表，不影响 Router 既有表。
func (a *app) migrate() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS feishu_unmatched_identity (app_id VARCHAR(64) NOT NULL, open_id VARCHAR(128) NOT NULL, union_id VARCHAR(128) NOT NULL DEFAULT '', sender_tenant_key VARCHAR(128) NOT NULL DEFAULT '', chat_id VARCHAR(128) NOT NULL DEFAULT '', first_message_id VARCHAR(128) NOT NULL DEFAULT '', latest_message_id VARCHAR(128) NOT NULL DEFAULT '', latest_message_preview VARCHAR(256) NULL, first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, status VARCHAR(16) NOT NULL DEFAULT 'pending', bound_container_id VARCHAR(64) NULL, bound_at DATETIME NULL, bound_by VARCHAR(64) NULL, PRIMARY KEY (app_id, open_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS feishu_external_route (app_id VARCHAR(64) NOT NULL, open_id VARCHAR(128) NOT NULL, container_id VARCHAR(64) NOT NULL, username VARCHAR(128) NOT NULL, enabled TINYINT(1) NOT NULL DEFAULT 1, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY (app_id, open_id), CONSTRAINT fk_external_route_container FOREIGN KEY (container_id) REFERENCES ekko_container(container_id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	} {
		if _, err := a.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
func (a *app) sign(value string) string {
	mac := hmac.New(sha256.New, []byte(a.cfg.Auth.SessionSecret))
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *app) loggedIn(r *http.Request) bool {
	c, err := r.Cookie("hermes_admin")
	if err != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	return len(parts) == 2 && hmac.Equal([]byte(parts[1]), []byte(a.sign(parts[0]))) && parts[0] == a.cfg.Auth.Username
}
func (a *app) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.loggedIn(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.render(w, "login.html", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	if r.Form.Get("username") != a.cfg.Auth.Username || bcrypt.CompareHashAndPassword([]byte(a.cfg.Auth.PasswordHash), []byte(r.Form.Get("password"))) != nil {
		a.render(w, "login.html", map[string]any{"Error": "账号或密码错误"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "hermes_admin", Value: a.cfg.Auth.Username + "." + a.sign(a.cfg.Auth.Username), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 8 * 3600})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "hermes_admin", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", 303)
}
func (a *app) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Nav": "overview", "ContainerCount": count(a.db, `SELECT COUNT(*) FROM ekko_container`), "RouteCount": count(a.db, `SELECT COUNT(*) FROM feishu_user_route`), "ExternalRouteCount": count(a.db, `SELECT COUNT(*) FROM feishu_external_route`), "PendingCount": count(a.db, `SELECT COUNT(*) FROM feishu_unmatched_identity WHERE status='pending'`), "Pending": rows(a.db, `SELECT app_id, open_id, chat_id, latest_message_preview, last_seen_at FROM feishu_unmatched_identity WHERE status='pending' ORDER BY last_seen_at DESC LIMIT 8`)}
	a.render(w, "dashboard.html", data)
}

const pageSize = 12

// listData 统一提供三个表格页的查询与分页字段。
func listData(db *sql.DB, r *http.Request, nav, countSQL, listSQL string, args []any, editSQL string, editArgs []any) map[string]any {
	page := 1
	if _, err := fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page); err != nil || page < 1 {
		page = 1
	}
	total := countWithArgs(db, countSQL, args...)
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	listArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	data := map[string]any{"Nav": nav, "Rows": rowsArgs(db, listSQL, listArgs...), "Query": r.URL.Query().Get("q"), "Page": page, "Pages": pages, "Total": total, "Prev": max(page-1, 1), "Next": min(page+1, pages)}
	if editSQL != "" {
		data["Edit"] = row(db, editSQL, editArgs...)
	}
	return data
}

func (a *app) containers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	like := "%" + q + "%"
	editID := strings.TrimSpace(r.URL.Query().Get("edit"))
	data := listData(a.db, r, "containers", `SELECT COUNT(*) FROM ekko_container WHERE container_id LIKE ? OR profile LIKE ?`, `SELECT container_id, profile, enabled, updated_at FROM ekko_container WHERE container_id LIKE ? OR profile LIKE ? ORDER BY updated_at DESC LIMIT ? OFFSET ?`, []any{like, like}, `SELECT container_id, profile, enabled FROM ekko_container WHERE container_id=?`, []any{editID})
	a.render(w, "containers.html", data)
}

func (a *app) routes(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	like := "%" + q + "%"
	editID := strings.TrimSpace(r.URL.Query().Get("edit"))
	data := listData(a.db, r, "routes", `SELECT COUNT(*) FROM feishu_user_route WHERE feishu_user_id LIKE ? OR container_id LIKE ? OR username LIKE ?`, `SELECT feishu_user_id, container_id, username, enabled, updated_at FROM feishu_user_route WHERE feishu_user_id LIKE ? OR container_id LIKE ? OR username LIKE ? ORDER BY updated_at DESC LIMIT ? OFFSET ?`, []any{like, like, like}, `SELECT feishu_user_id, container_id, username, enabled FROM feishu_user_route WHERE feishu_user_id=?`, []any{editID})
	a.render(w, "routes.html", data)
}

func (a *app) externalRoutes(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	like := "%" + q + "%"
	appID, openID := strings.TrimSpace(r.URL.Query().Get("app_id")), strings.TrimSpace(r.URL.Query().Get("open_id"))
	data := listData(a.db, r, "external", `SELECT COUNT(*) FROM feishu_external_route WHERE app_id LIKE ? OR open_id LIKE ? OR container_id LIKE ? OR username LIKE ?`, `SELECT app_id, open_id, container_id, username, enabled, updated_at FROM feishu_external_route WHERE app_id LIKE ? OR open_id LIKE ? OR container_id LIKE ? OR username LIKE ? ORDER BY updated_at DESC LIMIT ? OFFSET ?`, []any{like, like, like, like}, `SELECT app_id, open_id, container_id, username, enabled FROM feishu_external_route WHERE app_id=? AND open_id=?`, []any{appID, openID})
	if edit := data["Edit"].(map[string]string); len(edit) == 0 && appID != "" && openID != "" {
		data["Edit"] = map[string]string{"app_id": appID, "open_id": openID, "enabled": "1"}
	}
	a.render(w, "external_routes.html", data)
}

func rows(db *sql.DB, q string) []map[string]string {
	return rowsArgs(db, q)
}
func rowsArgs(db *sql.DB, q string, args ...any) []map[string]string {
	rs, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	out := []map[string]string{}
	for rs.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rs.Scan(ptrs...) != nil {
			continue
		}
		row := map[string]string{}
		for i, c := range cols {
			row[c] = vals[i].String
		}
		out = append(out, row)
	}
	return out
}
func row(db *sql.DB, q string, args ...any) map[string]string {
	list := rowsArgs(db, q, args...)
	if len(list) == 0 {
		return map[string]string{}
	}
	return list[0]
}
func count(db *sql.DB, q string) int { return countWithArgs(db, q) }
func countWithArgs(db *sql.DB, q string, args ...any) int {
	var n int
	_ = db.QueryRow(q, args...).Scan(&n)
	return n
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func (a *app) saveContainer(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "仅支持 POST", 405)
		return
	}
	id := strings.TrimSpace(r.FormValue("container_id"))
	if id == "" {
		http.Error(w, "container_id 不能为空", 400)
		return
	}
	_, err := a.db.Exec(`INSERT INTO ekko_container (container_id,profile,enabled) VALUES (?,?,?) ON DUPLICATE KEY UPDATE profile=VALUES(profile),enabled=VALUES(enabled)`, id, strings.TrimSpace(r.FormValue("profile")), r.FormValue("enabled") == "1")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/containers", http.StatusSeeOther)
}
func (a *app) saveRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "仅支持 POST", 405)
		return
	}
	id, container := strings.TrimSpace(r.FormValue("feishu_user_id")), strings.TrimSpace(r.FormValue("container_id"))
	username := strings.TrimSpace(r.FormValue("username"))
	if id == "" || container == "" || username == "" {
		http.Error(w, "内部路由字段不能为空", http.StatusBadRequest)
		return
	}
	tx, err := a.db.Begin()
	if err == nil {
		err = ensureContainer(tx, container)
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO feishu_user_route (feishu_user_id,container_id,username,enabled) VALUES (?,?,?,?) ON DUPLICATE KEY UPDATE container_id=VALUES(container_id),username=VALUES(username),enabled=VALUES(enabled)`, id, container, username, r.FormValue("enabled") == "1")
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/routes", http.StatusSeeOther)
}

// ensureContainer 在保存路由前确保其目标容器存在。
// containerID 是表单提交的容器 ID；找不到时会创建启用状态、profile 为 default 的基础容器。
func ensureContainer(tx *sql.Tx, containerID string) error {
	_, err := tx.Exec(`INSERT INTO ekko_container (container_id,profile,enabled) VALUES (?, 'default', 1) ON DUPLICATE KEY UPDATE container_id=container_id`, containerID)
	return err
}

// saveExternalRoute 新增或更新入口应用下外部用户的 Hermes 路由。
func (a *app) saveExternalRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	appID, openID := strings.TrimSpace(r.FormValue("app_id")), strings.TrimSpace(r.FormValue("open_id"))
	container, username := strings.TrimSpace(r.FormValue("container_id")), strings.TrimSpace(r.FormValue("username"))
	if appID == "" || openID == "" || container == "" || username == "" {
		http.Error(w, "外部路由字段不能为空", http.StatusBadRequest)
		return
	}
	tx, err := a.db.Begin()
	if err == nil {
		err = ensureContainer(tx, container)
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO feishu_external_route (app_id,open_id,container_id,username,enabled) VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE container_id=VALUES(container_id),username=VALUES(username),enabled=VALUES(enabled)`, appID, openID, container, username, r.FormValue("enabled") == "1")
	}
	if err == nil {
		// 外部路由保存成功即代表管理员已完成确认；更新不到记录时同样视为成功。
		_, err = tx.Exec(`UPDATE feishu_unmatched_identity SET status='bound',bound_container_id=?,bound_at=?,bound_by=? WHERE app_id=? AND open_id=? AND status='pending'`, container, time.Now(), a.cfg.Auth.Username, appID, openID)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/external-routes", http.StatusSeeOther)
}
func (a *app) bindExternal(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "仅支持 POST", 405)
		return
	}
	appID, openID := strings.TrimSpace(r.FormValue("app_id")), strings.TrimSpace(r.FormValue("open_id"))
	container, username := strings.TrimSpace(r.FormValue("container_id")), strings.TrimSpace(r.FormValue("username"))
	if appID == "" || openID == "" || container == "" || username == "" {
		http.Error(w, "绑定字段不能为空", 400)
		return
	}
	tx, err := a.db.Begin()
	if err == nil {
		err = ensureContainer(tx, container)
	}
	if err == nil {
		_, err = tx.Exec(`INSERT INTO feishu_external_route (app_id,open_id,container_id,username,enabled) VALUES (?,?,?,?,1) ON DUPLICATE KEY UPDATE container_id=VALUES(container_id),username=VALUES(username),enabled=1`, appID, openID, container, username)
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE feishu_unmatched_identity SET status='bound',bound_container_id=?,bound_at=?,bound_by=? WHERE app_id=? AND open_id=?`, container, time.Now(), a.cfg.Auth.Username, appID, openID)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/", 303)
}
