package feishu

import (
	"encoding/json"
	"strings"
)

// RichText 是飞书 post 富文本中可转发的文字和图片引用。
// ImageKey 仍需由调用方使用飞书媒体接口下载，不能直接暴露给目标模型。
type RichText struct {
	Text   string
	Images []RichTextImage
}

// RichTextImage 保存 post img 节点的下载标识和显示尺寸。
type RichTextImage struct {
	ImageKey string
	Width    int
	Height   int
}

// ParseInboundContent 将标准化内容与原始事件合并为可路由内容。
// 输入 normalContent 是 SDK 已解析的文本；rawEvent 是其保留的飞书原始事件。
// post 消息会从 event.message.content 还原文字和图片 key，其他类型保持原文本。
func ParseInboundContent(normalContent string, rawEvent any) RichText {
	result := RichText{Text: strings.TrimSpace(normalContent)}
	content := rawMessageContent(rawEvent)
	if content == "" {
		return result
	}
	var post struct {
		Content   [][]postNode `json:"content"`
		ContentV2 [][]postNode `json:"content_v2"`
	}
	if err := json.Unmarshal([]byte(content), &post); err != nil {
		return result
	}
	lines := post.ContentV2
	if len(lines) == 0 {
		lines = post.Content
	}
	if len(lines) == 0 {
		// 独立图片消息（message_type=image）不是 post，但 content 同样含 image_key。
		var image struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(content), &image); err == nil && image.ImageKey != "" {
			result.Images = append(result.Images, RichTextImage{ImageKey: image.ImageKey})
			if result.Text == "" {
				result.Text = "请分析这张图片。"
			}
		}
		return result
	}
	var textLines []string
	for _, line := range lines {
		var text strings.Builder
		for _, node := range line {
			switch node.Tag {
			case "text", "a":
				text.WriteString(node.Text)
			case "at":
				if node.UserName != "" {
					text.WriteString("@" + node.UserName)
				}
			case "img":
				if node.ImageKey != "" {
					result.Images = append(result.Images, RichTextImage{ImageKey: node.ImageKey, Width: node.Width, Height: node.Height})
				}
			}
		}
		if lineText := strings.TrimSpace(text.String()); lineText != "" {
			textLines = append(textLines, lineText)
		}
	}
	if len(textLines) > 0 {
		result.Text = strings.Join(textLines, "\n")
	} else if len(result.Images) > 0 {
		// 让仅含图片的消息仍有可读提示；图片本身会走附件通道。
		result.Text = "请分析这张图片。"
	}
	return result
}

// RemoveMentionPlaceholders 移除转发给 Hermes 时无业务含义的飞书提及占位符。
// text 是已经解析的消息正文；placeholders 仅应传入当前机器人的 mention key 或展示名称，
// 以保留用户在问题中 @ 其他成员的语义。
func RemoveMentionPlaceholders(text string, placeholders []string) string {
	for _, placeholder := range placeholders {
		if placeholder != "" {
			text = strings.ReplaceAll(text, placeholder, "")
		}
	}
	return strings.TrimSpace(text)
}

type postNode struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	UserName string `json:"user_name"`
	ImageKey string `json:"image_key"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// rawMessageContent 从 SDK 的任意原始事件表示中定位 event.message.content。
func rawMessageContent(rawEvent any) string {
	payload, err := json.Marshal(rawEvent)
	if err != nil {
		return ""
	}
	var event struct {
		Event struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return ""
	}
	return event.Event.Message.Content
}
