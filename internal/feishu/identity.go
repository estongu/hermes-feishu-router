package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SenderIdentity 描述入站发送者用于路由的身份。外部用户的 user_id 为空，
// 只能在当前应用范围内使用 open_id 路由。
type SenderIdentity struct {
	Value      string
	IsExternal bool
}

// ExtractSenderIdentity 从飞书原始事件提取内部 user_id 或外部 open_id。
func ExtractSenderIdentity(rawEvent any) (SenderIdentity, error) {
	payload, err := json.Marshal(rawEvent)
	if err != nil {
		return SenderIdentity{}, fmt.Errorf("序列化飞书原始事件: %w", err)
	}
	var event struct {
		Event struct {
			Sender struct {
				SenderID struct {
					UserID string `json:"user_id"`
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return SenderIdentity{}, fmt.Errorf("解析飞书原始事件: %w", err)
	}
	userID := strings.TrimSpace(event.Event.Sender.SenderID.UserID)
	if userID != "" {
		return SenderIdentity{Value: userID}, nil
	}
	openID := strings.TrimSpace(event.Event.Sender.SenderID.OpenID)
	if openID == "" {
		return SenderIdentity{}, fmt.Errorf("飞书事件未携带 sender_id.user_id 或 open_id")
	}
	return SenderIdentity{Value: openID, IsExternal: true}, nil
}
