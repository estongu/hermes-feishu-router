// Package store 提供 SQLite/MySQL 中的员工路由、会话映射与事件去重能力。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"

	"github.com/estongu/hermes-feishu-router/internal/model"
)

var ErrNotFound = errors.New("记录不存在")

// Store 封装 Hub 所需的最小持久化操作，便于业务层进行单元测试。
type Store interface {
	FindTargetByFeishuUserID(ctx context.Context, feishuUserID string) (model.EkkoTarget, error)
	FindTargetByExternalOpenID(ctx context.Context, appID, openID string) (model.EkkoTarget, error)
	RecordUnmatchedIdentity(ctx context.Context, appID, openID, chatID, messageID, preview string) error
	FindSession(ctx context.Context, appID, feishuUserID, chatID string) (model.ChatSession, error)
	SaveSession(ctx context.Context, session model.ChatSession) error
	MarkEventProcessed(ctx context.Context, appID, eventID string) (bool, error)
}

// MySQLStore 是 Store 的 SQL 实现；名称为兼容已有调用保留，driver 可为 mysql 或 sqlite。
type MySQLStore struct {
	db     *sql.DB
	driver string
}

// Open 建立 MySQL 或 SQLite 连接。SQLite 单机部署限制连接数，降低写锁竞争。
func Open(ctx context.Context, driver, dsn string) (*MySQLStore, error) {
	if driver != "mysql" && driver != "sqlite" {
		return nil, fmt.Errorf("不支持的数据库驱动: %s", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 连接: %w", err)
	}
	if driver == "sqlite" {
		db.SetMaxIdleConns(1)
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxIdleConns(10)
		db.SetMaxOpenConns(100)
		db.SetConnMaxLifetime(time.Hour)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接 %s: %w", driver, err)
	}
	return &MySQLStore{db: db, driver: driver}, nil
}

// Close 关闭数据库连接池。
func (s *MySQLStore) Close() error { return s.db.Close() }

// Migrate 创建或升级 Hub 所需数据表；操作可重复执行且不会删除现有数据。
func (s *MySQLStore) Migrate(ctx context.Context) error {
	if s.driver == "sqlite" {
		return s.migrateSQLite(ctx)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ekko_container (
			container_id VARCHAR(64) PRIMARY KEY,
			profile VARCHAR(64) NOT NULL DEFAULT 'default',
			enabled TINYINT(1) NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS feishu_user_route (
			feishu_user_id VARCHAR(128) PRIMARY KEY COMMENT '跨飞书应用稳定的 user_id',
			container_id VARCHAR(64) NOT NULL,
			username VARCHAR(128) NOT NULL COMMENT '匹配 Hermes users 接口的 remark_name',
			enabled TINYINT(1) NOT NULL DEFAULT 1,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			CONSTRAINT fk_user_route_container FOREIGN KEY (container_id) REFERENCES ekko_container(container_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS feishu_ekko_session (
			app_id VARCHAR(64) NOT NULL COMMENT '飞书应用 app_id',
			feishu_user_id VARCHAR(128) NOT NULL,
			chat_id VARCHAR(128) NOT NULL,
			container_id VARCHAR(64) NOT NULL,
			ekko_session_id VARCHAR(128) NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			PRIMARY KEY (app_id, feishu_user_id, chat_id),
			CONSTRAINT fk_session_container FOREIGN KEY (container_id) REFERENCES ekko_container(container_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS feishu_event_dedup (
			app_id VARCHAR(64) NOT NULL,
			event_id VARCHAR(128) NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (app_id, event_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS feishu_unmatched_identity (
			app_id VARCHAR(64) NOT NULL,
			open_id VARCHAR(128) NOT NULL,
			chat_id VARCHAR(128) NOT NULL DEFAULT '',
			first_message_id VARCHAR(128) NOT NULL DEFAULT '',
			latest_message_id VARCHAR(128) NOT NULL DEFAULT '',
			latest_message_preview VARCHAR(256) NULL,
			first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			status VARCHAR(16) NOT NULL DEFAULT 'pending',
			bound_container_id VARCHAR(64) NULL,
			bound_at DATETIME NULL,
			bound_by VARCHAR(64) NULL,
			PRIMARY KEY (app_id, open_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS feishu_external_route (
			app_id VARCHAR(64) NOT NULL,
			open_id VARCHAR(128) NOT NULL,
			container_id VARCHAR(64) NOT NULL,
			username VARCHAR(128) NOT NULL,
			enabled TINYINT(1) NOT NULL DEFAULT 1,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			PRIMARY KEY (app_id, open_id),
			CONSTRAINT fk_external_route_container FOREIGN KEY (container_id) REFERENCES ekko_container(container_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("执行迁移: %w", err)
		}
	}
	if err := s.migrateLegacyIdentityColumns(ctx); err != nil {
		return err
	}
	return nil
}

// migrateSQLite 创建 SQLite 表。时间字段由写入语句维护，避免依赖 MySQL 的 ON UPDATE 扩展。
func (s *MySQLStore) migrateSQLite(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ekko_container (container_id TEXT PRIMARY KEY, profile TEXT NOT NULL DEFAULT 'default', enabled INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS feishu_user_route (feishu_user_id TEXT PRIMARY KEY, container_id TEXT NOT NULL, username TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY (container_id) REFERENCES ekko_container(container_id))`,
		`CREATE TABLE IF NOT EXISTS feishu_ekko_session (app_id TEXT NOT NULL, feishu_user_id TEXT NOT NULL, chat_id TEXT NOT NULL, container_id TEXT NOT NULL, ekko_session_id TEXT NOT NULL, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (app_id, feishu_user_id, chat_id), FOREIGN KEY (container_id) REFERENCES ekko_container(container_id))`,
		`CREATE TABLE IF NOT EXISTS feishu_event_dedup (app_id TEXT NOT NULL, event_id TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (app_id, event_id))`,
		`CREATE TABLE IF NOT EXISTS feishu_unmatched_identity (app_id TEXT NOT NULL, open_id TEXT NOT NULL, chat_id TEXT NOT NULL DEFAULT '', first_message_id TEXT NOT NULL DEFAULT '', latest_message_id TEXT NOT NULL DEFAULT '', latest_message_preview TEXT, first_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, status TEXT NOT NULL DEFAULT 'pending', bound_container_id TEXT, bound_at TEXT, bound_by TEXT, PRIMARY KEY (app_id, open_id))`,
		`CREATE TABLE IF NOT EXISTS feishu_external_route (app_id TEXT NOT NULL, open_id TEXT NOT NULL, container_id TEXT NOT NULL, username TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (app_id, open_id), FOREIGN KEY (container_id) REFERENCES ekko_container(container_id))`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("执行 SQLite 迁移: %w", err)
		}
	}
	return nil
}

// FindTargetByExternalOpenID 返回绑定到当前机器人应用的外部用户路由。
func (s *MySQLStore) FindTargetByExternalOpenID(ctx context.Context, appID, openID string) (model.EkkoTarget, error) {
	const query = `SELECT c.container_id, r.username, c.profile, c.enabled FROM feishu_external_route r JOIN ekko_container c ON c.container_id = r.container_id WHERE r.app_id = ? AND r.open_id = ? AND r.enabled = 1 AND c.enabled = 1`
	var target model.EkkoTarget
	if err := s.db.QueryRowContext(ctx, query, appID, openID).Scan(&target.ContainerID, &target.Username, &target.Profile, &target.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.EkkoTarget{}, ErrNotFound
		}
		return model.EkkoTarget{}, fmt.Errorf("查询外部用户路由: %w", err)
	}
	return target, nil
}

// RecordUnmatchedIdentity 幂等记录外部用户的待配置身份；消息摘要不会保存完整正文。
func (s *MySQLStore) RecordUnmatchedIdentity(ctx context.Context, appID, openID, chatID, messageID, preview string) error {
	runes := []rune(preview)
	preview = string(runes[:min(len(runes), 200)])
	query := `INSERT INTO feishu_unmatched_identity (app_id, open_id, chat_id, first_message_id, latest_message_id, latest_message_preview) VALUES (?, ?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE chat_id=VALUES(chat_id), latest_message_id=VALUES(latest_message_id), latest_message_preview=VALUES(latest_message_preview), last_seen_at=CURRENT_TIMESTAMP`
	if s.driver == "sqlite" {
		query = `INSERT INTO feishu_unmatched_identity (app_id, open_id, chat_id, first_message_id, latest_message_id, latest_message_preview) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(app_id, open_id) DO UPDATE SET chat_id=excluded.chat_id, latest_message_id=excluded.latest_message_id, latest_message_preview=excluded.latest_message_preview, last_seen_at=CURRENT_TIMESTAMP`
	}
	_, err := s.db.ExecContext(ctx, query, appID, openID, chatID, messageID, messageID, string(preview))
	if err != nil {
		return fmt.Errorf("记录待配置外部身份: %w", err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// migrateLegacyIdentityColumns 将旧版 open_id 表结构平滑升级为 user_id。
// 历史 open_id 的值无法推导为 user_id，保留它们仅避免数据丢失；管理员应按 README 更新路由。
func (s *MySQLStore) migrateLegacyIdentityColumns(ctx context.Context) error {
	if s.driver == "sqlite" {
		return nil
	}
	if exists, err := s.columnExists(ctx, "feishu_user_route", "open_id"); err != nil {
		return err
	} else if exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE feishu_user_route CHANGE COLUMN open_id feishu_user_id VARCHAR(128) NOT NULL`); err != nil {
			return fmt.Errorf("升级 feishu_user_route 身份列: %w", err)
		}
	}

	if exists, err := s.columnExists(ctx, "feishu_ekko_session", "open_id"); err != nil {
		return err
	} else if exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE feishu_ekko_session CHANGE COLUMN open_id feishu_user_id VARCHAR(128) NOT NULL`); err != nil {
			return fmt.Errorf("升级 feishu_ekko_session 身份列: %w", err)
		}
	}
	if exists, err := s.columnExists(ctx, "feishu_ekko_session", "app_id"); err != nil {
		return err
	} else if !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE feishu_ekko_session ADD COLUMN app_id VARCHAR(64) NOT NULL DEFAULT '' FIRST, DROP PRIMARY KEY, ADD PRIMARY KEY (app_id, feishu_user_id, chat_id)`); err != nil {
			return fmt.Errorf("升级 feishu_ekko_session 应用维度: %w", err)
		}
	}

	if exists, err := s.columnExists(ctx, "feishu_event_dedup", "app_id"); err != nil {
		return err
	} else if !exists {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE feishu_event_dedup ADD COLUMN app_id VARCHAR(64) NOT NULL DEFAULT '' FIRST, DROP PRIMARY KEY, ADD PRIMARY KEY (app_id, event_id)`); err != nil {
			return fmt.Errorf("升级 feishu_event_dedup 应用维度: %w", err)
		}
	}
	return nil
}

// columnExists 检查当前数据库中指定列是否存在，供幂等升级逻辑使用。
func (s *MySQLStore) columnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	const query = `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`
	var count int
	if err := s.db.QueryRowContext(ctx, query, tableName, columnName).Scan(&count); err != nil {
		return false, fmt.Errorf("检查 %s.%s: %w", tableName, columnName, err)
	}
	return count > 0, nil
}

// FindTargetByFeishuUserID 返回已启用员工路由关联的已启用 Ekko 容器。
func (s *MySQLStore) FindTargetByFeishuUserID(ctx context.Context, feishuUserID string) (model.EkkoTarget, error) {
	const query = `SELECT c.container_id, r.username, c.profile, c.enabled
		FROM feishu_user_route r JOIN ekko_container c ON c.container_id = r.container_id
		WHERE r.feishu_user_id = ? AND r.enabled = 1 AND c.enabled = 1`
	var target model.EkkoTarget
	if err := s.db.QueryRowContext(ctx, query, feishuUserID).Scan(&target.ContainerID, &target.Username, &target.Profile, &target.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.EkkoTarget{}, ErrNotFound
		}
		return model.EkkoTarget{}, fmt.Errorf("查询员工路由: %w", err)
	}
	return target, nil
}

// FindSession 按员工和飞书聊天查找已创建的 Ekko 会话。
func (s *MySQLStore) FindSession(ctx context.Context, appID, feishuUserID, chatID string) (model.ChatSession, error) {
	const query = `SELECT app_id, feishu_user_id, chat_id, container_id, ekko_session_id, updated_at FROM feishu_ekko_session WHERE app_id = ? AND feishu_user_id = ? AND chat_id = ?`
	var session model.ChatSession
	if err := s.db.QueryRowContext(ctx, query, appID, feishuUserID, chatID).Scan(&session.AppID, &session.FeishuUserID, &session.ChatID, &session.ContainerID, &session.EkkoSessionID, &session.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ChatSession{}, ErrNotFound
		}
		return model.ChatSession{}, fmt.Errorf("查询飞书会话: %w", err)
	}
	return session, nil
}

// SaveSession 新建或更新飞书到 Ekko 的会话映射。
func (s *MySQLStore) SaveSession(ctx context.Context, session model.ChatSession) error {
	query := `INSERT INTO feishu_ekko_session (app_id, feishu_user_id, chat_id, container_id, ekko_session_id) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE container_id = VALUES(container_id), ekko_session_id = VALUES(ekko_session_id), updated_at = CURRENT_TIMESTAMP`
	if s.driver == "sqlite" {
		query = `INSERT INTO feishu_ekko_session (app_id, feishu_user_id, chat_id, container_id, ekko_session_id) VALUES (?, ?, ?, ?, ?) ON CONFLICT(app_id, feishu_user_id, chat_id) DO UPDATE SET container_id=excluded.container_id, ekko_session_id=excluded.ekko_session_id, updated_at=CURRENT_TIMESTAMP`
	}
	_, err := s.db.ExecContext(ctx, query, session.AppID, session.FeishuUserID, session.ChatID, session.ContainerID, session.EkkoSessionID)
	if err != nil {
		return fmt.Errorf("保存飞书会话: %w", err)
	}
	return nil
}

// MarkEventProcessed 原子记录飞书事件；返回 true 表示本次首次处理。
func (s *MySQLStore) MarkEventProcessed(ctx context.Context, appID, eventID string) (bool, error) {
	query := `INSERT IGNORE INTO feishu_event_dedup (app_id, event_id) VALUES (?, ?)`
	if s.driver == "sqlite" {
		query = `INSERT OR IGNORE INTO feishu_event_dedup (app_id, event_id) VALUES (?, ?)`
	}
	result, err := s.db.ExecContext(ctx, query, appID, eventID)
	if err != nil {
		return false, fmt.Errorf("记录飞书事件: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取事件去重结果: %w", err)
	}
	return rows == 1, nil
}
