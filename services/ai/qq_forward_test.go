package ai

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gobot/core"
)

type recordedForward struct {
	groupID, userID int64
	nodes           []core.ForwardNode
}

type forwardTestSender struct {
	*securitySender
	forwards chan recordedForward
	calls    atomic.Int32
	id       int64
	err      error
}

func (s *forwardTestSender) SendForwardWithReceipt(ctx context.Context, gid, uid int64, nodes []core.ForwardNode) (int64, error) {
	s.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.err != nil {
		return 0, s.err
	}
	s.forwards <- recordedForward{gid, uid, nodes}
	return s.id, nil
}

func installForwardSender(s *Service) *forwardTestSender {
	s.cfg.Bot.Name = "测试机器人"
	s.cfg.OneBot.SelfID = "9"
	sender := &forwardTestSender{securitySender: s.bot.Sender.(*securitySender), forwards: make(chan recordedForward, 8), id: 81}
	s.bot.SetSender(sender)
	return sender
}

func TestQQForwardOutputTargetsNodesAndDeduplication(t *testing.T) {
	s := groupPolicyFixture(t)
	sender := installForwardSender(s)
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai on"))
	s.cfg.Bot.LongMessageForward = false // Explicit calls do not depend on automatic forwarding.
	for _, gid := range []int64{0, 10} {
		ev := securityEvent(3, gid, "merge")
		ctx := core.WithEvent(context.Background(), ev)
		ctx = context.WithValue(ctx, qqOutputKey{}, &qqOutputState{sent: map[qqMessageTarget]map[string]bool{}})
		tc := callFor("qq_send_forward_message", `{"messages":["第一段","第二段 [CQ:at,qq=all]"]}`, "forward")
		result := s.executeTool(ctx, tc)
		if !strings.Contains(result, `"success":true`) || !strings.Contains(result, `"message_id":81`) || !strings.Contains(result, `"node_count":2`) {
			t.Fatal(result)
		}
		received := <-sender.forwards
		if received.groupID != gid || (gid == 0 && received.userID != 3) || len(received.nodes) != 2 {
			t.Fatalf("错误目标或节点: %+v", received)
		}
		for _, node := range received.nodes {
			if node.Uin != "9" || node.UserID != 9 || node.Nickname != "测试机器人" || len(node.Content) != 1 || node.Content[0].Type != "text" {
				t.Fatalf("节点身份或内容错误: %+v", node)
			}
		}
		if received.nodes[1].Content[0].Data["text"] != "第二段 [CQ:at,qq=all]" {
			t.Fatal("CQ 字符串被执行")
		}
		if result = s.executeTool(ctx, tc); !strings.Contains(result, `"duplicate":true`) {
			t.Fatal(result)
		}
		if !qqAlreadySent(ctx, ev, "第一段\n\n第二段 [CQ:at,qq=all]") {
			t.Fatal("最终正文未识别为已发送")
		}
	}
	if sender.calls.Load() != 2 {
		t.Fatal("重复发送合并转发")
	}
	tools := s.requestTools(context.WithValue(context.Background(), toolSetKey{}, []Tool{}))
	found := false
	for _, tool := range tools {
		if tool.Function.Name == "qq_send_forward_message" {
			found = true
		}
	}
	if !found {
		t.Fatal("受限工具集丢失合并转发工具")
	}
}

func TestQQForwardRejectsInvalidInputIdentityAndUnavailableTransport(t *testing.T) {
	s := securityFixture(t)
	sender := installForwardSender(s)
	ctx := core.WithEvent(context.Background(), securityEvent(3, 0, "merge"))
	for _, args := range []string{
		`{}`, `{"messages":null}`, `{"messages":[]}`, `{"messages":[1]}`, `{"messages":[""]}`, `{"messages":["  "]}`,
		`{"messages":["ok"],"user_id":4}`, `{"messages":["ok"],"group_id":10}`, `{"messages":["ok"],"group_id":0}`, `{"messages":["ok"],"user_id":3,"group_id":10}`,
		`{"messages":["ok"],"user_id":9223372036854775808}`,
	} {
		result := s.executeTool(ctx, callFor("qq_send_forward_message", args, "invalid"))
		if strings.Contains(result, `"success":true`) {
			t.Fatalf("无效参数发送成功: %s", args)
		}
	}
	for _, parts := range [][]interface{}{
		{strings.Repeat("字", 2501)}, make([]interface{}, 51),
	} {
		if _, err := s.sendQQForward(ctx, map[string]interface{}{"messages": parts}); err == nil {
			t.Fatal("未限制节点大小或数量")
		}
	}
	large := make([]interface{}, 21)
	for i := range large {
		large[i] = strings.Repeat("字", 2500)
	}
	if _, err := s.sendQQForward(ctx, map[string]interface{}{"messages": large}); err == nil {
		t.Fatal("未限制正文总长")
	}
	if sender.calls.Load() != 0 {
		t.Fatal("无效调用触达发送接口")
	}
	s.cfg.OneBot.SelfID = ""
	if result := s.executeTool(ctx, callFor("qq_send_forward_message", `{"messages":["ok"]}`, "identity")); !strings.Contains(result, "身份") {
		t.Fatal(result)
	}
	s.cfg.OneBot.SelfID = "9"
	s.bot.SetSender(sender.securitySender)
	if result := s.executeTool(ctx, callFor("qq_send_forward_message", `{"messages":["ok"]}`, "unsupported")); !strings.Contains(result, "不支持") {
		t.Fatal(result)
	}
}

func TestQQForwardCrossConversationApprovalAndGroupRecheck(t *testing.T) {
	s := groupPolicyFixture(t)
	sender := installForwardSender(s)
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai on"))
	for {
		select {
		case <-sender.sent:
			continue
		default:
		}
		break
	}
	ctx, cancel := context.WithCancel(core.WithEvent(context.Background(), securityEvent(1, 0, "merge")))
	defer cancel()
	done := make(chan string, 1)
	go func() {
		done <- s.executeTool(ctx, callFor("qq_send_forward_message", `{"messages":["目标内容"],"group_id":10}`, "approval"))
	}()
	select {
	case card := <-sender.sent:
		if !strings.Contains(card, "qq_send_forward_message/group/10") {
			t.Fatal(card)
		}
	case <-time.After(time.Second):
		t.Fatal("跨会话未请求审批")
	}
	if sender.calls.Load() != 0 {
		t.Fatal("审批前发送")
	}
	s.approvals.mu.Lock()
	var approvalID string
	for id := range s.approvals.pendings {
		approvalID = id
	}
	s.approvals.mu.Unlock()
	s.HandleControl(context.Background(), s.bot, securityEvent(1, 10, "/ai off"))
	s.approvals.Resolve(quoteApproval(s, securityEvent(1, 0, "同意"), approvalID))
	select {
	case result := <-done:
		if !strings.Contains(result, "取消") {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("审批完成后未退出")
	}
	if sender.calls.Load() != 0 {
		t.Fatal("群 AI 关闭后发送")
	}
}

func TestQQForwardFailuresCancellationAndExplicitOutput(t *testing.T) {
	s := securityFixture(t)
	sender := installForwardSender(s)
	ctx := core.WithEvent(context.Background(), securityEvent(3, 0, "merge"))
	tc := callFor("qq_send_forward_message", `{"messages":["first","second"]}`, "forward")
	for _, failure := range []error{errors.New("connection closed"), nil} {
		sender.err = failure
		sender.id = 0
		if result := s.executeTool(ctx, tc); strings.Contains(result, `"success":true`) || !strings.Contains(result, "失败") {
			t.Fatal(result)
		}
	}
	sender.id = 81
	before := sender.calls.Load()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if result := s.executeTool(canceled, tc); !strings.Contains(result, "取消") {
		t.Fatal(result)
	}
	if sender.calls.Load() != before {
		t.Fatal("取消后调用接口")
	}
	s.publishToolProgress(ctx, &Message{Role: "assistant", Content: "first\n\nsecond", ToolCalls: []ToolCall{tc}})
	select {
	case msg := <-sender.sent:
		t.Fatalf("合并工具前重复发送正文: %q", msg)
	default:
	}
}
