package ekko

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	socketclient "github.com/malcolmston/socketio/client"

	"github.com/estongu/hermes-feishu-router/internal/model"
)

// ChatClient 通过 Socket.IO 调用 Studio 的 run 事件，并等待最终的 run.completed 或 run.failed 事件。
type ChatClient struct {
	maxWait time.Duration
}

// NewChatClient 创建聊天客户端。单次执行最多等待五分钟，调用方 context 可更早取消。
func NewChatClient() *ChatClient { return &ChatClient{maxWait: 5 * time.Minute} }

// Run 向目标 Studio 发送输入并返回最终输出。onChunk 接收可用的增量文本；允许为 nil。
// target.BaseURL 和 token 必须由动态访问解析器在本次请求中提供；sessionID 为 Hub 保存的 Ekko 会话标识。
func (c *ChatClient) Run(ctx context.Context, target model.EkkoTarget, token, sessionID, text string, images []model.ImageAttachment, onChunk func(string)) (string, error) {
	if strings.TrimSpace(target.BaseURL) == "" || strings.TrimSpace(token) == "" || strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("Studio 聊天参数不完整")
	}
	input, err := c.prepareInput(ctx, target, token, text, images)
	if err != nil {
		return "", err
	}
	// Socket.IO 将 Dial URL 的路径解释为 namespace；Ekko 对话服务固定监听 /chat-run。
	socket, err := socketclient.Dial(chatRunURL(target.BaseURL), socketclient.Options{
		Auth:        map[string]any{"token": token},
		DialTimeout: 15 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("连接 Studio Socket.IO: %w", err)
	}
	defer socket.Close()

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	var streamed string
	emitChunk := func(args []any) {
		chunk := eventChunk(args)
		if chunk == "" || onChunk == nil {
			return
		}
		// 部分 Studio 版本推送累积全文，部分推送 delta；统一转换为新增部分。
		if strings.HasPrefix(chunk, streamed) {
			chunk = strings.TrimPrefix(chunk, streamed)
			streamed += chunk
		} else {
			streamed += chunk
		}
		if chunk != "" {
			onChunk(chunk)
		}
	}
	finish := func(value result) {
		select {
		case done <- value:
		default:
		}
	}
	socket.On("run.completed", func(args []any) []any {
		output := eventString(args, "output")
		if output == "" {
			output = eventString(args, "parsed_content")
		}
		if output == "" {
			finish(result{err: fmt.Errorf("Studio 未返回对话内容")})
			return nil
		}
		finish(result{text: output})
		return nil
	})
	socket.On("run.failed", func(args []any) []any {
		message := eventString(args, "error")
		if message == "" {
			message = "Studio 对话执行失败"
		}
		finish(result{err: fmt.Errorf("%s", message)})
		return nil
	})
	for _, eventName := range []string{
		"run.delta", "run.chunk", "run.output", "run.progress", "run.message",
		"run.stream", "run.token", "run.update", "run.data", "run.step",
		"agent.delta", "agent.message", "message.delta", "message.chunk",
	} {
		socket.On(eventName, func(args []any) []any { emitChunk(args); return nil })
	}

	request := map[string]any{
		"input":      input,
		"session_id": sessionID,
		"profile":    target.Profile,
	}
	if err := socket.Emit("run", request); err != nil {
		return "", fmt.Errorf("发送 Studio run 事件: %w", err)
	}

	timeout := time.NewTimer(c.maxWait)
	defer timeout.Stop()
	select {
	case value := <-done:
		return value.text, value.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timeout.C:
		return "", fmt.Errorf("等待 Studio 对话结果超时")
	}
}

type contentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type uploadedFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// prepareInput 将含图片的飞书消息转换为 Ekko Studio 已支持的 ContentBlock 输入。
// 无图片时保留字符串输入，以兼容旧版本 Studio 和既有会话。
func (c *ChatClient) prepareInput(ctx context.Context, target model.EkkoTarget, token, text string, images []model.ImageAttachment) (any, error) {
	if len(images) == 0 {
		return text, nil
	}
	uploaded, err := uploadImages(ctx, target, token, images)
	if err != nil {
		return nil, fmt.Errorf("上传图片到 Studio: %w", err)
	}
	blocks := make([]contentBlock, 0, len(uploaded)+1)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: text})
	}
	for index, file := range uploaded {
		blocks = append(blocks, contentBlock{Type: "image", Name: file.Name, Path: file.Path, MediaType: images[index].MediaType})
	}
	return blocks, nil
}

// uploadImages 将已下载的飞书图片上传到目标 Studio 的 profile 级上传目录。
// 输入 images 的 Data 是图片原始字节；返回值是 Studio 返回、可用于 run.input 的安全路径。
func uploadImages(ctx context.Context, target model.EkkoTarget, token string, images []model.ImageAttachment) ([]uploadedFile, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, image := range images {
		part, err := writer.CreateFormFile("file", image.Name)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(image.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(target.BaseURL, "/")+"/api/studio/uploads", &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if profile := strings.TrimSpace(target.Profile); profile != "" {
		request.Header.Set("X-Hermes-Profile", profile)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Studio 上传接口返回 HTTP %d", response.StatusCode)
	}
	var result struct {
		Files []uploadedFile `json:"files"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("解析 Studio 上传结果: %w", err)
	}
	if len(result.Files) != len(images) {
		return nil, fmt.Errorf("Studio 上传结果数量异常: got %d, want %d", len(result.Files), len(images))
	}
	for _, file := range result.Files {
		if strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.Path) == "" {
			return nil, fmt.Errorf("Studio 上传结果缺少文件名或路径")
		}
	}
	return result.Files, nil
}

// eventChunk 兼容当前已知 Studio 版本中常见的增量字段。
func eventChunk(args []any) string {
	for _, key := range []string{"delta", "content", "text", "output", "parsed_content"} {
		if value := eventString(args, key); value != "" {
			return value
		}
	}
	return ""
}

// chatRunURL 构造 Socket.IO 客户端选择 /chat-run namespace 所需的 Dial 地址。
func chatRunURL(baseURL string) string { return strings.TrimRight(baseURL, "/") + "/chat-run" }

// eventString 从 Socket.IO 事件第一个 JSON 对象参数中提取字符串字段。
// 采用 JSON 往返以兼容不同客户端将对象解码为 map 或结构体的情况。
func eventString(args []any, key string) string {
	if len(args) == 0 || args[0] == nil {
		return ""
	}
	raw, err := json.Marshal(args[0])
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
