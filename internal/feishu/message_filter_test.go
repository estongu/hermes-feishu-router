package feishu

import "testing"

// TestShouldRouteMessage 验证群聊只有明确 @ 当前机器人时才会进入 Hermes，
// 而用户在单聊窗口发给机器人的消息不要求 @。
func TestShouldRouteMessage(t *testing.T) {
	tests := []struct {
		name         string
		chatType     string
		mentionedBot bool
		want         bool
	}{
		{name: "单聊直接放行", chatType: "p2p", want: true},
		{name: "单聊类型大小写不敏感", chatType: " P2P ", want: true},
		{name: "群聊且@当前机器人", chatType: "group", mentionedBot: true, want: true},
		{name: "群聊未@当前机器人", chatType: "group", want: false},
		{name: "群聊@其他成员", chatType: "group", want: false},
		{name: "未知群聊类型未@机器人", chatType: "topic_group", want: false},
		{name: "未知群聊类型@当前机器人", chatType: "topic_group", mentionedBot: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRouteMessage(tt.chatType, tt.mentionedBot)
			if got != tt.want {
				t.Fatalf("ShouldRouteMessage(%q, %t) = %t, want %t", tt.chatType, tt.mentionedBot, got, tt.want)
			}
		})
	}
}
