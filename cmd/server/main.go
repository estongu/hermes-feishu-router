// server 启动飞书长连接，并将员工消息路由到其专属 Ekko Studio 容器。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	channeltypes "github.com/larksuite/channel-sdk-go/types"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/estongu/hermes-feishu-router/internal/config"
	"github.com/estongu/hermes-feishu-router/internal/ekko"
	feishuadapter "github.com/estongu/hermes-feishu-router/internal/feishu"
	"github.com/estongu/hermes-feishu-router/internal/hub"
	"github.com/estongu/hermes-feishu-router/internal/model"
	"github.com/estongu/hermes-feishu-router/internal/platform"
	"github.com/estongu/hermes-feishu-router/internal/store"
)

const feishuStreamThrottle = 15 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.LoadFile("config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	driver, dsn, err := cfg.Storage.DriverAndDSN(cfg.MySQL)
	if err != nil {
		log.Fatal(err)
	}
	database, err := store.Open(ctx, driver, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
	resolver, err := platform.NewAccessResolver(cfg.Platform.BaseURL, platform.Credentials{Username: cfg.Platform.Username, Password: cfg.Platform.Password})
	if err != nil {
		log.Fatal(err)
	}
	service := hub.NewService(database, resolver, ekko.NewChatClient())
	clients, err := newFeishuClients(cfg.Feishu.Apps, cfg.Feishu.IsStreamEnabled(), cfg.Feishu.DebugLogEvents, service)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Hermes Feishu Router 已启动，共连接 %d 个飞书应用", len(clients))

	// channel-sdk-go 的 Start 会无限阻塞，且其内部不会监听 ctx.Done()。
	// 因此独立启动它并在收到 Ctrl+C/SIGTERM 后主动 Stop，主程序才能正常退出。
	startError := make(chan error, len(clients))
	for _, client := range clients {
		go func(client channel.Channel) { startError <- client.Start(ctx) }(client)
	}
	select {
	case <-ctx.Done():
		log.Println("收到退出信号，正在关闭飞书长连接")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, client := range clients {
			if err := client.Stop(shutdownCtx); err != nil {
				log.Printf("关闭飞书长连接失败: %v", err)
			}
		}
	case err := <-startError:
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		log.Fatal(err)
	}
}

// newFeishuClients 为配置的每一个飞书应用创建独立长连接。
// 每条连接将所属 app_id 写入消息，避免同一用户在不同机器人中的会话相互混用。
func newFeishuClients(apps []config.FeishuAppConfig, streamEnabled, debugLogEvents bool, service *hub.Service) ([]channel.Channel, error) {
	clients := make([]channel.Channel, 0, len(apps))
	for _, app := range apps {
		outbound := channeltypes.DefaultChannelConfig().Outbound
		// 飞书对同一消息存在编辑次数上限；15 秒节流能保留实时感并显著减少编辑次数。
		outbound.StreamThrottleMs = feishuStreamThrottle
		client, err := channel.New(app.AppID, app.AppSecret, channel.WithOutboundConfig(outbound))
		if err != nil {
			return nil, fmt.Errorf("创建飞书应用 %s 的长连接: %w", app.Name, err)
		}
		handler := feishuadapter.NewHandler(service, channelSender{client: client}, streamEnabled)
		resourceDownloader := newMessageImageDownloader(app.AppID, app.AppSecret)
		appID := app.AppID
		appName := app.Name
		client.OnMessage(func(messageCtx context.Context, message *channel.NormalizedMessage) error {
			if debugLogEvents {
				logInboundMessage(appID, message)
			}
			// 即使应用配置了 im:message.group_msg，群聊也只能在明确 @ 当前机器人、且未 @所有人时转发。
			// MentionedBot 与 MentionAll 由 channel SDK 根据飞书事件自动标注。
			if !feishuadapter.ShouldRouteMessage(message.ChatType, message.MentionedBot, message.MentionAll) {
				return nil
			}
			identity, err := feishuadapter.ExtractSenderIdentity(message.RawEvent)
			if err != nil {
				return fmt.Errorf("飞书应用 %s: %w", appName, err)
			}
			eventID := message.EventID
			if eventID == "" {
				eventID = message.MessageID
			}
			content := feishuadapter.ParseInboundContent(message.Content, message.RawEvent)
			// 飞书将 @ 当前机器人的文本编码为 @_user_N 之类的占位符。
			// 该标记只用于事件路由，转发给 Hermes 前必须移除，避免干扰真实提问。
			content.Text = feishuadapter.RemoveMentionPlaceholders(content.Text, currentBotMentionPlaceholders(message.Mentions))
			images, err := downloadPostImages(messageCtx, resourceDownloader, message.MessageID, content.Images)
			if err != nil {
				return fmt.Errorf("飞书应用 %s: 下载富文本图片: %w", appName, err)
			}
			return handler.Handle(messageCtx, feishuadapter.Message{
				EventID:    eventID,
				AppID:      appID,
				UserID:     identity.Value,
				IsExternal: identity.IsExternal,
				ChatID:     message.ChatID,
				Text:       content.Text,
				Images:     images,
			})
		})
		client.OnError(func(err error) { log.Printf("飞书长连接错误 app=%s: %v", appName, err) })
		clients = append(clients, client)
	}
	return clients, nil
}

// currentBotMentionPlaceholders 从 SDK 已标记的当前机器人提及中提取可清除的文本形式。
// mentions 中其他人员或其他机器人的提及不会被加入，避免改变用户问题的原意。
func currentBotMentionPlaceholders(mentions []channel.Mention) []string {
	placeholders := make([]string, 0, len(mentions)*2)
	for _, mention := range mentions {
		if !mention.IsBot {
			continue
		}
		if mention.Key != "" {
			placeholders = append(placeholders, mention.Key)
		}
		if mention.Name != "" {
			placeholders = append(placeholders, "@"+mention.Name)
		}
	}
	return placeholders
}

// messageImageDownloader 是“消息资源下载”接口的最小能力。
// 用户发送的图片必须带 message_id 下载，不能调用仅适用于应用自上传图片的 images 接口。
type messageImageDownloader interface {
	DownloadMessageImage(ctx context.Context, messageID, imageKey string) ([]byte, error)
}

type feishuMessageImageDownloader struct{ client *lark.Client }

// newMessageImageDownloader 为一个飞书应用创建按消息下载资源的客户端。
func newMessageImageDownloader(appID, appSecret string) *feishuMessageImageDownloader {
	return &feishuMessageImageDownloader{client: lark.NewClient(appID, appSecret)}
}

// DownloadMessageImage 通过飞书“获取消息中的资源文件”接口下载用户消息内嵌图片。
func (d *feishuMessageImageDownloader) DownloadMessageImage(ctx context.Context, messageID, imageKey string) ([]byte, error) {
	response, err := d.client.Im.V1.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build())
	if err != nil {
		return nil, err
	}
	if !response.Success() {
		return nil, fmt.Errorf("下载消息资源 API 错误: %d - %s", response.Code, response.Msg)
	}
	data, err := io.ReadAll(response.File)
	if err != nil {
		return nil, fmt.Errorf("读取消息图片流: %w", err)
	}
	return data, nil
}

// downloadPostImages 下载 post 图片并补全目标 Studio 内容块所需的名称和媒体类型。
// messageID 是图片所属飞书消息 ID；images 来自 ParseInboundContent，返回的 Data 仅存在于当前请求内存中。
func downloadPostImages(ctx context.Context, downloader messageImageDownloader, messageID string, images []feishuadapter.RichTextImage) ([]model.ImageAttachment, error) {
	attachments := make([]model.ImageAttachment, 0, len(images))
	for index, image := range images {
		data, err := downloader.DownloadMessageImage(ctx, messageID, image.ImageKey)
		if err != nil {
			return nil, fmt.Errorf("下载 image_key=%s: %w", image.ImageKey, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("下载 image_key=%s: 图片为空", image.ImageKey)
		}
		mediaType := http.DetectContentType(data)
		extensions, _ := mime.ExtensionsByType(mediaType)
		extension := ".bin"
		if len(extensions) > 0 {
			extension = extensions[0]
		}
		name := fmt.Sprintf("feishu-image-%d%s", index+1, extension)
		attachments = append(attachments, model.ImageAttachment{Name: filepath.Base(name), MediaType: mediaType, Data: data})
	}
	return attachments, nil
}

// logInboundMessage 输出标准化消息和飞书原始事件，供路由字段排查使用。
// 原始事件可能包含消息正文，仅应在受控的本地日志中短期保留。
func logInboundMessage(appID string, message *channel.NormalizedMessage) {
	if payload, err := json.Marshal(message); err == nil {
		log.Printf("飞书标准化入站消息 app_id=%s: %s", appID, payload)
	} else {
		log.Printf("序列化飞书标准化消息失败: %v", err)
	}
	if payload, err := json.Marshal(message.RawEvent); err == nil {
		log.Printf("飞书原始入站事件 app_id=%s: %s", appID, payload)
	} else {
		log.Printf("序列化飞书原始事件失败: %v", err)
	}
}

// channelSender 将适配层的文本回复转换为 channel-sdk-go 的发送请求。
type channelSender struct{ client channel.Channel }

func (s channelSender) SendText(ctx context.Context, chatID, text string) error {
	_, err := s.client.Send(ctx, &channel.SendInput{ReceiveID: chatID, MsgType: "text", Text: text})
	if err != nil {
		return fmt.Errorf("发送飞书回复: %w", err)
	}
	return nil
}

// SendCard 将非流式最终答案发送为 Card 2.0 Markdown 卡片。
func (s channelSender) SendCard(ctx context.Context, chatID, markdown string) error {
	card, err := feishuadapter.BuildResponseCard(markdown)
	if err != nil {
		return fmt.Errorf("构造飞书回复卡片: %w", err)
	}
	if _, err := s.client.Send(ctx, &channel.SendInput{ReceiveID: chatID, MsgType: "interactive", Card: card}); err != nil {
		return fmt.Errorf("发送飞书回复卡片: %w", err)
	}
	return nil
}

// StartStream 创建 Card 2.0 流式消息，卡片 Markdown 由飞书客户端原生渲染。
func (s channelSender) StartStream(ctx context.Context, chatID, firstChunk string) (feishuadapter.Stream, error) {
	card, err := feishuadapter.BuildResponseCard(firstChunk)
	if err != nil {
		return nil, fmt.Errorf("构造飞书回复卡片: %w", err)
	}
	stream, err := s.client.Stream(ctx, &channel.SendInput{ReceiveID: chatID, Card: card})
	if err != nil {
		return nil, fmt.Errorf("创建飞书流式回复: %w", err)
	}
	return feishuadapter.NewCardStream(stream, firstChunk, feishuStreamThrottle), nil
}
