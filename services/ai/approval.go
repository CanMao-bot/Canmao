package ai

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/allow"
)

// RiskLevel 工具风险等级
type RiskLevel int

const (
	RiskLow     RiskLevel = iota // 低风险, 直接执行
	RiskMedium                   // 中风险, 需发起人确认
	RiskHigh                     // 高风险, 需主人/管理员确认
	RiskCritical                 // 极危, 仅主人可批准
)

func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "低风险"
	case RiskMedium:
		return "中风险"
	case RiskHigh:
		return "高风险"
	case RiskCritical:
		return "极危操作"
	}
	return "未知"
}

// ApprovalRequest 一条待审批请求
type ApprovalRequest struct {
	key      string
	ToolName string
	Args     map[string]interface{}
	Risk     RiskLevel
	Requester int64 // 发起人
	GroupID   int64
	Channel   chan ApprovalResult
	ExpireAt  time.Time
}

type ApprovalResult struct {
	Approved bool
	Reason   string
}

// ApprovalManager 管理待审批请求
type ApprovalManager struct {
	mu       sync.Mutex
	pendings map[string]*ApprovalRequest
	bot      *core.Bot
	allow    *allow.Store
	timeout  time.Duration
}

func NewApprovalManager(bot *core.Bot, timeout time.Duration) *ApprovalManager {
	return &ApprovalManager{
		pendings: make(map[string]*ApprovalRequest),
		bot:      bot,
		timeout:  timeout,
	}
}

func (m *ApprovalManager) SetAllowStore(s *allow.Store) { m.allow = s }

func approvalKey(ev *core.Event) string {
	if ev.IsGroup() {
		return "g" + int64ToStr(ev.GroupID) + "_u" + int64ToStr(ev.UserID)
	}
	return "p" + int64ToStr(ev.UserID)
}

// Request 发起审批, 阻塞等待结果
func (m *ApprovalManager) Request(ctx context.Context, ev *core.Event, toolName string, args map[string]interface{}, risk RiskLevel) (*ApprovalResult, error) {
	key := approvalKey(ev)

	m.mu.Lock()
	if _, exists := m.pendings[key]; exists {
		m.mu.Unlock()
		return &ApprovalResult{Approved: false, Reason: "已有待处理的审批请求, 请先回复同意或拒绝"}, nil
	}
	req := &ApprovalRequest{
		key:       key,
		ToolName:  toolName,
		Args:      args,
		Risk:      risk,
		Requester: ev.UserID,
		GroupID:   ev.GroupID,
		Channel:   make(chan ApprovalResult, 1),
		ExpireAt:  time.Now().Add(m.timeout),
	}
	m.pendings[key] = req
	m.mu.Unlock()

	// 发送审批请求消息
	argsJSON, _ := json.Marshal(args)
	approver := m.approverHint(req, ev)
	msg := "⚠️ 【安全审核】\n" +
		"AI 请求执行「" + toolName + "」(" + risk.String() + ")\n" +
		"参数: `" + string(argsJSON) + "`\n" +
		"需要 " + approver + " 确认。\n" +
		"回复「同意」继续, 「拒绝」取消, 「以后都允许」永久授权(本会话对同类操作免审批)。"

	if ev.IsGroup() {
		m.bot.Sender.SendGroupMsg(ev.GroupID, 0, []core.Segment{core.TextSegment(msg)})
	} else {
		m.bot.Sender.SendPrivateMsg(ev.UserID, []core.Segment{core.TextSegment(msg)})
	}

	select {
	case res := <-req.Channel:
		m.remove(key)
		return &res, nil
	case <-time.After(m.timeout):
		m.remove(key)
		m.notifyTimeout(ev, toolName)
		return &ApprovalResult{Approved: false, Reason: "审批超时"}, nil
	case <-ctx.Done():
		m.remove(key)
		return &ApprovalResult{Approved: false, Reason: "请求被取消"}, nil
	}
}

// Resolve 处理用户对审批请求的回复; 返回 true 表示消费了该消息
func (m *ApprovalManager) Resolve(ev *core.Event) bool {
	key := approvalKey(ev)
	m.mu.Lock()
	req, ok := m.pendings[key]
	m.mu.Unlock()
	if !ok {
		return false
	}
	if time.Now().After(req.ExpireAt) {
		m.remove(key)
		return false
	}

	text := strings.TrimSpace(ev.Text())
	approve := isApprove(text)
	reject := isReject(text)
	always := isAlwaysAllow(text)
	if !approve && !reject && !always {
		return false
	}

	// 权限校验: 谁能批准
	if _, err := m.canApprove(req, ev); err != nil {
		// 无权限批准, 立即结束审批(视为拒绝), 并提示
		m.bot.Reply(ev, "无权批准此操作: "+err.Error())
		res := ApprovalResult{Approved: false, Reason: "无权批准: " + err.Error()}
		req.Channel <- res
		m.remove(key)
		return true
	}

	res := ApprovalResult{Approved: approve}
	if reject {
		res.Reason = "用户拒绝"
	}
	if always {
		// 记录永久允许
		res.Approved = true
		res.Reason = "已永久允许"
		if m.allow != nil {
			scope, gid := scopeOf(ev)
			if err := m.allow.Add(scope, gid, ev.UserID, req.ToolName); err != nil {
				m.bot.Reply(ev, "⚠️ 记录永久允许失败: "+err.Error())
			}
		}
	}
	req.Channel <- res
	m.remove(key)

	if approve {
		m.bot.Reply(ev, "✅ 已批准, 正在执行...")
	} else if always {
		m.bot.Reply(ev, "✅ 已批准并记住「以后都允许」, 该操作后续免审批")
	} else {
		m.bot.Reply(ev, "❌ 已拒绝该操作")
	}
	return true
}

// IsAllowed 判断工具在当前作用域是否已永久免审批
func (m *ApprovalManager) IsAllowed(ev *core.Event, toolName string) bool {
	if m.allow == nil {
		return false
	}
	scope, gid := scopeOf(ev)
	return m.allow.IsAllowed(scope, gid, ev.UserID, toolName)
}

// scopeOf 返回作用域标识: 群内按群隔离, 私聊按用户隔离
func scopeOf(ev *core.Event) (string, int64) {
	if ev.IsGroup() {
		return "group", ev.GroupID
	}
	return "private", 0
}

func isAlwaysAllow(s string) bool {
	for _, w := range []string{"以后都允许", "以后都同意", "永久允许", "总是允许", "一直允许", "always", "allow always", "记住"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func (m *ApprovalManager) remove(key string) {
	m.mu.Lock()
	delete(m.pendings, key)
	m.mu.Unlock()
}

func (m *ApprovalManager) approverHint(req *ApprovalRequest, ev *core.Event) string {
	switch req.Risk {
	case RiskLow, RiskMedium:
		return "发起人"
	case RiskHigh:
		return "主人或管理员"
	default:
		return "仅主人"
	}
}

// canApprove 判断当前回复者是否有权批准
func (m *ApprovalManager) canApprove(req *ApprovalRequest, ev *core.Event) (string, error) {
	isMaster := m.bot.Cfg.Bot.MasterID == int64ToStr(ev.UserID)
	// 极危: 仅主人
	if req.Risk >= RiskCritical && !isMaster {
		return "", errNoPermission("该操作仅主人可批准")
	}
	// 中风险: 发起人即可
	if req.Risk <= RiskMedium {
		if ev.UserID == req.Requester || isMaster {
			return "发起人", nil
		}
		return "", errNoPermission("仅发起人可批准")
	}
	// 高风险: 主人/管理员/发起人
	if isMaster {
		return "主人", nil
	}
	if isAdmin(m.bot, ev.UserID) {
		return "管理员", nil
	}
	if ev.UserID == req.Requester {
		return "发起人", nil
	}
	return "", errNoPermission("仅主人/管理员可批准")
}

type permError string

func (e permError) Error() string { return string(e) }

func errNoPermission(msg string) error { return permError(msg) }

func isApprove(s string) bool {
	for _, w := range []string{"同意", "批准", "确认", "可以", "好的", "好", "yes", "y", "ok", "approve", "sure"} {
		if strings.EqualFold(s, w) {
			return true
		}
	}
	return false
}

func isReject(s string) bool {
	for _, w := range []string{"拒绝", "不同意", "取消", "不批准", "不行", "不要", "no", "n", "reject", "deny", "cancel"} {
		if strings.EqualFold(s, w) {
			return true
		}
	}
	return false
}

func (m *ApprovalManager) notifyTimeout(ev *core.Event, tool string) {
	msg := "⏰ 审批超时, 已取消「" + tool + "」操作"
	if ev.IsGroup() {
		m.bot.Sender.SendGroupMsg(ev.GroupID, 0, []core.Segment{core.TextSegment(msg)})
	} else {
		m.bot.Sender.SendPrivateMsg(ev.UserID, []core.Segment{core.TextSegment(msg)})
	}
}

func (m *ApprovalManager) Debug() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pendings)
}

var _ = log.Printf
