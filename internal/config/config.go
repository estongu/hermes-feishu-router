// Package config 负责从 config.yaml 读取 Hub 全部运行配置。
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是启动 Hub 所需的完整运行配置，统一来自 config.yaml。
type Config struct {
	Feishu  FeishuConfig  `yaml:"feishu"`
	Storage StorageConfig `yaml:"storage"`
	// MySQL 保留用于兼容旧版配置；新部署请使用 storage。
	MySQL    MySQLConfig    `yaml:"mysql"`
	Platform PlatformConfig `yaml:"platform"`
}

// StorageConfig 描述 Router 使用的持久化存储。SQLite 适合单机部署；MySQL 用于多实例部署。
type StorageConfig struct {
	Driver     string      `yaml:"driver"`
	SQLitePath string      `yaml:"sqlite-path"`
	MySQL      MySQLConfig `yaml:"mysql"`
}

// DriverAndDSN 返回 database/sql 所需驱动和 DSN；未配置 storage 时兼容旧 MySQL 配置。
func (c StorageConfig) DriverAndDSN(legacy MySQLConfig) (string, string, error) {
	driver := strings.ToLower(strings.TrimSpace(c.Driver))
	if driver == "" {
		if legacy.Path == "" {
			return "", "", fmt.Errorf("storage.driver 不能为空")
		}
		return "mysql", legacy.DSN(), nil
	}
	switch driver {
	case "sqlite":
		path := strings.TrimSpace(c.SQLitePath)
		if path == "" {
			return "", "", fmt.Errorf("storage.sqlite-path 不能为空")
		}
		return "sqlite", fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path), nil
	case "mysql":
		if c.MySQL.Path == "" || c.MySQL.Port == "" || c.MySQL.Username == "" || c.MySQL.Password == "" || c.MySQL.DBName == "" {
			return "", "", fmt.Errorf("storage.mysql 的必填字段不能为空")
		}
		return "mysql", c.MySQL.DSN(), nil
	default:
		return "", "", fmt.Errorf("不支持的 storage.driver: %s", c.Driver)
	}
}

type FeishuConfig struct {
	Apps           []FeishuAppConfig `yaml:"apps"`
	StreamEnabled  *bool             `yaml:"stream-enabled"`
	DebugLogEvents bool              `yaml:"debug-log-events"`
	// AppID、AppSecret 保留用于兼容单机器人旧配置；新部署请使用 apps。
	AppID     string `yaml:"app-id"`
	AppSecret string `yaml:"app-secret"`
}

// IsStreamEnabled 返回是否启用飞书流式输出；未配置时保持向后兼容，默认启用。
func (c FeishuConfig) IsStreamEnabled() bool {
	return c.StreamEnabled == nil || *c.StreamEnabled
}

// FeishuAppConfig 是一个飞书机器人的长连接凭据。每个应用必须使用独立 app-id。
type FeishuAppConfig struct {
	Name      string `yaml:"name"`
	AppID     string `yaml:"app-id"`
	AppSecret string `yaml:"app-secret"`
}

// Applications 返回可启动的机器人列表，同时兼容旧版单机器人配置。
// 返回值中的 Name 仅用于日志，不参与用户或会话路由。
func (c FeishuConfig) Applications() ([]FeishuAppConfig, error) {
	apps := c.Apps
	if len(apps) == 0 && (c.AppID != "" || c.AppSecret != "") {
		apps = []FeishuAppConfig{{Name: "default", AppID: c.AppID, AppSecret: c.AppSecret}}
	}
	if len(apps) == 0 {
		return nil, fmt.Errorf("feishu.apps 至少需要配置一个机器人")
	}
	seen := make(map[string]struct{}, len(apps))
	for index := range apps {
		apps[index].Name = strings.TrimSpace(apps[index].Name)
		apps[index].AppID = strings.TrimSpace(apps[index].AppID)
		apps[index].AppSecret = strings.TrimSpace(apps[index].AppSecret)
		if apps[index].Name == "" {
			apps[index].Name = apps[index].AppID
		}
		if apps[index].AppID == "" || apps[index].AppSecret == "" {
			return nil, fmt.Errorf("feishu.apps[%d] 的 app-id 和 app-secret 不能为空", index)
		}
		if _, exists := seen[apps[index].AppID]; exists {
			return nil, fmt.Errorf("feishu.apps 中存在重复 app-id: %s", apps[index].AppID)
		}
		seen[apps[index].AppID] = struct{}{}
	}
	return apps, nil
}

// MySQLConfig 与用户现有 YAML 字段保持一致。
type MySQLConfig struct {
	Path         string `yaml:"path"`
	Port         string `yaml:"port"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	DBName       string `yaml:"db-name"`
	Config       string `yaml:"config"`
	MaxIdleConns int    `yaml:"max-idle-conns"`
	MaxOpenConns int    `yaml:"max-open-conns"`
}

// DSN 返回 go-sql-driver/mysql 所需连接串；调用方不得记录其返回值。
func (c MySQLConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?%s", c.Username, c.Password, c.Path, c.Port, c.DBName, c.Config)
}

// PlatformConfig 是管理平台登录配置，仅用于读取用户外部 JWT。
type PlatformConfig struct {
	BaseURL  string `yaml:"base-url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// LoadFile 加载并校验 config.yaml；密码与 Secret 不会回显或写入日志。
func LoadFile(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置文件: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置文件: %w", err)
	}
	cfg.Platform.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.Platform.BaseURL), "/")
	cfg.Platform.Username = strings.TrimSpace(cfg.Platform.Username)
	apps, err := cfg.Feishu.Applications()
	if err != nil {
		return Config{}, err
	}
	cfg.Feishu.Apps = apps
	if _, _, err := cfg.Storage.DriverAndDSN(cfg.MySQL); err != nil || cfg.Platform.BaseURL == "" || cfg.Platform.Username == "" || cfg.Platform.Password == "" {
		if err != nil {
			return Config{}, err
		}
		return Config{}, fmt.Errorf("config.yaml 中 platform 的必填字段不能为空")
	}
	return cfg, nil
}
