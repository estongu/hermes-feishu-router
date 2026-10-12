package feishu

import "testing"

// TestExtractUserID 验证多飞书应用路由从原始事件读取跨应用稳定的 user_id。
func TestExtractUserID(t *testing.T) {
	rawEvent := map[string]any{
		"event": map[string]any{
			"sender": map[string]any{
				"sender_id": map[string]any{"open_id": "ou_app_scoped", "user_id": "u_global"},
			},
		},
	}
	userID, err := ExtractUserID(rawEvent)
	if err != nil || userID != "u_global" {
		t.Fatalf("ExtractUserID() = %q, %v", userID, err)
	}
}

// TestExtractUserIDMissing 验证未获 user_id 时不给错误地回退到 open_id。
func TestExtractUserIDMissing(t *testing.T) {
	_, err := ExtractUserID(map[string]any{"event": map[string]any{"sender": map[string]any{"sender_id": map[string]any{"open_id": "ou_app_scoped"}}}})
	if err == nil {
		t.Fatal("ExtractUserID() should reject an event without user_id")
	}
}
