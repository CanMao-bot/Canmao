package ai

import (
	"testing"

	"gobot/core"
)

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

	// 高风险: 主人/管理员/发起人
	req = mkReq(RiskHigh, 20000)
	for _, uid := range []int64{10000, 20000, 30000} {
		if _, err := m.canApprove(req, mkEv(uid)); err != nil {
			t.Errorf("高风险 uid=%d 不可批准: %v", uid, err)
		}
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
