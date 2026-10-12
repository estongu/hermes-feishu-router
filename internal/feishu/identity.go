package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractUserID 从飞书原始消息事件提取 sender_id.user_id。
// channel SDK 的 NormalizedMessage.UserID 优先放入 app scoped 的 open_id，
// 因此多机器人路由必须使用原始事件里的 user_id。
func ExtractUserID(rawEvent any) (string, error) {
	payload, err := json.Marshal(rawEvent)
	if err != nil {
		return "", fmt.Errorf("序列化飞书原始事件: %w", err)
	}
	var event struct {
		Event struct {
			Sender struct {
				SenderID struct {
					UserID string `json:"user_id"`
				} `json:"sender_id"`
			} `json:"sender"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return "", fmt.Errorf("解析飞书原始事件: %w", err)
	}
	userID := strings.TrimSpace(event.Event.Sender.SenderID.UserID)
	if userID == "" {
		return "", fmt.Errorf("飞书事件未携带 sender_id.user_id，请检查应用权限和事件订阅")
	}
	return userID, nil
}
