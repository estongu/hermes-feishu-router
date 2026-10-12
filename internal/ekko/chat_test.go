package ekko

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/estongu/hermes-feishu-router/internal/model"
)

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

// TestUploadImages 验证图片会携带 Studio 鉴权和 profile，并转换为返回的上传路径。
func TestUploadImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/studio/uploads" || request.Header.Get("Authorization") != "Bearer studio-token" || request.Header.Get("X-Hermes-Profile") != "default" {
			t.Fatalf("request = %s, auth=%q, profile=%q", request.URL.Path, request.Header.Get("Authorization"), request.Header.Get("X-Hermes-Profile"))
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm() error = %v", err)
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile() error = %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "feishu-image-1.png" || string(data) != "image-data" {
			t.Fatalf("file name=%q, body=%q", header.Filename, data)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"files":[{"name":"feishu-image-1.png","path":"/tmp/profile/upload.png"}]}`))
	}))
	defer server.Close()

	files, err := uploadImages(context.Background(), model.EkkoTarget{BaseURL: server.URL, Profile: "default"}, "studio-token", []model.ImageAttachment{{Name: "feishu-image-1.png", MediaType: "image/png", Data: []byte("image-data")}})
	if err != nil {
		t.Fatalf("uploadImages() error = %v", err)
	}
	if len(files) != 1 || files[0].Path != "/tmp/profile/upload.png" {
		t.Fatalf("uploadImages() = %#v", files)
	}
}

// TestPrepareInput 验证上传后的图片以目标协议要求的 image 内容块加入 run.input。
func TestPrepareInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"files":[{"name":"image.png","path":"/tmp/image.png"}]}`))
	}))
	defer server.Close()
	input, err := NewChatClient().prepareInput(context.Background(), model.EkkoTarget{BaseURL: server.URL}, "token", "请识别", []model.ImageAttachment{{Name: "image.png", MediaType: "image/png", Data: []byte("data")}})
	if err != nil {
		t.Fatalf("prepareInput() error = %v", err)
	}
	blocks, ok := input.([]contentBlock)
	if !ok || len(blocks) != 2 || blocks[0].Text != "请识别" || blocks[1].Type != "image" || blocks[1].Path != "/tmp/image.png" || blocks[1].MediaType != "image/png" {
		t.Fatalf("input = %#v", input)
	}
	if plain, err := NewChatClient().prepareInput(context.Background(), model.EkkoTarget{}, "", "纯文本", nil); err != nil || plain != "纯文本" {
		t.Fatalf("plain input = %#v, error = %v", plain, err)
	}
	if strings.TrimSpace(blocks[0].Text) == "" {
		t.Fatal("text block is unexpectedly empty")
	}
}
