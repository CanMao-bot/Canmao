package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"gobot/core"
)

func TestApprovalRequiresActualQuotedRequestAndDoesNotDisplayID(t *testing.T) {
	s := securityFixture(t)
	id, done := requestApproval(t, s, securityEvent(3, 7, ""), RiskHigh)
	for _, ev := range []*core.Event{securityEvent(1, 7, "同意"), securityEvent(1, 7, "同意 "+id), securityEvent(1, 7, "以后都允许")} {
		if s.approvals.Resolve(ev) {
			t.Fatal("unquoted approval consumed")
		}
	}
	wrong := securityEvent(1, 7, "同意")
	wrong.Message = append([]core.Segment{{Type: "reply", Data: map[string]interface{}{"id": int64(99999)}}}, wrong.Message...)
	if s.approvals.Resolve(wrong) {
		t.Fatal("unrelated quote accepted")
	}
	reply := quoteApproval(s, securityEvent(1, 7, "同意"), id)
	if !s.approvals.Resolve(reply) {
		t.Fatal("quote failed")
	}
	if !(<-done).Approved {
		t.Fatal("quote not approved")
	}
	if s.approvals.Resolve(reply) {
		t.Fatal("quoted approval replayed")
	}
	// The user sees no internal random identifier.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.approvals.Request(ctx, securityEvent(4, 7, ""), "review", map[string]interface{}{}, RiskHigh)
	for {
		select {
		case msg := <-s.bot.Sender.(*securitySender).sent:
			if strings.Contains(msg, "安全审核") {
				if strings.Contains(msg, "安全审核 #") || strings.Contains(msg, "编号") || !strings.Contains(msg, "引用本条申请") {
					t.Fatal(msg)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("no prompt")
		}
	}
}

func TestQuotedApprovalsSelectBetweenConcurrentRequests(t *testing.T) {
	s := securityFixture(t)
	id1, r1 := requestApproval(t, s, securityEvent(3, 7, ""), RiskHigh)
	id2, r2 := requestApproval(t, s, securityEvent(4, 7, ""), RiskHigh)
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "拒绝"), id2)) {
		t.Fatal("second quote not matched")
	}
	if (<-r2).Approved {
		t.Fatal("rejection ignored")
	}
	select {
	case <-r1:
		t.Fatal("another request was resolved")
	default:
	}
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "以后都允许"), id1)) {
		t.Fatal("first quote not matched")
	}
	if !(<-r1).Approved {
		t.Fatal("approval failed")
	}
	if !s.approvals.IsAllowedFor(securityEvent(3, 7, ""), "review_action", RiskHigh) || s.approvals.IsAllowedFor(securityEvent(4, 7, ""), "review_action", RiskHigh) {
		t.Fatal("permanent grant applied to wrong requester")
	}
}

type noReceiptSender struct{ sent chan string }

func (s *noReceiptSender) SendGroupMsg(int64, int64, []core.Segment) error { return nil }
func (s *noReceiptSender) SendPrivateMsg(int64, []core.Segment) error      { return nil }

func TestApprovalFailsClosedWithoutReceipt(t *testing.T) {
	s := securityFixture(t)
	s.bot.SetSender(&noReceiptSender{})
	if _, err := s.approvals.Request(context.Background(), securityEvent(1, 0, ""), "review", nil, RiskHigh); err == nil {
		t.Fatal("missing message receipt was accepted")
	}
	if s.approvals.Debug() != 0 {
		t.Fatal("unreachable pending approval leaked")
	}
}

func TestExpiredQuotedRequestCannotExecute(t *testing.T) {
	s := securityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *ApprovalResult, 1)
	go func() {
		res, _ := s.approvals.Request(ctx, securityEvent(1, 7, ""), "review", nil, RiskHigh)
		done <- res
	}()
	select {
	case <-s.bot.Sender.(*securitySender).sent:
	case <-time.After(time.Second):
		t.Fatal("missing prompt")
	}
	s.approvals.mu.Lock()
	var id string
	for key, req := range s.approvals.pendings {
		id = key
		req.ExpireAt = time.Now().Add(-time.Second)
	}
	s.approvals.mu.Unlock()
	if s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "同意"), id)) {
		t.Fatal("expired quote accepted")
	}
	cancel()
	if result := <-done; result == nil || result.Approved {
		t.Fatal(result)
	}
}

func TestSingleUseQuotedRequestNeverGrantsPermanentAuthority(t *testing.T) {
	s := securityFixture(t)
	s.approvals = NewSingleUseApprovalManager(s.bot, time.Second)
	s.approvals.SetAllowStore(s.allow)
	id, done := requestApproval(t, s, securityEvent(1, 7, ""), RiskHigh)
	if s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "以后都允许"), id)) {
		t.Fatal("single-use granted permanent approval")
	}
	if !s.approvals.Resolve(quoteApproval(s, securityEvent(1, 7, "同意"), id)) || !(<-done).Approved {
		t.Fatal("single-use quote failed")
	}
	if s.approvals.IsAllowedFor(securityEvent(1, 7, ""), "review_action", RiskHigh) {
		t.Fatal("single-use grant persisted")
	}
}

func mkEv(uid int64) *core.Event {
	return &core.Event{Type: "message", DetailType: "private", UserID: uid}
}

// 回归: isAlwaysAllow 不得包含裸「记住」——审批等待期间用户让 AI 存记忆
// 会说「记住XXX」, 曾被 Contains 误判为永久授权。
func TestIsAlwaysAllow(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"以后都允许", true},
		{"以后都同意", true},
		{"永久允许", true},
		{"总是允许", true},
		{"一直允许", true},
		{"always", true},
		{"allow always", true},
		{"记住允许", true},
		{"记住这个操作", true},
		{"记住这个权限", true},
		// 关键回归项: 让 AI 存记忆的话术不得触发授权
		{"记住", false},
		{"记住我说的内容", false},
		{"记住这件事, 别忘了", false},
		{"帮我记住这个", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isAlwaysAllow(c.in); got != c.want {
			t.Errorf("isAlwaysAllow(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsApprove(t *testing.T) {
	yes := []string{"同意", "批准", "确认", "可以", "好的", "好", "yes", "y", "ok", "approve", "sure", "OK"}
	for _, s := range yes {
		if !isApprove(s) {
			t.Errorf("isApprove(%q) = false, want true", s)
		}
	}
	// 全词匹配: 变体不触发
	no := []string{"好的呀", "同意啦", "不太同意", "确认一下?", "好吧"}
	for _, s := range no {
		if isApprove(s) {
			t.Errorf("isApprove(%q) = true, want false", s)
		}
	}
}

func TestIsReject(t *testing.T) {
	yes := []string{"拒绝", "不同意", "取消", "不要", "no", "reject"}
	for _, s := range yes {
		if !isReject(s) {
			t.Errorf("isReject(%q) = false, want true", s)
		}
	}
	no := []string{"不拒绝了", "取消吗?", "不要紧"}
	for _, s := range no {
		if isReject(s) {
			t.Errorf("isReject(%q) = true, want false", s)
		}
	}
}

// 审批权限阶梯: 极危仅主人, 高风险主人/管理员/发起人, 中风险发起人即可
func TestCanApprove(t *testing.T) {
	bot := core.NewBot(&core.Config{})
	bot.Cfg.Bot.MasterID = "10000"
	bot.Cfg.Bot.AdminIDs = []string{"30000"}
	m := NewApprovalManager(bot, 10)

	mkReq := func(risk RiskLevel, requester int64) *ApprovalRequest {
		return &ApprovalRequest{Risk: risk, Requester: requester}
	}

	// 极危: 仅主人
	req := mkReq(RiskCritical, 20000)
	if _, err := m.canApprove(req, mkEv(20000)); err == nil {
		t.Error("极危操作非主人可批准, want 拒绝")
	}
	if _, err := m.canApprove(req, mkEv(10000)); err != nil {
		t.Errorf("极危操作主人不可批准: %v", err)
	}

	// 高风险: 仅主人/管理员，发起人不能自批。
	req = mkReq(RiskHigh, 20000)
	for _, uid := range []int64{10000, 30000} {
		if _, err := m.canApprove(req, mkEv(uid)); err != nil {
			t.Errorf("高风险 uid=%d 不可批准: %v", uid, err)
		}
	}
	if _, err := m.canApprove(req, mkEv(20000)); err == nil {
		t.Fatal("高风险发起人不得自行批准")
	}

	// 中风险: 发起人/主人可, 旁观者不可
	req = mkReq(RiskMedium, 20000)
	if _, err := m.canApprove(req, mkEv(20000)); err != nil {
		t.Errorf("中风险发起人不可批准: %v", err)
	}
	if _, err := m.canApprove(req, mkEv(30000)); err == nil {
		t.Error("中风险旁观者可批准, want 拒绝")
	}
}

// 默认风险: 未显式标注的工具默认中风险(需发起人确认)
func TestRiskForToolDefault(t *testing.T) {
	if riskForTool(Tool{}) != RiskMedium {
		t.Error("默认风险应为 RiskMedium")
	}
	if riskForTool(Tool{Risk: RiskCritical}) != RiskCritical {
		t.Error("显式风险应保留")
	}
}
