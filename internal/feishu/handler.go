// Package feishu 将飞书 SDK 的入站文本消息转换为 Hub 路由请求，并发送文本回复。
package feishu

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/estongu/hermes-feishu-router/internal/hub"
	"github.com/estongu/hermes-feishu-router/internal/model"
)

// defaultConversationTimeout 是后台对话的最长执行时间。
// 飞书长连接事件上下文通常只有十余秒，不能直接用于模型推理。
const defaultConversationTimeout = 5 * time.Minute

// ConversationService 是飞书适配层所需的最小 Hub 能力，便于隔离 SDK 并进行单测。
type ConversationService interface {
	Handle(ctx context.Context, message hub.Message) (string, error)
	HandleStream(ctx context.Context, message hub.Message, onChunk func(string)) (string, error)
}

// Sender 将纯文本发送回产生该消息的飞书 chat_id。
type Sender interface {
	SendText(ctx context.Context, chatID, text string) error
}

// StreamSender 可将一条飞书消息持续追加更新；普通 Sender 仍可作为兼容降级。
type StreamSender interface {
	StartStream(ctx context.Context, chatID, firstChunk string) (Stream, error)
}

// CardSender 用 Card 2.0 发送非流式最终答案。
type CardSender interface {
	SendCard(ctx context.Context, chatID, markdown string) error
}

// Stream 是一条正在输出的飞书消息。
type Stream interface {
	Append(ctx context.Context, text string) error
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

// Message 是飞书 SDK 归一化后的路由字段。UserID 从原始事件 sender_id.user_id 提取。
type Message struct {
	EventID    string
	AppID      string
	UserID     string
	IsExternal bool
	ChatID     string
	Text       string
	Images     []model.ImageAttachment
}

// Handler 处理飞书文本消息，并把 Hub 的最终文本回复发回原聊天。
type Handler struct {
	service       ConversationService
	sender        Sender
	streamEnabled bool
	workerContext context.Context
	workerTimeout time.Duration
}

// NewHandler 创建飞书消息处理器。
func NewHandler(service ConversationService, sender Sender, streamEnabled bool) *Handler {
	return NewHandlerWithWorkerContext(context.Background(), service, sender, streamEnabled)
}

// NewHandlerWithWorkerContext 创建支持后台执行的消息处理器。
// workerContext 应为服务生命周期上下文；服务停止时它会取消所有尚未完成的后台对话。
func NewHandlerWithWorkerContext(workerContext context.Context, service ConversationService, sender Sender, streamEnabled bool) *Handler {
	if workerContext == nil {
		workerContext = context.Background()
	}
	return &Handler{
		service:       service,
		sender:        sender,
		streamEnabled: streamEnabled,
		workerContext: workerContext,
		workerTimeout: defaultConversationTimeout,
	}
}

// Handle 路由一条飞书消息。重复事件不会发送空消息；未配置员工路由时回复明确提示。
func (h *Handler) Handle(ctx context.Context, message Message) error {
	if err := validateMessage(message); err != nil {
		return err
	}
	// 先确认收到，避免用户在 Ekko 规划与模型执行期间看到长时间空白。
	// 最终答案（或流式答案）会作为随后的一条机器人消息发送。
	if err := h.sendAccepted(ctx, message.ChatID); err != nil {
		return fmt.Errorf("发送任务已接收提示: %w", err)
	}
	return h.handleConversation(ctx, message)
}

// HandleAsync 在飞书事件回调中快速确认消息，并在独立服务上下文中完成耗时对话。
// 入参 ctx 仅用于本次确认消息；不能传给后台任务，否则飞书的事件截止时间会取消 Ekko 调用。
func (h *Handler) HandleAsync(ctx context.Context, message Message) error {
	if err := validateMessage(message); err != nil {
		return err
	}
	if err := h.sendAccepted(ctx, message.ChatID); err != nil {
		return fmt.Errorf("发送任务已接收提示: %w", err)
	}
	go h.handleInBackground(message)
	return nil
}

// handleInBackground 使用服务生命周期上下文执行对话，并向用户反馈可恢复的失败。
func (h *Handler) handleInBackground(message Message) {
	ctx, cancel := context.WithTimeout(h.workerContext, h.workerTimeout)
	defer cancel()
	if err := h.handleConversation(ctx, message); err != nil {
		log.Printf("异步处理飞书对话失败: event_id=%s chat_id=%s: %v", message.EventID, message.ChatID, err)
		if sendErr := h.sender.SendText(ctx, message.ChatID, "处理失败，请稍后重试。"); sendErr != nil {
			log.Printf("发送飞书对话失败提示失败: event_id=%s chat_id=%s: %v", message.EventID, message.ChatID, sendErr)
		}
	}
}

// validateMessage 校验进入 Hub 前必须具备的事件定位字段。
func validateMessage(message Message) error {
	if strings.TrimSpace(message.EventID) == "" || strings.TrimSpace(message.AppID) == "" || strings.TrimSpace(message.UserID) == "" || strings.TrimSpace(message.ChatID) == "" {
		return fmt.Errorf("飞书消息缺少 event_id、app_id、user_id 或 chat_id")
	}
	return nil
}

// sendAccepted 发送立即确认，提示用户后台任务已经开始执行。
func (h *Handler) sendAccepted(ctx context.Context, chatID string) error {
	return h.sender.SendText(ctx, chatID, "已收到你的问题，正在处理，请稍候。")
}

// handleConversation 完成一次同步的 Hub 路由与最终回复。
func (h *Handler) handleConversation(ctx context.Context, message Message) error {
	hubMessage := hub.Message{
		EventID:      message.EventID,
		AppID:        message.AppID,
		FeishuUserID: message.UserID,
		IsExternal:   message.IsExternal,
		ChatID:       message.ChatID,
		Text:         message.Text,
		Images:       message.Images,
	}
	if !h.streamEnabled {
		reply, err := h.service.Handle(ctx, hubMessage)
		if errors.Is(err, hub.ErrRouteNotFound) {
			log.Printf("未找到飞书路由: app_id=%s user_id=%s chat_id=%s", message.AppID, message.UserID, message.ChatID)
			return h.sender.SendText(ctx, message.ChatID, "你还没有被分配 Ekko Studio 容器，请联系管理员配置路由。")
		}
		if err != nil {
			return fmt.Errorf("处理 Ekko 对话: %w", err)
		}
		if reply == "" {
			return nil
		}
		if cardSender, ok := h.sender.(CardSender); ok {
			if err := cardSender.SendCard(ctx, message.ChatID, reply); err != nil {
				return fmt.Errorf("发送飞书回复卡片: %w", err)
			}
			return nil
		}
		return h.sender.SendText(ctx, message.ChatID, reply)
	}
	var stream Stream
	var streamContent string
	streamSender, canStream := h.sender.(StreamSender)
	startStream := func(chunk string) bool {
		created, err := streamSender.StartStream(ctx, message.ChatID, chunk)
		if err != nil {
			log.Printf("创建飞书流式消息失败，将等待最终回复: %v", err)
			return false
		}
		stream = created
		streamContent += chunk
		return true
	}
	onChunk := func(chunk string) {
		if chunk == "" || !canStream {
			return
		}
		if stream == nil {
			startStream(chunk)
			return
		}
		if err := stream.Append(ctx, chunk); err != nil {
			// 飞书限制一条消息的编辑次数。达到上限时续建下一条流式消息，
			// 已展示的前半段保留，后续内容不会丢失。
			log.Printf("当前飞书流式消息不可继续更新，创建续接消息: %v", err)
			stream = nil
			startStream(chunk)
			return
		}
		streamContent += chunk
	}
	reply, err := h.service.HandleStream(ctx, hubMessage, onChunk)
	if errors.Is(err, hub.ErrRouteNotFound) {
		// 仅记录路由定位字段，便于管理员比对数据库 user_id；不记录消息正文或访问令牌。
		log.Printf("未找到飞书路由: app_id=%s user_id=%s chat_id=%s", message.AppID, message.UserID, message.ChatID)
		return h.sender.SendText(ctx, message.ChatID, "你还没有被分配 Ekko Studio 容器，请联系管理员配置路由。")
	}
	if err != nil {
		return fmt.Errorf("处理 Ekko 对话: %w", err)
	}
	if stream != nil {
		// 最终答案可能包含未通过增量事件推送的结尾，将缺失部分补入同一条消息。
		if suffix := strings.TrimPrefix(reply, streamContent); suffix != reply && suffix != "" {
			if err := stream.Append(ctx, suffix); err != nil {
				return fmt.Errorf("补充飞书流式最终内容: %w", err)
			}
		} else if suffix == reply && reply != streamContent {
			// 增量与最终答案不连续时，以最终答案为准追加，保证用户不会只看到中间状态。
			if err := stream.Append(ctx, "\n\n"+reply); err != nil {
				return fmt.Errorf("补充飞书流式最终内容: %w", err)
			}
		}
		if err := stream.Flush(ctx); err != nil {
			// 最终刷新也可能触发飞书的单卡编辑上限；续建一张包含完整答案的卡片，
			// 确保用户至少能拿到完整结果。
			log.Printf("当前飞书流式消息不可完成刷新，创建最终续接卡片: %v", err)
			stream = nil
			if !canStream || !startStream(reply) {
				return fmt.Errorf("刷新飞书流式消息: %w", err)
			}
		}
		return stream.Close(ctx)
	}
	if reply == "" {
		return nil
	}
	return h.sender.SendText(ctx, message.ChatID, reply)
}
