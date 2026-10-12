package ekko

import "testing"

// TestEventString 验证 Socket.IO JSON 对象参数中存在、缺失和空值三种字段读取情况。
func TestEventString(t *testing.T) {
	tests := []struct {
		name string
		args []any
		key  string
		want string
	}{
		{name: "输出字段", args: []any{map[string]any{"output": " Ekko 回复 "}}, key: "output", want: "Ekko 回复"},
		{name: "缺失字段", args: []any{map[string]any{"error": "失败"}}, key: "output", want: ""},
		{name: "空参数", args: nil, key: "output", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := eventString(test.args, test.key); got != test.want {
				t.Fatalf("eventString() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestChatRunURL 验证无论容器根地址是否带结尾斜杠，都会连接到 Ekko 的 /chat-run namespace。
func TestChatRunURL(t *testing.T) {
	if got := chatRunURL("http://studio:16020/"); got != "http://studio:16020/chat-run" {
		t.Fatalf("chatRunURL() = %q", got)
	}
}

// TestEventChunk 验证不同 Studio 增量字段都可转换为文本片段。
func TestEventChunk(t *testing.T) {
	if got := eventChunk([]any{map[string]any{"delta": "你好"}}); got != "你好" {
		t.Fatalf("eventChunk() = %q", got)
	}
	if got := eventChunk([]any{map[string]any{"content": "世界"}}); got != "世界" {
		t.Fatalf("eventChunk() = %q", got)
	}
}
