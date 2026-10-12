// Package model 定义 Hub 内部的路由、目标容器与会话数据模型。
package model

import "time"

// EkkoTarget 是员工路由关联的 Ekko Studio 容器配置。
type EkkoTarget struct {
	ContainerID string
	// Username 按业务约定保存目标实例在 /api/hermes/users 返回中的 remark_name。
	// 字段名沿用用户路由表约定，并非 Ekko 返回的 username 字段。
	Username string
	// BaseURL 由每次消息动态解析，不写入 ekko_container 表。
	BaseURL string
	Profile string
	Enabled bool
}

// StudioAccess 是一次消息转发临时取得的目标容器地址与访问令牌。
// Token 只能在当前请求内存中使用，不能写入数据库或日志。
type StudioAccess struct {
	BaseURL string
	Token   string
}

// ChatSession 把一个飞书会话映射至某个 Ekko Studio 会话。
// ChatID 由飞书入站消息自动提供，无需在部署前人工维护。
type ChatSession struct {
	AppID         string
	FeishuUserID  string
	ChatID        string
	ContainerID   string
	EkkoSessionID string
	UpdatedAt     time.Time
}
