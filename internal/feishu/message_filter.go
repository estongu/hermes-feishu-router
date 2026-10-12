package feishu

import "strings"

// ShouldRouteMessage 判断入站消息是否应转发到 Hermes。
// 传入飞书事件的 chatType 与 SDK 已识别的 mentionedBot；单聊消息直接放行，
// 群聊（包括未知的群聊类型）必须明确 @ 当前机器人，防止授予群消息权限后误答普通聊天。
func ShouldRouteMessage(chatType string, mentionedBot bool) bool {
	return strings.EqualFold(strings.TrimSpace(chatType), "p2p") || mentionedBot
}
