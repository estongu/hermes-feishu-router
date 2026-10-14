package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/estongu/hermes-feishu-router/internal/model"
)

// TestSQLiteStore 验证单机默认存储的迁移、路由查询、会话保存和事件去重。
// 输入为临时 SQLite 文件；输出为可读取的路由/会话和幂等去重结果。
func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "router.db")+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ekko_container (container_id, profile) VALUES ('hermes-a', 'default')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO feishu_user_route (feishu_user_id, container_id, username) VALUES ('user-a', 'hermes-a', 'alice')`); err != nil {
		t.Fatal(err)
	}
	target, err := s.FindTargetByFeishuUserID(ctx, "user-a")
	if err != nil || target.ContainerID != "hermes-a" || target.Username != "alice" {
		t.Fatalf("target=%+v, err=%v", target, err)
	}
	if err := s.SaveSession(ctx, model.ChatSession{AppID: "cli_a", FeishuUserID: "user-a", ChatID: "chat-a", ContainerID: "hermes-a", EkkoSessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	session, err := s.FindSession(ctx, "cli_a", "user-a", "chat-a")
	if err != nil || session.EkkoSessionID != "session-a" {
		t.Fatalf("session=%+v, err=%v", session, err)
	}
	first, err := s.MarkEventProcessed(ctx, "cli_a", "event-a")
	if err != nil || !first {
		t.Fatalf("first=%t, err=%v", first, err)
	}
	second, err := s.MarkEventProcessed(ctx, "cli_a", "event-a")
	if err != nil || second {
		t.Fatalf("second=%t, err=%v", second, err)
	}
}
