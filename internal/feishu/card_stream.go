package feishu

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// CardController 是飞书 SDK 卡片流控制器的最小接口，便于单元测试。
type CardController interface {
	UpdateCard(ctx context.Context, card string) error
	Close(ctx context.Context) error
}

// CardStream 用飞书 Card 2.0 markdown 组件渲染 Hermes 输出，并合并高频增量更新。
type CardStream struct {
	controller CardController
	throttle   time.Duration
	content    string
	lastUpdate time.Time
	mu         sync.Mutex
}

// NewCardStream 创建已发送首段内容的卡片流。throttle 控制后续卡片更新频率。
func NewCardStream(controller CardController, firstChunk string, throttle time.Duration) *CardStream {
	return &CardStream{controller: controller, content: firstChunk, throttle: throttle, lastUpdate: time.Now()}
}

// Append 追加增量；在节流窗口内仅缓存，下一次更新或 Flush 时一起写入卡片。
func (s *CardStream) Append(ctx context.Context, text string) error {
	s.mu.Lock()
	s.content += text
	shouldUpdate := time.Since(s.lastUpdate) >= s.throttle
	s.mu.Unlock()
	if !shouldUpdate {
		return nil
	}
	return s.update(ctx)
}

// Replace 以 text 替换当前卡片的全部正文，并立即提交更新。
// 入参 text 是 Studio run.completed 返回的完整最终答案。
func (s *CardStream) Replace(ctx context.Context, text string) error {
	s.mu.Lock()
	s.content = text
	s.mu.Unlock()
	return s.update(ctx)
}

// Flush 强制将当前所有内容更新到卡片。
func (s *CardStream) Flush(ctx context.Context) error { return s.update(ctx) }

// Close 结束卡片流。
func (s *CardStream) Close(ctx context.Context) error { return s.controller.Close(ctx) }

func (s *CardStream) update(ctx context.Context) error {
	s.mu.Lock()
	content := s.content
	s.mu.Unlock()
	card, err := BuildResponseCard(content)
	if err != nil {
		return err
	}
	if err := s.controller.UpdateCard(ctx, card); err != nil {
		return err
	}
	s.mu.Lock()
	s.lastUpdate = time.Now()
	s.mu.Unlock()
	return nil
}

// BuildResponseCard 构造 Card 2.0 的 Markdown 正文卡片，使 Markdown 在飞书客户端原生渲染。
func BuildResponseCard(content string) (string, error) {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true, "width_mode": "default"},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "Hermes Assistant"},
			"template": "blue",
			"icon":     map[string]any{"tag": "standard_icon", "token": "ai-common_colorful"},
		},
		"body": map[string]any{
			"direction": "vertical", "padding": "12px 12px 20px 12px",
			"elements": []any{map[string]any{"tag": "markdown", "element_id": "answer", "content": content, "text_size": "normal"}},
		},
	}
	bytes, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
