package feishu

import "testing"

// TestExtractUserID 验证多飞书应用路由从原始事件读取跨应用稳定的 user_id。
func TestExtractSenderIdentityUsesInternalUserID(t *testing.T) {
	rawEvent := map[string]any{
		"event": map[string]any{
			"sender": map[string]any{
				"sender_id": map[string]any{"open_id": "ou_app_scoped", "user_id": "u_global"},
			},
		},
	}
	identity, err := ExtractSenderIdentity(rawEvent)
	if err != nil || identity.Value != "u_global" || identity.IsExternal {
		t.Fatalf("ExtractSenderIdentity() = %#v, %v", identity, err)
	}
}

// TestExtractSenderIdentityUsesExternalOpenID 验证外部用户缺少 user_id 时使用应用范围内的 open_id。
func TestExtractSenderIdentityUsesExternalOpenID(t *testing.T) {
	identity, err := ExtractSenderIdentity(map[string]any{"event": map[string]any{"sender": map[string]any{"sender_id": map[string]any{"open_id": "ou_app_scoped"}}}})
	if err != nil || identity.Value != "ou_app_scoped" || !identity.IsExternal {
		t.Fatalf("ExtractSenderIdentity() = %#v, %v", identity, err)
	}
}
