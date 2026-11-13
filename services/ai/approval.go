package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/allow"
)

// RiskLevel 工具风险等级
type RiskLevel int

const (
	RiskUnspecified RiskLevel = iota // 零值保守回退到中风险
	RiskLow                          // 低风险, 直接执行
	RiskMedium                       // 中风险, 需发起人确认
	RiskHigh                         // 高风险, 需主人/管理员确认
	RiskCritical                     // 极危, 仅主人可批准
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
	ID        string
	key       string
	ToolName  string
	Args      map[string]interface{}
	Risk      RiskLevel
	Requester int64 // 发起人
	GroupID   int64
	Messages  []approvalMessage
	Channel   chan ApprovalResult
	ExpireAt  time.Time
}

type approvalMessage struct{ ID, GroupID, UserID int64 }

func (p *ApprovalRequest) matchesReply(ev *core.Event) bool {
	for _, msg := range p.Messages {
		if msg.ID != ev.ReplyID() {
			continue
		}
		if ev.IsGroup() && msg.GroupID == ev.GroupID && p.GroupID == ev.GroupID {
			return true
		}
		if ev.IsPrivate() && msg.GroupID == 0 && msg.UserID == ev.UserID && p.GroupID == 0 {
			return true
		}
	}
	return false
}

type ApprovalResult struct {
	Approved bool
	Reason   string
}

// ApprovalManager 管理待审批请求
type ApprovalManager struct {
	mu        sync.Mutex
	pendings  map[string]*ApprovalRequest
	bot       *core.Bot
	allow     *allow.Store
	timeout   time.Duration
	singleUse bool
}

func NewApprovalManager(bot *core.Bot, timeout time.Duration) *ApprovalManager {
	return &ApprovalManager{
		pendings: make(map[string]*ApprovalRequest),
		bot:      bot,
		timeout:  timeout,
	}
}

func (m *ApprovalManager) SetAllowStore(s *allow.Store) { m.allow = s }

// NewSingleUseApprovalManager requires a quoted request and never grants permanent authority.
func NewSingleUseApprovalManager(bot *core.Bot, timeout time.Duration) *ApprovalManager {
	m := NewApprovalManager(bot, timeout)
	m.singleUse = true
	return m
}

func approvalKey(ev *core.Event) string {
	if ev.IsGroup() {
		return "g" + int64ToStr(ev.GroupID) + "_u" + int64ToStr(ev.UserID)
	}
	return "p" + int64ToStr(ev.UserID)
}

// Request 发起审批, 阻塞等待结果
func (m *ApprovalManager) Request(ctx context.Context, ev *core.Event, toolName string, args map[string]interface{}, risk RiskLevel) (*ApprovalResult, error) {
	if ev == nil || ev.UserID <= 0 {
		return nil, errNoEvent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := approvalKey(ev)

	m.mu.Lock()
	for _, pending := range m.pendings {
		if pending.key == key && time.Now().Before(pending.ExpireAt) {
			m.mu.Unlock()
			return &ApprovalResult{Approved: false, Reason: "已有待处理的审批请求, 请先回复同意或拒绝"}, nil
		}
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
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	req.ID = hex.EncodeToString(nonce[:])
	m.pendings[req.ID] = req
	m.mu.Unlock()
	defer m.remove(req.ID)

	// 发送审批请求消息
	argsJSON, _ := json.Marshal(args)
	approver := m.approverHint(req, ev)
	msg := "⚠️ 【安全审核】\n" +
		"请求执行「" + toolName + "」(" + risk.String() + ")\n" +
		"参数: `" + string(argsJSON) + "`\n" +
		"需要 " + approver + " 确认。\n" +
		"请引用本条申请回复「同意」继续，或「拒绝」取消。\n" +
		"永久授权：引用本条申请回复「以后都允许」（授予发起人，仅限本会话范围）。"
	if m.singleUse {
		msg = strings.Split(msg, "永久授权：")[0] + "本操作每次均需引用申请确认，不接受永久授权。"
	}
	send := func(target *core.Event, text string) error {
		// Serialize receipt registration with Resolve so an immediate reply cannot
		// race the API receipt. No other bot operation uses this mutex.
		m.mu.Lock()
		defer m.mu.Unlock()
		id, err := m.bot.SendTracked(ctx, target, text)
		if err != nil {
			return err
		}
		req.Messages = append(req.Messages, approvalMessage{ID: id, GroupID: target.GroupID, UserID: target.UserID})
		return nil
	}
	if err := send(ev, msg); err != nil {
		return nil, err
	}
	if !ev.IsGroup() {
		if risk >= RiskHigh && m.bot.Cfg.Bot.MasterID != int64ToStr(ev.UserID) {
			if owner, err := strconv.ParseInt(m.bot.Cfg.Bot.MasterID, 10, 64); err == nil && owner > 0 {
				_ = send(&core.Event{DetailType: "private", UserID: owner}, "来自用户 "+int64ToStr(ev.UserID)+" 的审批请求:\n"+msg)
			}
		}
	}

	select {
	case res := <-req.Channel:
		return &res, nil
	case <-time.After(m.timeout):
		m.notifyTimeout(ev, toolName)
		return &ApprovalResult{Approved: false, Reason: "审批超时"}, nil
	case <-ctx.Done():
		return &ApprovalResult{Approved: false, Reason: "请求被取消"}, nil
	}
}

// Resolve 处理用户对审批请求的回复; 返回 true 表示消费了该消息
func (m *ApprovalManager) Resolve(ev *core.Event) bool {
	if ev == nil || ev.UserID <= 0 || ev.ReplyID() == 0 {
		return false
	}
	text := strings.TrimSpace(ev.Text())
	approve, reject, always := isApprove(text), isReject(text), isAlwaysAllow(text)
	if !approve && !reject && !always {
		return false
	}
	if m.singleUse && always {
		return false
	}
	m.mu.Lock()
	var req *ApprovalRequest
	for _, p := range m.pendings {
		if time.Now().After(p.ExpireAt) || !p.matchesReply(ev) {
			continue
		}
		req = p
		break
	}
	if req == nil {
		m.mu.Unlock()
		return false
	}
	if _, err := m.canApprove(req, ev); err != nil && !(reject && ev.UserID == req.Requester) {
		m.mu.Unlock()
		m.bot.Reply(ev, "无权批准此操作: "+err.Error())
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
			scope := "private"
			if req.GroupID != 0 {
				scope = "group"
			}
			if err := m.allow.Grant(scope, req.GroupID, req.Requester, req.ToolName, ev.UserID, int(req.Risk)); err != nil {
				res.Reason = "本次已批准，但永久授权保存失败"
			}
		}
	}
	req.Channel <- res
	delete(m.pendings, req.ID)
	m.mu.Unlock()

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
	return m.IsAllowedFor(ev, toolName, RiskHigh)
}

func (m *ApprovalManager) IsAllowedFor(ev *core.Event, toolName string, risk RiskLevel) bool {
	if m.allow == nil {
		return false
	}
	scope, gid := scopeOf(ev)
	grant, err := m.allow.Lookup(scope, gid, ev.UserID, toolName)
	if err != nil || grant.ApprovedBy == 0 || grant.Risk < int(risk) {
		return false
	}
	approver := &core.Event{UserID: grant.ApprovedBy, GroupID: ev.GroupID, DetailType: ev.DetailType}
	_, err = m.canApprove(&ApprovalRequest{Requester: ev.UserID, Risk: risk}, approver)
	return err == nil
}

// scopeOf 返回作用域标识: 群内按群隔离, 私聊按用户隔离
func scopeOf(ev *core.Event) (string, int64) {
	if ev.IsGroup() {
		return "group", ev.GroupID
	}
	return "private", 0
}

func isAlwaysAllow(s string) bool {
	// 注意: 不可包含裸「记住」——用户让 AI 存记忆时会说「记住XXX」,
	// 审批等待期间会被误判为永久授权(提示词注入/误操作风险)。
	for _, w := range []string{"以后都允许", "以后都同意", "永久允许", "总是允许", "一直允许", "记住允许", "记住这个操作", "记住这个权限", "always", "allow always"} {
		if strings.EqualFold(s, w) {
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
	// 高风险只能由主人或配置的管理员批准，发起人身份不授予审批权。
	if isMaster {
		return "主人", nil
	}
	if isAdmin(m.bot, ev.UserID) {
		return "管理员", nil
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
