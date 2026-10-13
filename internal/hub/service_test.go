package hub

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/estongu/hermes-feishu-router/internal/model"
	"github.com/estongu/hermes-feishu-router/internal/store"
)

type fakeStore struct {
	mu         sync.Mutex
	processed  map[string]bool
	target     model.EkkoTarget
	session    model.ChatSession
	hasSession bool
	saved      model.ChatSession
}

func (s *fakeStore) FindTargetByFeishuUserID(context.Context, string) (model.EkkoTarget, error) {
	if s.target.ContainerID == "" {
		return model.EkkoTarget{}, store.ErrNotFound
	}
	return s.target, nil
}
func (s *fakeStore) FindTargetByExternalOpenID(context.Context, string, string) (model.EkkoTarget, error) {
	if s.target.ContainerID == "" {
		return model.EkkoTarget{}, store.ErrNotFound
	}
	return s.target, nil
}
func (s *fakeStore) RecordUnmatchedIdentity(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *fakeStore) FindSession(context.Context, string, string, string) (model.ChatSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSession {
		return model.ChatSession{}, store.ErrNotFound
	}
	return s.session, nil
}
func (s *fakeStore) SaveSession(_ context.Context, value model.ChatSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = value
	s.session = value
	s.hasSession = true
	return nil
}
func (s *fakeStore) MarkEventProcessed(_ context.Context, _, eventID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processed == nil {
		s.processed = make(map[string]bool)
	}
	if s.processed[eventID] {
		return false, nil
	}
	s.processed[eventID] = true
	return true, nil
}

type fakeAccessResolver struct{}

func (fakeAccessResolver) Resolve(context.Context, string) (model.StudioAccess, error) {
	return model.StudioAccess{BaseURL: "http://studio:16020", Token: "studio-token"}, nil
}

type fakeEkko struct{ ran int }

func (e *fakeEkko) Run(context.Context, model.EkkoTarget, string, string, string, []model.ImageAttachment, func(string)) (string, error) {
	e.ran++
	return "Ekko 回复", nil
}

// TestHandleCreatesAndPersistsSession 验证首次收到未知 chat_id 时会自动创建并保存 Ekko 会话。
func TestHandleCreatesAndPersistsSession(t *testing.T) {
	db := &fakeStore{target: model.EkkoTarget{ContainerID: "hermes-a", Username: "test-user"}}
	ekko := &fakeEkko{}
	service := NewService(db, fakeAccessResolver{}, ekko)

	reply, err := service.Handle(context.Background(), Message{EventID: "event-1", AppID: "cli-bot", FeishuUserID: "ou_user", ChatID: "oc-chat", Text: "你好"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if reply != "Ekko 回复" || ekko.ran != 1 {
		t.Fatalf("unexpected reply or call count: reply=%q run=%d", reply, ekko.ran)
	}
	if db.saved.EkkoSessionID == "" || db.saved.ContainerID != "hermes-a" || db.saved.AppID != "cli-bot" || db.saved.FeishuUserID != "ou_user" {
		t.Fatalf("saved session = %#v", db.saved)
	}
}

// TestHandleDuplicateEvent 验证飞书重复事件不会重复调用模型。
func TestHandleDuplicateEvent(t *testing.T) {
	db := &fakeStore{processed: map[string]bool{"event-1": true}, target: model.EkkoTarget{ContainerID: "ekko-a"}}
	ekko := &fakeEkko{}
	reply, err := NewService(db, fakeAccessResolver{}, ekko).Handle(context.Background(), Message{EventID: "event-1", AppID: "cli-bot", FeishuUserID: "ou_user", ChatID: "oc-chat"})
	if err != nil || reply != "" || ekko.ran != 0 {
		t.Fatalf("duplicate = reply %q, err %v, runs %d", reply, err, ekko.ran)
	}
}

// TestHandleRouteNotFound 验证未被分配容器的员工得到明确业务错误。
func TestHandleRouteNotFound(t *testing.T) {
	_, err := NewService(&fakeStore{}, fakeAccessResolver{}, &fakeEkko{}).Handle(context.Background(), Message{EventID: "event-1", AppID: "cli-bot", FeishuUserID: "ou_user", ChatID: "oc-chat"})
	if !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("Handle() error = %v, want ErrRouteNotFound", err)
	}
}

// TestHandleExternalIdentity 验证外部成员以当前应用范围内的 open_id 建立独立会话。
func TestHandleExternalIdentity(t *testing.T) {
	db := &fakeStore{target: model.EkkoTarget{ContainerID: "hermes-external", Username: "external-user"}}
	ekko := &fakeEkko{}
	_, err := NewService(db, fakeAccessResolver{}, ekko).Handle(context.Background(), Message{EventID: "event-external", AppID: "cli-gateway", FeishuUserID: "ou_external", IsExternal: true, ChatID: "oc-shared", Text: "你好"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if db.saved.FeishuUserID != "ou_external" || db.saved.AppID != "cli-gateway" || ekko.ran != 1 {
		t.Fatalf("external session = %#v, run=%d", db.saved, ekko.ran)
	}
}

// TestHandleSerializesSameChat 验证同一用户同一 chat 的并发输入会被串行化。
func TestHandleSerializesSameChat(t *testing.T) {
	db := &fakeStore{target: model.EkkoTarget{ContainerID: "hermes-a", Username: "test-user"}}
	ekko := &fakeEkko{}
	service := NewService(db, fakeAccessResolver{}, ekko)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, _ = service.Handle(context.Background(), Message{EventID: string(rune('a' + index)), AppID: "cli-bot", FeishuUserID: "ou_user", ChatID: "oc-chat"})
		}(i)
	}
	wg.Wait()
	if db.saved.EkkoSessionID == "" {
		t.Fatal("saved session id is empty")
	}
}
