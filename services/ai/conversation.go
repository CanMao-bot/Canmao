package ai

import (
	"context"
	"fmt"
	"sync"

	"gobot/core"
)

type conversationSlot struct {
	token chan struct{}
	refs  int
}
type conversationLocks struct {
	mu    sync.Mutex
	slots map[string]*conversationSlot
}

func (l *conversationLocks) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.slots == nil {
		l.slots = make(map[string]*conversationSlot)
	}
	slot := l.slots[key]
	if slot == nil {
		slot = &conversationSlot{token: make(chan struct{}, 1)}
		l.slots[key] = slot
	}
	slot.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		slot.refs--
		if slot.refs == 0 {
			delete(l.slots, key)
		}
	}
	select {
	case slot.token <- struct{}{}:
		return func() { <-slot.token; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func (s *Service) lockConversation(ctx context.Context, ev *core.Event) (func(), error) {
	scope, gid, uid := s.sessionScope(ev)
	return s.conversations.acquire(ctx, fmt.Sprintf("%s:%d:%d", scope, gid, uid))
}

func (s *Service) HandleControl(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if ev.Type != "message" {
		return false
	}
	if ev.IsGroup() && s.perm != nil && !s.perm.BotEnabled(ev.GroupID) {
		return false
	}
	// Owner can stop or configure AI while a conversation is blocked on a tool/LLM.
	if cmd, arg := cmdOf(ev.Text()); cmd == "/ai" {
		return s.handleGroupAICommand(bot, ev, arg)
	}
	return s.approvals.Resolve(ev) || s.asker.Resolve(ev)
}
