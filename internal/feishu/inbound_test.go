package feishu

import "testing"

// TestParseInboundContentPost 验证 post 的文字、图片 key 与图片尺寸都会从原始事件恢复。
func TestParseInboundContentPost(t *testing.T) {
	raw := map[string]any{"event": map[string]any{"message": map[string]any{"content": `{"content_v2":[[{"tag":"img","image_key":"img_key","width":1172,"height":772}],[{"tag":"text","text":"识别图中内容"}]]}`}}}
	got := ParseInboundContent("[rich text message]", raw)
	if got.Text != "识别图中内容" || len(got.Images) != 1 {
		t.Fatalf("ParseInboundContent() = %#v", got)
	}
	if image := got.Images[0]; image.ImageKey != "img_key" || image.Width != 1172 || image.Height != 772 {
		t.Fatalf("image = %#v", image)
	}
}

// TestParseInboundContentText 验证普通文本事件不会因原始事件缺失而被改写。
func TestParseInboundContentText(t *testing.T) {
	got := ParseInboundContent("你好", nil)
	if got.Text != "你好" || len(got.Images) != 0 {
		t.Fatalf("ParseInboundContent() = %#v", got)
	}
}

// TestParseInboundContentImage 验证独立图片消息也会提取 image_key。
func TestParseInboundContentImage(t *testing.T) {
	raw := map[string]any{"event": map[string]any{"message": map[string]any{"content": `{"image_key":"img_only"}`}}}
	got := ParseInboundContent("", raw)
	if got.Text != "请分析这张图片。" || len(got.Images) != 1 || got.Images[0].ImageKey != "img_only" {
		t.Fatalf("ParseInboundContent() = %#v", got)
	}
}
