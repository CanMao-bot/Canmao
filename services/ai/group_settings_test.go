package ai

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/perm"
)

func groupPolicyFixture(t *testing.T) *Service {
	t.Helper()
	s := securityFixture(t)
	if err := s.perm.SetGroupBot(10, true); err != nil {
		t.Fatal(err)
	}
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) { completeText(w, "ok") })
	return s
}
func TestOnlyRealOwnerMayChangeGroupAI(t *testing.T) {
	s := groupPolicyFixture(t)
	if err := s.perm.SetGroupRole(10, 3, perm.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{2, 3, 4} {
		for _, cmd := range []string{"/ai on", "/ai mention on", "/ai probability 1", "/ai reset"} {
			ev := securityEvent(uid, 10, cmd)
			s.handleCommands(core.WithEvent(context.Background(), ev), s.bot, ev, true)
			g, _ := s.perm.GroupAI(10)
			if g.Enabled || g.MentionOnly != nil || g.ReplyProbability != nil {
				t.Fatalf("user %d elevated using %s: %+v", uid, cmd, g)
			}
		}
	}
	ev := securityEvent(1, 10, "/ai on")
	s.HandleControl(context.Background(), s.bot, ev)
	if !s.groupAIEnabled(10) {
		t.Fatal("owner enable failed")
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(2, 10, "/ai off"))
	if !s.groupAIEnabled(10) {
		t.Fatal("config admin disabled owner AI")
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 0, "/ai off"))
	if !s.groupAIEnabled(10) {
		t.Fatal("private command modified group")
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai off"))
	if s.groupAIEnabled(10) {
		t.Fatal("owner disable failed")
	}
}
func TestGroupAISettingsReadDoesNotEnableAndNoModelBlocksEnable(t *testing.T) {
	s := securityFixture(t)
	if err := s.perm.SetGroupBot(10, true); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"/ai", "/ai settings", "/ai status", "/ai on"} {
		s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, cmd))
		if s.groupAIEnabled(10) {
			t.Fatal("model missing or readonly command enabled AI", cmd)
		}
	}
	// Owner messages in an unapproved group do not emit provider setup guidance.
	for len(s.bot.Sender.(*securitySender).sent) > 0 {
		<-s.bot.Sender.(*securitySender).sent
	}
	ev := securityEvent(1, 10, "hello")
	s.Handle(context.Background(), s.bot, ev)
	if len(s.bot.Sender.(*securitySender).sent) > 0 {
		t.Fatal("disabled group emitted model guidance")
	}
}
func TestIndependentAITriggersAndOwnerChange(t *testing.T) {
	s := groupPolicyFixture(t)
	s.cfg.OneBot.SelfID = "9"
	s.cfg.AI.ReplyProbability = fp(0)
	s.cfg.AI.MentionOnly = true
	for _, gid := range []int64{10, 20} {
		if err := s.perm.SetGroupBot(gid, true); err != nil {
			t.Fatal(err)
		}
		on := true
		if err := s.perm.ConfigureGroupAI(gid, 1, 1, perm.GroupAIPatch{Enabled: &on}); err != nil {
			t.Fatal(err)
		}
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai mention off"))
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai probability 1"))
	if !s.shouldRespond(s.bot, securityEvent(4, 10, "hello"), false) {
		t.Fatal("local probability override ignored")
	}
	if s.shouldRespond(s.bot, securityEvent(4, 20, "hello"), false) {
		t.Fatal("override leaked to other group")
	}
	ev := securityEvent(4, 20, "hello")
	ev.Message = append(ev.Message, core.AtSegment(9))
	if !s.shouldRespond(s.bot, ev, false) {
		t.Fatal("mention override ignored")
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai reset"))
	if s.shouldRespond(s.bot, securityEvent(4, 10, "hello"), false) {
		t.Fatal("reset failed")
	}
	s.cfg.Bot.MasterID = "5"
	if s.groupAIEnabled(10) {
		t.Fatal("old owner authorization survived owner change")
	}
}
func TestOwnerCanDisableAIWhileConversationBlocked(t *testing.T) {
	s := groupPolicyFixture(t)
	on := true
	if err := s.perm.ConfigureGroupAI(10, 1, 1, perm.GroupAIPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	ev := securityEvent(1, 10, "/ai off")
	unlock, err := s.lockConversation(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	done := make(chan struct{})
	go func() { s.HandleControl(context.Background(), s.bot, ev); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner off blocked by active conversation")
	}
	if s.groupAIEnabled(10) {
		t.Fatal("AI still enabled")
	}
}
func TestDisabledGroupUsesNoLLMAndKeepsPrivateAI(t *testing.T) {
	s := securityFixture(t)
	var calls atomic.Int32
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completeText(w, "ok") })
	if err := s.perm.SetGroupBot(10, true); err != nil {
		t.Fatal(err)
	}
	s.Handle(context.Background(), s.bot, securityEvent(1, 10, "hello"))
	s.Handle(context.Background(), s.bot, securityEvent(1, 10, "/compact"))
	if calls.Load() != 0 {
		t.Fatal("disabled group called model")
	}
	s.Handle(context.Background(), s.bot, securityEvent(1, 0, "hello"))
	if calls.Load() != 1 {
		t.Fatal("private AI no longer works", calls.Load())
	}
}

func TestInvalidGroupAISettingsLeaveStateUnchanged(t *testing.T) {
	s := groupPolicyFixture(t)
	for _, cmd := range []string{"/ai probability -1", "/ai probability 2", "/ai probability NaN", "/ai probability +Inf", "/ai mention yes", "/ai on extra"} {
		s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, cmd))
		g, err := s.perm.GroupAI(10)
		if err != nil || g.Enabled || g.MentionOnly != nil || g.ReplyProbability != nil {
			t.Fatalf("invalid command %s changed settings: %+v %v", cmd, g, err)
		}
	}
}
func TestDisabledAICannotExecutePendingApprovedTool(t *testing.T) {
	s := groupPolicyFixture(t)
	on := true
	if err := s.perm.ConfigureGroupAI(10, 1, 1, perm.GroupAIPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	effects := 0
	tool := NewTool("pending_effect", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) { effects++; return "done", nil })
	tool.Risk = RiskHigh
	s.AddTool(tool)
	ev := securityEvent(3, 10, "")
	ctx := context.WithValue(core.WithEvent(context.Background(), ev), groupAIGuardKey{}, true)
	done := make(chan string, 1)
	tc := ToolCall{ID: "pending-test", Type: "function"}
	tc.Function.Name = "pending_effect"
	tc.Function.Arguments = "{}"
	go func() { done <- s.executeTool(ctx, tc) }()
	var nonce string
	deadline := time.After(time.Second)
	for nonce == "" {
		select {
		case <-s.bot.Sender.(*securitySender).sent:
			s.approvals.mu.Lock()
			for _, p := range s.approvals.pendings {
				nonce = p.ID
			}
			s.approvals.mu.Unlock()
		case <-deadline:
			t.Fatal("pending approval missing")
		}
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai off"))
	s.approvals.Resolve(securityEvent(1, 10, "同意 "+nonce))
	select {
	case result := <-done:
		if effects != 0 || !strings.Contains(result, "AI 已关闭") {
			t.Fatal("disabled tool executed", effects, result)
		}
	case <-time.After(time.Second):
		t.Fatal("pending tool not resolved")
	}
}
