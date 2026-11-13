package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/mood"
	"gobot/store/persona"
)

func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(what)
	}
}

func TestReplyAndNextTurnDoNotWaitForAuxiliaryModels(t *testing.T) {
	s := groupPolicyFixture(t)
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai on"))
	s.cfg.Mood.Enabled = true
	moodStore, err := mood.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mm := NewMoodManager(moodStore, s.bot, 10)
	s.SetMoodManager(mm)
	moodStarted, releaseMood := make(chan struct{}), make(chan struct{})
	var moodCalls atomic.Int32
	mm.SetLLM(func(ctx context.Context, _ string) (EmotionResult, error) {
		if moodCalls.Add(1) == 1 {
			close(moodStarted)
		}
		select {
		case <-releaseMood:
			return EmotionResult{Delta: 1, Emotion: "happy"}, nil
		case <-ctx.Done():
			return EmotionResult{}, ctx.Err()
		}
	})
	s.cfg.Persona.Enabled, s.cfg.Persona.SelfImprove, s.cfg.Persona.ImproveEvery = true, true, 1
	personaStore, err := persona.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pm := NewPersonaManager(personaStore, s.bot, s.cfg)
	s.SetPersonaManager(pm)
	personaStarted := make(chan struct{})
	var personaCalls atomic.Int32
	pm.SetLLM(func(ctx context.Context, _ string) (string, error) {
		if personaCalls.Add(1) == 1 {
			close(personaStarted)
		}
		<-ctx.Done()
		return "", ctx.Err()
	})
	handle := func(message string) <-chan struct{} {
		done := make(chan struct{})
		go func() { s.Handle(context.Background(), s.bot, securityEvent(1, 10, message)); close(done) }()
		return done
	}
	first := handle("hello")
	awaitSignal(t, moodStarted, "情绪检测未启动")
	awaitSignal(t, first, "首答等待情绪检测")
	awaitSignal(t, handle("another turn"), "下条消息被后台情绪检测阻塞")
	close(releaseMood)
	awaitSignal(t, personaStarted, "人设优化未启动")
	awaitSignal(t, handle("third turn"), "下条消息被人设优化阻塞")
}

func toolCompletion(w http.ResponseWriter, content string, calls ...ToolCall) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": Message{Role: "assistant", Content: content, ToolCalls: calls}}}})
}

func callFor(name, args, id string) ToolCall {
	tc := ToolCall{ID: id, Type: "function"}
	tc.Function.Name, tc.Function.Arguments = name, args
	return tc
}

func TestResidentMessageSendsBeforeLongToolAndRemainsAvailable(t *testing.T) {
	s := securityFixture(t)
	s.cfg.AI.MaxIterations = 1 // The closing request must retain output as well.
	started, release := make(chan struct{}), make(chan struct{})
	slow := NewTool("slow_work", "test", nil, nil, func(ctx context.Context, _ map[string]interface{}) (string, error) {
		close(started)
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	slow.Risk = RiskLow
	s.AddTool(slow)
	var rounds atomic.Int32
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []Tool `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		found := false
		for _, tool := range req.Tools {
			if tool.Function.Name == "qq_send_message" {
				found = true
			}
		}
		if !found {
			t.Error("常驻输出工具丢失")
		}
		if rounds.Add(1) == 1 {
			toolCompletion(w, "", callFor("qq_send_message", `{"message":"正在处理任务"}`, "notice"), callFor("slow_work", "{}", "work"))
		} else {
			completeText(w, "处理完成")
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, toolSetKey{}, []Tool{slow})
	done := make(chan struct{})
	go func() {
		defer close(done)
		reply, err := s.runAgent(ctx, []Message{NewTextMessage("user", "do work")}, securityEvent(3, 0, "do work"))
		if err != nil || reply != "处理完成" {
			t.Errorf("%q %v", reply, err)
		}
	}()
	awaitSignal(t, started, "耗时工具未启动")
	select {
	case msg := <-s.bot.Sender.(*securitySender).sent:
		if msg != "正在处理任务" {
			t.Fatalf("进度正文: %q", msg)
		}
	default:
		t.Fatal("耗时工具开始前没有发出进度")
	}
	close(release)
	awaitSignal(t, done, "任务未结束")
}

func TestPublicToolTextIsImmediateAndFinalIsNotRepeated(t *testing.T) {
	s := securityFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	slow := NewTool("slow_work", "test", nil, nil, func(ctx context.Context, _ map[string]interface{}) (string, error) {
		close(started)
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	slow.Risk = RiskLow
	s.AddTool(slow)
	var rounds atomic.Int32
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		if rounds.Add(1) == 1 {
			toolCompletion(w, "正在查询", callFor("slow_work", "{}", "work"))
		} else {
			completeText(w, "正在查询")
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Handle(ctx, s.bot, securityEvent(3, 0, "query")); close(done) }()
	awaitSignal(t, started, "任务未开始")
	select {
	case msg := <-s.bot.Sender.(*securitySender).sent:
		if msg != "正在查询" {
			t.Fatal(msg)
		}
	default:
		t.Fatal("公开正文被工具等待吞掉")
	}
	close(release)
	awaitSignal(t, done, "任务未结束")
	select {
	case msg := <-s.bot.Sender.(*securitySender).sent:
		t.Fatalf("重复发送: %q", msg)
	default:
	}
}

func TestQQMessageIdentityCancellationAndReceipt(t *testing.T) {
	s := groupPolicyFixture(t)
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai on"))
	for {
		select {
		case <-s.bot.Sender.(*securitySender).sent:
			continue
		default:
		}
		break
	}
	ctx := core.WithEvent(context.Background(), securityEvent(3, 10, "progress"))
	ctx = context.WithValue(ctx, qqOutputKey{}, &qqOutputState{sent: map[qqMessageTarget]map[string]bool{}})
	tc := callFor("qq_send_message", `{"message":"公开进度"}`, "notice")
	if result := s.executeTool(ctx, tc); !strings.Contains(result, `"success":true`) || !strings.Contains(result, `"message_id":`) {
		t.Fatal(result)
	}
	if result := s.executeTool(ctx, tc); !strings.Contains(result, `"duplicate":true`) {
		t.Fatal(result)
	}
	<-s.bot.Sender.(*securitySender).sent
	for _, args := range []string{`{"user_id":4,"message":"x"}`, `{"user_id":3,"message":"x"}`, `{"group_id":11,"message":"x"}`, `{"group_id":10,"user_id":3,"message":"x"}`, `{"group_id":0,"message":"x"}`, `{"group_id":9223372036854775808,"message":"x"}`} {
		tc.Function.Arguments = args
		if result := s.executeTool(ctx, tc); !strings.Contains(result, "无权") {
			t.Fatalf("%s: %s", args, result)
		}
	}
	tc.Function.Arguments = `{"message":"another"}`
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if result := s.executeTool(canceled, tc); !strings.Contains(result, "取消") {
		t.Fatal(result)
	}
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai off"))
	if result := s.executeTool(ctx, tc); !strings.Contains(result, "无权") {
		t.Fatal(result)
	}
}

func TestCrossConversationMessageStillNeedsApproval(t *testing.T) {
	s := securityFixture(t)
	ctx, cancel := context.WithCancel(core.WithEvent(context.Background(), securityEvent(1, 0, "notify")))
	defer cancel()
	result := make(chan string, 1)
	go func() {
		result <- s.executeTool(ctx, callFor("qq_send_message", `{"user_id":4,"message":"通知目标"}`, "notice"))
	}()
	select {
	case msg := <-s.bot.Sender.(*securitySender).sent:
		if !strings.Contains(msg, "qq_send_message") {
			t.Fatalf("没有审批卡片: %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("未请求跨会话审批")
	}
	cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("取消未退出审批")
	}
	select {
	case msg := <-s.bot.Sender.(*securitySender).sent:
		if msg == "通知目标" {
			t.Fatal("未批准即发送")
		}
	default:
	}
}

func TestBackgroundQueueIsBoundedAndShutdownCancelsWorker(t *testing.T) {
	s := securityFixture(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	s.enqueueBackground(context.Background(), "blocked", time.Minute, func(ctx context.Context) { close(started); <-ctx.Done(); close(stopped) })
	awaitSignal(t, started, "后台未启动")
	for i := 0; i < cap(s.backgroundJobs); i++ {
		if !s.enqueueBackground(context.Background(), fmt.Sprint(i), time.Minute, func(context.Context) { t.Error("退出后仍执行排队任务") }) {
			t.Fatal("队列过早满")
		}
	}
	if s.enqueueBackground(context.Background(), "overflow", time.Minute, func(context.Context) {}) {
		t.Fatal("没有背压")
	}
	s.bot.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.bot.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, stopped, "退出没有取消辅助任务")
}

type receiptFailureSender struct {
	*securitySender
	err error
}

func (s *receiptFailureSender) SendMessageWithReceipt(context.Context, int64, int64, []core.Segment) (int64, error) {
	return 0, s.err
}

func TestQQMessageNeverReportsFailedOrMissingReceiptAsSuccess(t *testing.T) {
	for _, failure := range []error{errors.New("connection closed"), nil} {
		s := securityFixture(t)
		s.bot.SetSender(&receiptFailureSender{s.bot.Sender.(*securitySender), failure})
		ctx := core.WithEvent(context.Background(), securityEvent(3, 0, "notice"))
		result := s.executeTool(ctx, callFor("qq_send_message", `{"message":"progress"}`, "notice"))
		if strings.Contains(result, `"success":true`) || !strings.Contains(result, "失败") {
			t.Fatal(result)
		}
	}
}
