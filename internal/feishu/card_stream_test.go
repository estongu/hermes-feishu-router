package feishu

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeCardController struct{ cards []string }

func (c *fakeCardController) UpdateCard(_ context.Context, card string) error {
	c.cards = append(c.cards, card)
	return nil
}
func (c *fakeCardController) Close(context.Context) error { return nil }

// TestBuildResponseCard 验证卡片使用 Card 2.0 markdown 组件承载原始 Markdown。
func TestBuildResponseCard(t *testing.T) {
	card, err := BuildResponseCard("**加粗**\n- 列表")
	if err != nil || !strings.Contains(card, `"schema":"2.0"`) || !strings.Contains(card, `"tag":"markdown"`) {
		t.Fatalf("BuildResponseCard() = %q, %v", card, err)
	}
}

// TestCardStreamThrottles 验证节流窗口内只缓存，Flush 时才更新卡片。
func TestCardStreamThrottles(t *testing.T) {
	controller := &fakeCardController{}
	stream := NewCardStream(controller, "首段", time.Hour)
	if err := stream.Append(context.Background(), "后段"); err != nil {
		t.Fatal(err)
	}
	if len(controller.cards) != 0 {
		t.Fatalf("unexpected updates: %d", len(controller.cards))
	}
	if err := stream.Flush(context.Background()); err != nil || len(controller.cards) != 1 || !strings.Contains(controller.cards[0], "首段后段") {
		t.Fatalf("flush cards=%v err=%v", controller.cards, err)
	}
}
