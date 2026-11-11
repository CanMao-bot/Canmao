package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/allow"
	"gobot/store/perm"
	"gobot/store/provider"
	"gobot/store/sched"
	"gobot/store/session"
)

type securitySender struct {
	sent     chan string
	sequence atomic.Int64
}

func (s *securitySender) SendMessageWithReceipt(ctx context.Context, gid, uid int64, segs []core.Segment) (int64, error) {
	id := s.sequence.Add(1)
	return id, s.send(segs)
}

func quoteApproval(s *Service, ev *core.Event, id string) *core.Event {
	// Internal request IDs are only used by tests to find the sent message.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.approvals.mu.Lock()
		if p := s.approvals.pendings[id]; p != nil {
			for _, msg := range p.Messages {
				if (ev.IsGroup() && msg.GroupID == ev.GroupID) || (ev.IsPrivate() && msg.GroupID == 0 && msg.UserID == ev.UserID) {
					ev.Message = append([]core.Segment{{Type: "reply", Data: map[string]interface{}{"id": msg.ID}}}, ev.Message...)
					s.approvals.mu.Unlock()
					return ev
				}
			}
			if len(p.Messages) > 0 && (ev.IsGroup() || ev.UserID != 1) {
				ev.Message = append([]core.Segment{{Type: "reply", Data: map[string]interface{}{"id": p.Messages[0].ID}}}, ev.Message...)
				s.approvals.mu.Unlock()
				return ev
			}
		}
		s.approvals.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	return ev
}

func (s *securitySender) send(segs []core.Segment) error {
	select {
	case s.sent <- segmentsText(segs):
	default:
	}
	return nil
}
func (s *securitySender) SendGroupMsg(_, _ int64, segs []core.Segment) error { return s.send(segs) }
func (s *securitySender) SendPrivateMsg(_ int64, segs []core.Segment) error  { return s.send(segs) }

func securityFixture(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	cfg := &core.Config{Bot: core.BotConfig{MasterID: "1", AdminIDs: []string{"2"}}, AI: core.AIConfig{MaxHistory: 4, ContextWindow: 64000, Timeout: 3, Model: "test-model"}}
	p, err := perm.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := allow.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	bot := core.NewBot(cfg)
	bot.SetSender(&securitySender{sent: make(chan string, 100)})
	svc := New(cfg, p, st, a, bot)
	t.Cleanup(func() { bot.Shutdown(); a.Close(); st.Close(); p.Close() })
	return svc
}

func securityEvent(uid, gid int64, text string) *core.Event {
	ev := &core.Event{Type: "message", DetailType: "private", UserID: uid, RawMessage: text, Message: []core.Segment{core.TextSegment(text)}}
	if gid != 0 {
		ev.DetailType = "group"
		ev.GroupID = gid
	}
	return ev
}

func installTestModel(t *testing.T, s *Service, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s.cfg.AI.BaseURL = server.URL
	prov, err := provider.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.SetProviderStore(prov)
	if err = s.models.InitFromConfig(); err != nil {
		t.Fatal(err)
	}
}

func completeText(w http.ResponseWriter, text string) {
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": map[string]interface{}{"role": "assistant", "content": text}}}})
}

func requestApproval(t *testing.T, s *Service, ev *core.Event, risk RiskLevel) (string, <-chan *ApprovalResult) {
	t.Helper()
	for {
		select {
		case <-s.bot.Sender.(*securitySender).sent:
			continue
		default:
		}
		break
	}
	result := make(chan *ApprovalResult, 1)
	go func() {
		res, err := s.approvals.Request(context.Background(), ev, "review_action", map[string]interface{}{}, risk)
		if err != nil {
			result <- &ApprovalResult{Reason: err.Error()}
		} else {
			result <- res
		}
	}()
	select {
	case <-s.bot.Sender.(*securitySender).sent:
	case <-time.After(time.Second):
		t.Fatal("审批未发出")
	}
	s.approvals.mu.Lock()
	defer s.approvals.mu.Unlock()
	for _, p := range s.approvals.pendings {
		if p.Requester == ev.UserID {
			return p.ID, result
		}
	}
	t.Fatal("缺少审批请求")
	return "", nil
}

func TestApprovalRoutesToOwnerAndGrantsRequester(t *testing.T) {
	s := securityFixture(t)
	requester := securityEvent(3, 7, "")
	id, result := requestApproval(t, s, requester, RiskHigh)
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(3, 7, "同意"), id)) {
		t.Fatal("应处理无权批准的回复")
	}
	select {
	case <-result:
		t.Fatal("发起人自批不得结束审批")
	default:
	}
	if s.approvals.Resolve(quoteApproval(s, securityEvent(1, 8, "同意"), id)) {
		t.Fatal("不允许跨群批准")
	}
	if s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "不要以后都允许"), id)) {
		t.Fatal("否定句不得授权")
	}
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "以后都允许"), id)) {
		t.Fatal("主人应能定位成员请求")
	}
	select {
	case res := <-result:
		if !res.Approved {
			t.Fatal(res.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("审批未唤醒")
	}
	if !s.approvals.IsAllowedFor(requester, "review_action", RiskHigh) {
		t.Fatal("权限应授予发起人")
	}
	if s.approvals.IsAllowedFor(securityEvent(1, 7, ""), "review_action", RiskHigh) {
		t.Fatal("不能误授予批准人")
	}
	if s.approvals.IsAllowedFor(securityEvent(3, 8, ""), "review_action", RiskHigh) {
		t.Fatal("授权不能跨群")
	}
}

func TestCriticalPrivateApprovalAndAmbiguousGroupRequests(t *testing.T) {
	s := securityFixture(t)
	id, result := requestApproval(t, s, securityEvent(3, 0, ""), RiskCritical)
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(1, 0, "同意"), id)) {
		t.Fatal("主人应能以编号批准私聊请求")
	}
	if res := <-result; !res.Approved {
		t.Fatal(res.Reason)
	}
	// 消费上一条成功提示。
	<-s.bot.Sender.(*securitySender).sent
	id1, r1 := requestApproval(t, s, securityEvent(3, 7, ""), RiskHigh)
	id2, r2 := requestApproval(t, s, securityEvent(4, 7, ""), RiskHigh)
	if s.approvals.Resolve(securityEvent(1, 7, "同意")) {
		t.Fatal("应提示指定编号")
	}
	if s.approvals.Debug() != 2 {
		t.Fatal("模糊回复不能批准任意请求")
	}
	s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "同意"), id1))
	s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "拒绝"), id2))
	if !(<-r1).Approved || (<-r2).Approved {
		t.Fatal("编号审批结果错误")
	}
}

func TestGrantRequiresCurrentApproverAuthority(t *testing.T) {
	s := securityFixture(t)
	ev := securityEvent(3, 7, "")
	if err := s.allow.Add("group", 7, 3, "review_action"); err != nil {
		t.Fatal(err)
	}
	if s.approvals.IsAllowedFor(ev, "review_action", RiskHigh) {
		t.Fatal("旧式无批准人记录不能免审批")
	}
	if err := s.allow.Grant("group", 7, 3, "review_action", 2, int(RiskHigh)); err != nil {
		t.Fatal(err)
	}
	if !s.approvals.IsAllowedFor(ev, "review_action", RiskHigh) {
		t.Fatal("有效管理员授权应通过")
	}
	s.cfg.Bot.AdminIDs = nil
	if s.approvals.IsAllowedFor(ev, "review_action", RiskHigh) {
		t.Fatal("批准人权限撤销后授权应失效")
	}
}

func TestExecutorValidatesAndIsolatesTools(t *testing.T) {
	s := securityFixture(t)
	ctx := core.WithEvent(context.Background(), securityEvent(3, 7, ""))
	tc := ToolCall{}
	tc.Function.Name = "send_group_message"
	for _, raw := range []string{"{", "null", "[]", "{}", `{"group_id":"wrong","message":"x"}`, `{"group_id":7.5,"message":"x"}`} {
		tc.Function.Arguments = raw
		if got := s.executeTool(ctx, tc); !strings.Contains(got, "参数无效") {
			t.Fatalf("%s: %s", raw, got)
		}
	}
	calls := 0
	tool := NewTool("safe_test", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) { calls++; return "ok", nil })
	tool.Risk = RiskLow
	s.AddTool(tool)
	tc.Function.Name = "safe_test"
	tc.Function.Arguments = "{}"
	if got := s.executeTool(ctx, tc); got != "ok" || calls != 1 {
		t.Fatalf("低风险不应要求审批: %s", got)
	}
	if got := s.executeTool(context.Background(), tc); !strings.Contains(got, "无权") {
		t.Fatalf("无事件不得执行: %s", got)
	}
	panicTool := NewTool("panic_test", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) { panic("test panic") })
	panicTool.Risk = RiskLow
	s.AddTool(panicTool)
	tc.Function.Name = "panic_test"
	if got := s.executeTool(ctx, tc); !strings.Contains(got, "已隔离") {
		t.Fatal(got)
	}
	if riskForTool(Tool{Risk: RiskLow}) != RiskLow || riskForTool(Tool{}) != RiskMedium {
		t.Fatal("风险零值映射错误")
	}
	for _, name := range []string{"group_ban", "group_kick", "group_set_card", "group_set_name", "run_command", "opencode_task"} {
		if riskForTool(Tool{Function: ToolFunction{Name: name}, Risk: RiskLow}) < RiskHigh {
			t.Fatal("危险工具风险不能降低: " + name)
		}
	}
}

func TestAgentHighRiskToolStillRequiresApprovalForPrivilegedRequester(t *testing.T) {
	for _, uid := range []int64{1, 2} {
		t.Run(fmtID(uid), func(t *testing.T) {
			s := securityFixture(t)
			effects := make(chan struct{}, 1)
			tool := NewTool("group_ban", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) {
				effects <- struct{}{}
				return "done", nil
			})
			tool.Risk = RiskLow // The execution boundary must enforce the hard minimum.
			s.ReplaceToolsByPrefix("group_ban", []Tool{tool})
			ctx, cancel := context.WithCancel(core.WithEvent(context.Background(), securityEvent(uid, 7, "")))
			defer cancel()
			tc := ToolCall{}
			tc.Function.Name, tc.Function.Arguments = "group_ban", "{}"
			done := make(chan string, 1)
			go func() { done <- s.executeTool(ctx, tc) }()
			select {
			case prompt := <-s.bot.Sender.(*securitySender).sent:
				if !strings.Contains(prompt, "安全审核") {
					t.Fatal("missing Agent safety review", prompt)
				}
			case result := <-done:
				t.Fatal("Agent operation returned before approval", result)
			case <-time.After(time.Second):
				t.Fatal("privileged Agent action did not request approval")
			}
			select {
			case <-effects:
				t.Fatal("Agent action executed before approval")
			default:
			}
			s.approvals.mu.Lock()
			var nonce string
			for _, req := range s.approvals.pendings {
				nonce = req.ID
			}
			s.approvals.mu.Unlock()
			if nonce == "" || !s.approvals.Resolve(quoteApproval(s, securityEvent(uid, 7, "同意"), nonce)) {
				t.Fatal("Agent approval not resolved")
			}
			select {
			case result := <-done:
				if result != "done" || len(effects) != 1 {
					t.Fatal("approved Agent operation not executed once", result)
				}
			case <-time.After(time.Second):
				t.Fatal("approved Agent operation blocked")
			}
		})
	}
}

func TestCompactionUpdatesSnapshotAndPreservesConcurrentAppend(t *testing.T) {
	s := securityFixture(t)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) { completeText(w, "new-summary") })
	ses, err := s.session.Create("private", 0, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err = s.session.Append(ses.ID, []session.Message{{Role: "user", Content: "input"}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	ses, _ = s.session.Get(ses.ID)
	if err = s.Compact(context.Background(), ses); err != nil {
		t.Fatal(err)
	}
	if len(ses.Messages) != 2 || ses.Summary != "new-summary" {
		t.Fatalf("旧快照未刷新: %+v", ses)
	}
	if err = s.Compact(context.Background(), ses); err != nil {
		t.Fatal(err)
	}
	if ses.Summary != "new-summary" {
		t.Fatal("摘要不得追加")
	}
	s.handleClearSession(s.bot, securityEvent(3, 0, ""))
	cleared, _ := s.session.Get(ses.ID)
	if cleared.Summary != "" || len(cleared.Messages) != 0 {
		t.Fatal("清空必须包含摘要")
	}
	if err = s.session.Append(ses.ID, []session.Message{{Role: "user", Content: "old"}}, 0); err != nil {
		t.Fatal(err)
	}
	ses, _ = s.session.Get(ses.ID)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		if err := s.session.Append(ses.ID, []session.Message{{Role: "user", Content: "concurrent"}}, 0); err != nil {
			t.Error(err)
		}
		completeText(w, "summary")
	})
	// 第二次安装更新 provider 地址。
	if err = s.summarizeAndStore(context.Background(), ses, ses.Messages, nil); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("应检测冲突: %v", err)
	}
	after, _ := s.session.Get(ses.ID)
	if len(after.Messages) != 2 || after.Messages[1].Content != "concurrent" {
		t.Fatal("并发消息丢失")
	}
}

func TestContextFitsBeforeExecutionAndUsesSelectedModel(t *testing.T) {
	s := securityFixture(t)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) { completeText(w, "summary") })
	ses, _ := s.session.Create("private", 0, 3, "")
	for i := 0; i < 8; i++ {
		if err := s.session.Append(ses.ID, []session.Message{{Role: "user", Content: strings.Repeat("中", 6000)}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	ses, _ = s.session.Get(ses.ID)
	if err := s.maybeAutoCompact(context.Background(), ses); err != nil {
		t.Fatal(err)
	}
	messages, err := s.fitContext(context.Background(), ses, func() []Message { return append(s.buildContextMessages(ses, ""), NewTextMessage("user", "next")) }, s.Tools())
	if err != nil {
		t.Fatal(err)
	}
	if estimateRequestTokens(messages, s.Tools()) > s.inputBudget(context.Background()) {
		t.Fatal("压缩后仍超预算")
	}
	ctx := WithModelOverride(context.Background(), "gpt-4")
	ctx = s.client.pin(ctx, s.models.ContextWindowFor("gpt-4"))
	if s.inputBudget(ctx) >= 8192 {
		t.Fatal("应使用覆盖模型的小窗口")
	}
	tool := NewTool("large", strings.Repeat("中", 10000), nil, nil, nil)
	if estimateRequestTokens(nil, []Tool{tool}) < 10000 {
		t.Fatal("必须计入工具 Schema")
	}
}

func TestToolEffectIsRecordedWhenModelFailsAndMessageIsDeduplicated(t *testing.T) {
	s := securityFixture(t)
	calls, effects := 0, 0
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": map[string]interface{}{"role": "assistant", "tool_calls": []interface{}{map[string]interface{}{"id": "c1", "type": "function", "function": map[string]interface{}{"name": "effect", "arguments": "{}"}}}}}}})
		} else {
			http.Error(w, "test failure", http.StatusServiceUnavailable)
		}
	})
	tool := NewTool("effect", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) {
		effects++
		return "effect-completed", nil
	})
	tool.Risk = RiskLow
	s.AddTool(tool)
	ev := securityEvent(3, 0, "run")
	ev.MessageID = 100
	if !s.Handle(context.Background(), s.bot, ev) {
		t.Fatal("not handled")
	}
	ses, _ := s.session.GetCurrent("private", 0, 3, "")
	var history string
	for _, m := range ses.Messages {
		history += m.Content
	}
	if effects != 1 || !strings.Contains(history, "effect-completed") || !strings.Contains(history, "已中断") {
		t.Fatalf("副作用没有留存: %s", history)
	}
	s.Handle(context.Background(), s.bot, ev)
	if effects != 1 || calls != 2 {
		t.Fatalf("同一消息不能重复执行 effects=%d calls=%d", effects, calls)
	}
}

func TestTaskCancellationChecksScope(t *testing.T) {
	s := securityFixture(t)
	st, err := sched.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.sched = st
	id, err := st.Add(&sched.Task{Type: "once", Scope: "private", UserID: 3, Enabled: true, TargetAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]interface{}{"task_id": float64(id)}
	got, _ := s.cancelTaskCallback(core.WithEvent(context.Background(), securityEvent(4, 0, "")), args)
	if !strings.Contains(got, "取消失败") {
		t.Fatal("不能取消他人任务: " + got)
	}
	got, _ = s.cancelTaskCallback(core.WithEvent(context.Background(), securityEvent(3, 0, "")), args)
	if !strings.Contains(got, "已取消") {
		t.Fatal(got)
	}
}

func TestVisionUsesOwnModelAndPinnedProvider(t *testing.T) {
	s := securityFixture(t)
	s.cfg.Memory.VisionModel = "vision-model"
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "vision-model" {
			t.Errorf("识图模型被主模型覆盖: %s", request.Model)
		}
		completeText(w, "图片描述")
	})
	ctx := s.client.pin(WithModelOverride(context.Background(), "main-model"), 64000)
	if got := s.describeImages(ctx, []string{"data:image/png;base64,AA=="}); got != "图片描述" {
		t.Fatal(got)
	}
	if modelFromCtx(ctx) != "main-model" {
		t.Fatal("识图不能改动主回合模型")
	}
}

func TestTurnKeepsProviderModelAndWindowAfterSwitch(t *testing.T) {
	s := securityFixture(t)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "test-model" {
			t.Errorf("回合模型变化: %s", request.Model)
		}
		completeText(w, "pinned")
	})
	ctx, err := s.models.pinForLevel(context.Background(), levelMember)
	if err != nil {
		t.Fatal(err)
	}
	budget := s.inputBudget(ctx)
	d, err := s.models.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	d.Providers = append(d.Providers, provider.Provider{Name: "other", BaseURL: "http://127.0.0.1:1", Model: "gpt-4"})
	d.Current = "other"
	if err = s.models.store.Save(d); err != nil {
		t.Fatal(err)
	}
	if s.inputBudget(ctx) != budget {
		t.Fatal("切换provider改变了进行中回合预算")
	}
	msg, err := s.client.Complete(ctx, []Message{NewTextMessage("user", "test")}, nil)
	if err != nil || msg.TextContent() != "pinned" {
		t.Fatalf("切换provider改变了回合连接: %v", err)
	}
}

func TestStepIDReusesResultAndRefusesUncertainReplay(t *testing.T) {
	s := securityFixture(t)
	ses, err := s.session.Create("private", 0, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.session.BeginRun(ses.ID, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(core.WithEvent(context.Background(), securityEvent(3, 0, "")), runContextKey{}, run)
	calls := 0
	tool := NewTool("effect_once", "test", nil, nil, func(context.Context, map[string]interface{}) (string, error) { calls++; return "done", nil })
	tool.Risk = RiskLow
	s.AddTool(tool)
	tc := ToolCall{ID: "one"}
	tc.Function.Name, tc.Function.Arguments = "effect_once", "{}"
	for i := 0; i < 2; i++ {
		if got := s.executeTool(ctx, tc); got != "done" {
			t.Fatal(got)
		}
	}
	step, _, err := s.session.BeginStep(run.ID, "uncertain", "effect_once", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.session.UpdateStep(step.ID, "executing", ""); err != nil {
		t.Fatal(err)
	}
	tc.ID = "uncertain"
	if got := s.executeTool(ctx, tc); !strings.Contains(got, "不能重复执行") {
		t.Fatal(got)
	}
	if calls != 1 {
		t.Fatalf("副作用执行次数=%d", calls)
	}
}

func TestAskAnswersBypassConversationLockAndKeepQuestionNumbers(t *testing.T) {
	s := securityFixture(t)
	ev := securityEvent(3, 0, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := s.lockConversation(ctx, ev)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	result := make(chan *AskResult, 1)
	go func() {
		r, _ := s.asker.Ask(ctx, ev, []Question{{ID: "a", Text: "first"}, {ID: "b", Text: "second"}})
		result <- r
	}()
	select {
	case <-s.bot.Sender.(*securitySender).sent:
	case <-ctx.Done():
		t.Fatal("未提问")
	}
	// Handle 仍持有同一会话的锁时，控制回复必须先处理，否则问答互锁。
	if !s.Handle(ctx, s.bot, securityEvent(3, 0, "2. beta")) {
		t.Fatal("未接收部分回答")
	}
	if !s.Handle(ctx, s.bot, securityEvent(3, 0, "1. alpha")) {
		t.Fatal("未接收补充回答")
	}
	select {
	case got := <-result:
		if got == nil || !got.Completed || got.Answers["a"] != "alpha" || got.Answers["b"] != "beta" {
			t.Fatalf("回答错位: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("回答被会话锁阻塞")
	}
}

func TestRequesterCanCancelOwnHighRiskRequest(t *testing.T) {
	s := securityFixture(t)
	id, result := requestApproval(t, s, securityEvent(3, 7, ""), RiskHigh)
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(3, 7, "拒绝"), id)) {
		t.Fatal("发起人应能取消")
	}
	select {
	case got := <-result:
		if got.Approved {
			t.Fatal("取消不能批准")
		}
	case <-time.After(time.Second):
		t.Fatal("取消未唤醒")
	}
}

func TestRawSchemaKeepsNestedConstraints(t *testing.T) {
	tool := NewTool("nested", "test", nil, nil, nil)
	tool.Function.Parameters.Raw = json.RawMessage(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","items":{"type":"object","required":["name"],"properties":{"name":{"type":"string","enum":["ok"]}}}}}}`)
	for _, raw := range []string{`{"items":[{}]}`, `{"items":[{"name":"other"}]}`, `{"items":[1]}`} {
		if _, err := validateArguments(tool, raw); err == nil {
			t.Fatalf("嵌套约束丢失: %s", raw)
		}
	}
	if _, err := validateArguments(tool, `{"items":[{"name":"ok"}]}`); err != nil {
		t.Fatal(err)
	}
}
