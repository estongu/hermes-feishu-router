package feishu

import "strings"

// ShouldRouteMessage 判断入站消息是否应转发到 Hermes。
// chatType 是飞书会话类型，mentionedBot 表示是否明确 @ 当前机器人，mentionAll 表示是否 @所有人。
// 单聊消息直接放行；群聊（包括未知群聊类型）仅在明确 @ 当前机器人且未 @所有人时放行，
// 避免授予群消息权限后误答普通聊天或全员通知。
func ShouldRouteMessage(chatType string, mentionedBot, mentionAll bool) bool {
	return strings.EqualFold(strings.TrimSpace(chatType), "p2p") || (mentionedBot && !mentionAll)
}
