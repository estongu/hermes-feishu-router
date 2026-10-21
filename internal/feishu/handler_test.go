package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/estongu/hermes-feishu-router/internal/hub"
)

type fakeService struct {
	reply string
	err   error
}

func (s fakeService) Handle(context.Context, hub.Message) (string, error) { return s.reply, s.err }
func (s fakeService) HandleStream(_ context.Context, _ hub.Message, _ func(string)) (string, error) {
	return s.reply, s.err
}

type fakeSender struct {
	chatID string
	text   string
}

type blockingService struct {
	started chan context.Context
	release chan struct{}
}

// Handle 阻塞到测试显式放行，用于验证后台任务没有继承飞书事件的取消信号。
func (s blockingService) Handle(ctx context.Context, _ hub.Message) (string, error) {
	s.started <- ctx
	<-s.release
	return "后台回答", nil
}

func (s blockingService) HandleStream(ctx context.Context, message hub.Message, _ func(string)) (string, error) {
	return s.Handle(ctx, message)
}

type fakeStream struct {
	content string
	closed  bool
}

func (s *fakeStream) Append(_ context.Context, text string) error { s.content += text; return nil }
func (s *fakeStream) Flush(context.Context) error                 { return nil }
func (s *fakeStream) Close(context.Context) error                 { s.closed = true; return nil }

type fakeStreamSender struct {
	fakeSender
	stream *fakeStream
}

func (s *fakeStreamSender) SendCard(_ context.Context, chatID, markdown string) error {
	s.chatID, s.text = chatID, markdown
	return nil
}

func (s *fakeStreamSender) StartStream(_ context.Context, _ string, firstChunk string) (Stream, error) {
	s.stream = &fakeStream{content: firstChunk}
	return s.stream, nil
}

type cappedStream struct{ fakeStream }

func (s *cappedStream) Append(context.Context, string) error { return errors.New("edit limit") }

type rolloverStreamSender struct{ streams []*fakeStream }

func (s *rolloverStreamSender) SendText(context.Context, string, string) error { return nil }
func (s *rolloverStreamSender) StartStream(_ context.Context, _ string, firstChunk string) (Stream, error) {
	if len(s.streams) == 0 {
		first := &cappedStream{fakeStream: fakeStream{content: firstChunk}}
		s.streams = append(s.streams, &first.fakeStream)
		return first, nil
	}
	next := &fakeStream{content: firstChunk}
	s.streams = append(s.streams, next)
	return next, nil
}

type chunkService struct{}

func (chunkService) Handle(context.Context, hub.Message) (string, error) { return "完整回答", nil }
func (chunkService) HandleStream(_ context.Context, _ hub.Message, onChunk func(string)) (string, error) {
	onChunk("完整")
	onChunk("回答")
	return "完整回答", nil
}

func (s *fakeSender) SendText(_ context.Context, chatID, text string) error {
	s.chatID, s.text = chatID, text
	return nil
}

// TestHandlerRouteNotFound 验证未路由员工会在原 chat_id 收到明确配置提示。
func TestHandlerRouteNotFound(t *testing.T) {
	sender := &fakeSender{}
	handler := NewHandler(fakeService{err: hub.ErrRouteNotFound}, sender, true)
	if err := handler.Handle(context.Background(), Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat", Text: "你好"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sender.chatID != "oc_chat" || sender.text == "" {
		t.Fatalf("sender = %#v", sender)
	}
}

// TestHandlerDuplicateEvent 验证重复事件没有最终内容时只保留已接收提示。
func TestHandlerDuplicateEvent(t *testing.T) {
	sender := &fakeSender{}
	handler := NewHandler(fakeService{}, sender, true)
	if err := handler.Handle(context.Background(), Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sender.text == "" {
		t.Fatal("expected acknowledgement")
	}
}

// TestHandlerPropagatesFailure 验证非路由类错误会被返回给 SDK，以便其按平台策略重试。
func TestHandlerPropagatesFailure(t *testing.T) {
	handler := NewHandler(fakeService{err: errors.New("network failed")}, &fakeSender{}, true)
	if err := handler.Handle(context.Background(), Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat"}); err == nil {
		t.Fatal("expected error")
	}
}

// TestHandlerStreamsChunks 验证增量到达时复用一条飞书消息，并在结束后关闭流。
func TestHandlerStreamsChunks(t *testing.T) {
	sender := &fakeStreamSender{}
	handler := NewHandler(chunkService{}, sender, true)
	if err := handler.Handle(context.Background(), Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sender.stream == nil || sender.stream.content != "完整回答" || !sender.stream.closed {
		t.Fatalf("stream = %#v", sender.stream)
	}
}

// TestHandlerRollsOverStream 验证飞书达到单消息编辑上限后会续建下一条流式消息。
func TestHandlerRollsOverStream(t *testing.T) {
	sender := &rolloverStreamSender{}
	handler := NewHandler(chunkService{}, sender, true)
	if err := handler.Handle(context.Background(), Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(sender.streams) != 2 || sender.streams[1].content != "回答" {
		t.Fatalf("streams = %#v", sender.streams)
	}
}

// TestHandlerSendsCardWhenStreamDisabled 验证关闭流式后最终答案仍以卡片发送。
func TestHandlerSendsCardWhenStreamDisabled(t *testing.T) {
	sender := &fakeStreamSender{}
	handler := NewHandler(fakeService{reply: "**最终答案**"}, sender, false)
	if err := handler.Handle(context.Background(), Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat"}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sender.text != "**最终答案**" {
		t.Fatalf("card content = %q", sender.text)
	}
}

// TestHandleAsyncUsesWorkerContext 验证飞书事件 ctx 被取消后，后台 Ekko 对话仍会继续执行。
// 入参是一个可取消的事件上下文；预期后台服务收到的 ctx 不会因该事件取消而结束。
func TestHandleAsyncUsesWorkerContext(t *testing.T) {
	eventCtx, cancelEvent := context.WithCancel(context.Background())
	defer cancelEvent()
	service := blockingService{started: make(chan context.Context, 1), release: make(chan struct{})}
	handler := NewHandlerWithWorkerContext(context.Background(), service, &fakeSender{}, false)
	handler.workerTimeout = time.Second
	if err := handler.HandleAsync(eventCtx, Message{EventID: "evt", AppID: "cli_bot", UserID: "user_1", ChatID: "oc_chat", Text: "你好"}); err != nil {
		t.Fatalf("HandleAsync() error = %v", err)
	}
	cancelEvent()
	select {
	case workerCtx := <-service.started:
		if err := workerCtx.Err(); err != nil {
			t.Fatalf("后台上下文被飞书事件取消: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("后台任务未启动")
	}
	close(service.release)
}
