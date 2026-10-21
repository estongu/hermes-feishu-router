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

// TestCardStreamReplace 验证最终答案可覆盖已累计的流式内容，防止上游重复片段遗留在卡片中。
// 入参为重复的中间正文与最终答案，出参为仅含最终答案的卡片 JSON。
func TestCardStreamReplace(t *testing.T) {
	controller := &fakeCardController{}
	stream := NewCardStream(controller, "重复片段重复片段", time.Hour)
	if err := stream.Replace(context.Background(), "最终答案"); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if len(controller.cards) != 1 || !strings.Contains(controller.cards[0], "最终答案") || strings.Contains(controller.cards[0], "重复片段") {
		t.Fatalf("cards = %v", controller.cards)
	}
}
