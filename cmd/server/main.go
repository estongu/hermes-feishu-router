// server 启动飞书长连接，并将员工消息路由到其专属 Ekko Studio 容器。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	channel "github.com/larksuite/channel-sdk-go"
	channeltypes "github.com/larksuite/channel-sdk-go/types"

	"github.com/estongu/hermes-feishu-router/internal/config"
	"github.com/estongu/hermes-feishu-router/internal/ekko"
	feishuadapter "github.com/estongu/hermes-feishu-router/internal/feishu"
	"github.com/estongu/hermes-feishu-router/internal/hub"
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
	database, err := store.Open(ctx, cfg.MySQL.DSN())
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
		appID := app.AppID
		appName := app.Name
		client.OnMessage(func(messageCtx context.Context, message *channel.NormalizedMessage) error {
			if debugLogEvents {
				logInboundMessage(appID, message)
			}
			// 即使应用配置了 im:message.group_msg，群聊也只能在明确 @ 当前机器人时转发。
			// MentionedBot 由 channel SDK 根据当前应用机器人的身份和 mentions 自动标注。
			if !feishuadapter.ShouldRouteMessage(message.ChatType, message.MentionedBot) {
				return nil
			}
			userID, err := feishuadapter.ExtractUserID(message.RawEvent)
			if err != nil {
				return fmt.Errorf("飞书应用 %s: %w", appName, err)
			}
			eventID := message.EventID
			if eventID == "" {
				eventID = message.MessageID
			}
			return handler.Handle(messageCtx, feishuadapter.Message{
				EventID: eventID,
				AppID:   appID,
				UserID:  userID,
				ChatID:  message.ChatID,
				Text:    message.Content,
			})
		})
		client.OnError(func(err error) { log.Printf("飞书长连接错误 app=%s: %v", appName, err) })
		clients = append(clients, client)
	}
	return clients, nil
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
