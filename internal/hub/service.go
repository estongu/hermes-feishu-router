// Package hub 实现飞书消息到目标 Ekko Studio 的路由编排。
package hub

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/estongu/hermes-feishu-router/internal/model"
	"github.com/estongu/hermes-feishu-router/internal/store"
)

var (
	ErrRouteNotFound = errors.New("当前飞书用户尚未分配 Ekko 容器")
)

// Message 是飞书 SDK 归一化后的最小消息字段。
// FeishuUserID 跨应用稳定，AppID 用于隔离不同机器人的会话。
type Message struct {
	EventID      string
	AppID        string
	FeishuUserID string
	ChatID       string
	Text         string
}

// AccessResolver 按路由中的 username 获取本次请求可用的 Studio 地址与短期令牌。
type AccessResolver interface {
	Resolve(ctx context.Context, username string) (model.StudioAccess, error)
}

// EkkoClient 约束 Hub 对 Ekko Studio 的必要能力。
// 具体 Socket.IO 协议实现将在对实际部署版本完成兼容性验证后接入。
type EkkoClient interface {
	Run(ctx context.Context, target model.EkkoTarget, token, sessionID, text string, onChunk func(string)) (string, error)
}

// Service 负责去重、查路由、创建或恢复会话并执行 Ekko 对话。
type Service struct {
	store  store.Store
	access AccessResolver
	ekko   EkkoClient
	locks  sync.Map
}

// NewService 创建消息编排服务。
func NewService(store store.Store, access AccessResolver, ekko EkkoClient) *Service {
	return &Service{store: store, access: access, ekko: ekko}
}

// Handle 处理一条飞书消息并返回最终回复。
// 同一个 app_id + user_id + chat_id 会被串行化，确保 Ekko 上下文不会因并发输入而乱序。
func (s *Service) Handle(ctx context.Context, message Message) (string, error) {
	return s.handle(ctx, message, nil)
}

// HandleStream 处理一条消息，并在 Studio 发出增量文本时调用 onChunk。
// 若目标 Studio 未发出增量事件，onChunk 不会被调用，返回值仍为完整最终答案。
func (s *Service) HandleStream(ctx context.Context, message Message, onChunk func(string)) (string, error) {
	return s.handle(ctx, message, onChunk)
}

// handle 完成路由、会话恢复与 Ekko 调用；onChunk 为可选流式输出回调。
func (s *Service) handle(ctx context.Context, message Message, onChunk func(string)) (string, error) {
	if message.EventID == "" || message.AppID == "" || message.FeishuUserID == "" || message.ChatID == "" {
		return "", fmt.Errorf("飞书消息缺少 event_id、app_id、user_id 或 chat_id")
	}
	processed, err := s.store.MarkEventProcessed(ctx, message.AppID, message.EventID)
	if err != nil {
		return "", err
	}
	if !processed {
		return "", nil
	}

	key := message.AppID + ":" + message.FeishuUserID + ":" + message.ChatID
	lock := s.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	target, err := s.store.FindTargetByFeishuUserID(ctx, message.FeishuUserID)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrRouteNotFound
	}
	if err != nil {
		return "", err
	}
	access, err := s.access.Resolve(ctx, target.Username)
	if err != nil {
		return "", err
	}
	target.BaseURL = access.BaseURL

	session, err := s.store.FindSession(ctx, message.AppID, message.FeishuUserID, message.ChatID)
	if errors.Is(err, store.ErrNotFound) || session.ContainerID != target.ContainerID {
		// Ekko 的 chat-run 协议允许客户端为新会话生成 UUID，并在首次运行时持久化它。
		sessionID, createErr := newSessionID()
		if createErr != nil {
			return "", fmt.Errorf("生成 Ekko 会话 ID: %w", createErr)
		}
		session = model.ChatSession{AppID: message.AppID, FeishuUserID: message.FeishuUserID, ChatID: message.ChatID, ContainerID: target.ContainerID, EkkoSessionID: sessionID}
		if saveErr := s.store.SaveSession(ctx, session); saveErr != nil {
			return "", saveErr
		}
	} else if err != nil {
		return "", err
	}

	reply, err := s.ekko.Run(ctx, target, access.Token, session.EkkoSessionID, message.Text, onChunk)
	if err != nil {
		return "", fmt.Errorf("执行 Ekko 会话: %w", err)
	}
	return reply, nil
}

// newSessionID 生成 RFC 4122 version 4 UUID，作为新 Ekko 会话的客户端 ID。
func newSessionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func (s *Service) lockFor(key string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}
