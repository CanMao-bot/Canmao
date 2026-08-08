// Package groupreq 入群申请管理: bot 是群管理时把入群申请发卡到群里,
// 群主/管理员/主人用 /同意 N /拒绝 N [理由] 审批。
package groupreq

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/perm"
)

// Request 一条待审批的入群申请
type Request struct {
	ID       int       // 群内递增短编号
	UserID   int64     // 申请者 QQ
	Nickname string    // 申请者昵称
	Comment  string    // 验证信息/入群理由(可能含问题答案)
	Flag     string    // 审批标识(set_group_add_request 用)
	Time     time.Time // 收到时间
}

// adminCacheEntry bot 群管理身份缓存
type adminCacheEntry struct {
	isAdmin bool
	at      time.Time
}

const (
	pendingTTL    = 24 * time.Hour    // 待审批申请保留时长
	adminCacheTTL = 10 * time.Minute  // bot 管理身份缓存时长
)

type Manager struct {
	bot        *core.Bot
	perm       *perm.Store
	selfID     int64
	mu         sync.Mutex
	pending    map[int64][]*Request // groupID -> 待审批队列
	adminCache map[int64]adminCacheEntry
}

func New(bot *core.Bot, permStore *perm.Store) *Manager {
	selfID, _ := strconv.ParseInt(bot.Cfg.OneBot.SelfID, 10, 64)
	return &Manager{
		bot:        bot,
		perm:       permStore,
		selfID:     selfID,
		pending:    make(map[int64][]*Request),
		adminCache: make(map[int64]adminCacheEntry),
	}
}

func (m *Manager) Name() string { return "group-request" }

func (m *Manager) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	// 入群申请事件
	if ev.Type == "request" && ev.RequestType == "group" && ev.SubType == "add" {
		return m.handleRequest(bot, ev)
	}
	// 群消息审批命令
	if ev.Type == "message" && ev.IsGroup() {
		return m.handleCommand(bot, ev)
	}
	return false
}

// handleRequest 处理入群申请: 群内发申请卡片
func (m *Manager) handleRequest(bot *core.Bot, ev *core.Event) bool {
	// 群 bot 未启用则静默忽略
	if m.perm != nil && !m.perm.BotEnabled(ev.GroupID) {
		return true
	}
	// bot 在该群须是管理/群主才能审批, 否则静默
	if !m.botIsGroupAdmin(ev.GroupID, ev.SelfID) {
		return true
	}
	reqClient, ok := bot.Sender.(core.GroupRequestClient)
	if !ok {
		return true
	}
	// 取申请者昵称, 失败用 QQ 号
	nickname := strconv.FormatInt(ev.UserID, 10)
	if info, err := reqClient.GetStrangerInfo(ev.UserID); err == nil {
		if nick, _ := info["nickname"].(string); nick != "" {
			nickname = nick
		}
	}

	m.mu.Lock()
	m.cleanupLocked(ev.GroupID)
	req := &Request{
		ID:       m.nextIDLocked(ev.GroupID),
		UserID:   ev.UserID,
		Nickname: nickname,
		Comment:  ev.Comment,
		Flag:     ev.Flag,
		Time:     time.Now(),
	}
	m.pending[ev.GroupID] = append(m.pending[ev.GroupID], req)
	m.mu.Unlock()

	// 群内发申请卡片
	card := m.renderCard(req)
	if err := bot.Sender.SendGroupMsg(ev.GroupID, 0, []core.Segment{core.TextSegment(card)}); err != nil {
		log.Printf("[groupreq] 发送申请卡片失败: %v", err)
	}
	return true
}

// handleCommand 处理群内审批命令
func (m *Manager) handleCommand(bot *core.Bot, ev *core.Event) bool {
	fields := strings.Fields(strings.TrimSpace(ev.Text()))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/入群申请", "/requests":
		bot.Reply(ev, m.renderList(ev.GroupID))
		return true
	case "/同意", "/approve":
		return m.handleApprove(bot, ev, fields, true)
	case "/拒绝", "/reject":
		return m.handleApprove(bot, ev, fields, false)
	}
	return false
}

// handleApprove 同意/拒绝审批
func (m *Manager) handleApprove(bot *core.Bot, ev *core.Event, fields []string, approve bool) bool {
	// 仅主人/群主/管理员可用, 无权限静默
	if !m.canApprove(bot, ev) {
		return true
	}
	if len(fields) < 2 {
		usage := "用法: " + fields[0] + " <编号>"
		if !approve {
			usage += " [理由]"
		}
		bot.Reply(ev, usage)
		return true
	}
	id, err := strconv.Atoi(fields[1])
	if err != nil {
		bot.Reply(ev, "编号格式不对: "+fields[1])
		return true
	}
	req := m.take(ev.GroupID, id)
	if req == nil {
		bot.Reply(ev, fmt.Sprintf("未找到该编号的申请 #%d", id))
		return true
	}
	reqClient, ok := bot.Sender.(core.GroupRequestClient)
	if !ok {
		bot.Reply(ev, "当前连接不支持审批入群申请")
		return true
	}
	reason := ""
	if !approve && len(fields) > 2 {
		reason = strings.Join(fields[2:], " ")
	}
	if err := reqClient.SetGroupAddRequest(req.Flag, approve, reason); err != nil {
		// 审批失败放回队列, 可重试
		m.putBack(ev.GroupID, req)
		bot.Reply(ev, "审批失败: "+err.Error())
		return true
	}
	who := fmt.Sprintf("%s %d", req.Nickname, req.UserID)
	if approve {
		bot.Reply(ev, fmt.Sprintf("✅ 已同意 #%d (%s) 的入群申请", req.ID, who))
	} else {
		bot.Reply(ev, fmt.Sprintf("🚫 已拒绝 #%d (%s) 的入群申请", req.ID, who))
	}
	return true
}

// canApprove 是否可审批: 主人或该群 owner/admin
func (m *Manager) canApprove(bot *core.Bot, ev *core.Event) bool {
	if bot.Cfg.Bot.MasterID == strconv.FormatInt(ev.UserID, 10) {
		return true
	}
	ga, ok := bot.Sender.(core.GroupAdminClient)
	if !ok {
		return false
	}
	info, err := ga.GetGroupMemberInfo(ev.GroupID, ev.UserID)
	if err != nil {
		return false
	}
	role, _ := info["role"].(string)
	return role == "owner" || role == "admin"
}

// botIsGroupAdmin bot 在该群是否为管理/群主(结果缓存 10 分钟)
func (m *Manager) botIsGroupAdmin(groupID, evSelfID int64) bool {
	m.mu.Lock()
	if e, ok := m.adminCache[groupID]; ok && time.Since(e.at) < adminCacheTTL {
		m.mu.Unlock()
		return e.isAdmin
	}
	m.mu.Unlock()

	ga, ok := m.bot.Sender.(core.GroupAdminClient)
	if !ok {
		return false
	}
	selfID := m.selfID
	if selfID == 0 {
		selfID = evSelfID
	}
	info, err := ga.GetGroupMemberInfo(groupID, selfID)
	if err != nil {
		return false
	}
	role, _ := info["role"].(string)
	isAdmin := role == "owner" || role == "admin"

	m.mu.Lock()
	m.adminCache[groupID] = adminCacheEntry{isAdmin: isAdmin, at: time.Now()}
	m.mu.Unlock()
	return isAdmin
}

// take 取出并移除指定编号的申请(顺带清理过期)
func (m *Manager) take(groupID int64, id int) *Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(groupID)
	list := m.pending[groupID]
	for i, r := range list {
		if r.ID == id {
			m.pending[groupID] = append(list[:i], list[i+1:]...)
			return r
		}
	}
	return nil
}

// putBack 审批失败时把申请放回队列尾部
func (m *Manager) putBack(groupID int64, req *Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[groupID] = append(m.pending[groupID], req)
}

// nextIDLocked 生成群内递增短编号(调用方须持锁)
func (m *Manager) nextIDLocked(groupID int64) int {
	max := 0
	for _, r := range m.pending[groupID] {
		if r.ID > max {
			max = r.ID
		}
	}
	return max + 1
}

// cleanupLocked 清理超过 24h 的申请(调用方须持锁)
func (m *Manager) cleanupLocked(groupID int64) {
	list := m.pending[groupID]
	keep := list[:0]
	for _, r := range list {
		if time.Since(r.Time) < pendingTTL {
			keep = append(keep, r)
		}
	}
	m.pending[groupID] = keep
}

// renderCard 渲染申请卡片
func (m *Manager) renderCard(req *Request) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📨 入群申请 #%d\n", req.ID))
	b.WriteString(fmt.Sprintf("QQ: %d  昵称: %s\n", req.UserID, req.Nickname))
	b.WriteString("flag: " + req.Flag + "\n")
	b.WriteString(renderComment(req.Comment))
	b.WriteString(fmt.Sprintf("\n群主/管理员/主人回复: /同意 %d 或 /拒绝 %d [理由]", req.ID, req.ID))
	return b.String()
}

// renderComment 渲染验证信息; 含 "问题：/答案：" 结构时拆开展示
func renderComment(comment string) string {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return "验证信息: (无)"
	}
	if qi := strings.Index(comment, "问题："); qi >= 0 {
		rest := comment[qi+len("问题："):]
		var question, answer string
		if ai := strings.Index(rest, "答案："); ai >= 0 {
			question = strings.TrimSpace(rest[:ai])
			answer = strings.TrimSpace(rest[ai+len("答案："):])
		} else {
			question = strings.TrimSpace(rest)
		}
		out := "问题: " + question
		if answer != "" {
			out += "\n答案: " + answer
		}
		return out
	}
	return "验证信息: " + comment
}

// renderList 渲染待审批列表
func (m *Manager) renderList(groupID int64) string {
	m.mu.Lock()
	m.cleanupLocked(groupID)
	list := make([]*Request, len(m.pending[groupID]))
	copy(list, m.pending[groupID])
	m.mu.Unlock()

	if len(list) == 0 {
		return "暂无待审批申请"
	}
	var b strings.Builder
	b.WriteString("📨 待审批入群申请:\n")
	for _, r := range list {
		b.WriteString(fmt.Sprintf("#%d %s (%d)  %s\n", r.ID, r.Nickname, r.UserID, r.Time.Format("01-02 15:04")))
	}
	b.WriteString("回复 /同意 N 或 /拒绝 N [理由] 审批")
	return b.String()
}
