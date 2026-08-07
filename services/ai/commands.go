package ai

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gobot/core"
	"gobot/store/provider"
	"gobot/store/session"
)

func (s *Service) handleCommands(ctx context.Context, bot *core.Bot, ev *core.Event, isMaster bool) (handled, done bool) {
	// 统一用 cmdOf 解析首个命令 token, 支持 "at+命令" 场景
	cmd, arg := cmdOf(ev.Text())

	if !ev.IsGroup() {
		// 私聊也支持会话/压缩命令
		switch cmd {
		case "/help", "/帮助":
			return true, s.handleHelp(ctx, bot, ev, isMaster)
		case "/compact", "/压缩":
			return true, s.handleCompact(ctx, bot, ev)
		case "/new", "/新会话":
			return true, s.handleNewSession(bot, ev)
		case "/sessions", "/历史会话":
			return true, s.handleListSessions(bot, ev)
		case "/resume":
			return true, s.handleResumeSession(bot, ev, arg)
		case "/rename":
			return true, s.handleRenameSession(bot, ev, arg)
		case "/delete":
			return true, s.handleDeleteSession(bot, ev, arg)
		case "/clear", "/清除":
			return true, s.handleClearSession(bot, ev)
		case "/models", "/模型":
			return true, s.handleListModels(ctx, bot, ev, isMaster)
		case "/model":
			return true, s.handleSwitchModel(ctx, bot, ev, arg, isMaster)
		case "/provider", "/providers", "/提供商":
			return true, s.handleProvider(ctx, bot, ev, arg, isMaster)
		case "/sched", "/定时":
			return true, s.handleSchedCmd(ctx, bot, ev, arg, isMaster)
		}
		return false, false
	}

	switch cmd {
	case "/help", "/帮助":
		return true, s.handleHelp(ctx, bot, ev, isMaster)
	case "/sched", "/定时":
		return true, s.handleSchedCmd(ctx, bot, ev, arg, isMaster)
	case "/bot":
		role, _ := s.perm.GetGroupRole(ev.GroupID, ev.UserID)
		if isMaster || role == "admin" || isAdmin(bot, ev.UserID) {
			if arg == "on" || arg == "开" {
				s.perm.SetGroupBot(ev.GroupID, true)
				bot.Reply(ev, "已开启本群 bot")
				return true, true
			}
			if arg == "off" || arg == "关" {
				s.perm.SetGroupBot(ev.GroupID, false)
				bot.Reply(ev, "已关闭本群 bot")
				return true, true
			}
			bot.Reply(ev, "用法: /bot on 或 /bot off")
			return true, true
		}
		bot.Reply(ev, "无权限")
		return true, true
	case "/ai":
		role, _ := s.perm.GetGroupRole(ev.GroupID, ev.UserID)
		canAdmin := isMaster || role == "admin" || isAdmin(bot, ev.UserID)
		switch arg {
		case "on", "开", "":
			if canAdmin {
				s.perm.SetGroupAI(ev.GroupID, true)
				bot.Reply(ev, "已开启本群 AI 功能")
			} else {
				bot.Reply(ev, "无权限")
			}
			return true, true
		case "off", "关":
			if canAdmin {
				s.perm.SetGroupAI(ev.GroupID, false)
				bot.Reply(ev, "已关闭本群 AI 功能")
			} else {
				bot.Reply(ev, "无权限")
			}
			return true, true
		case "status":
			on := s.perm.GroupEnabled(ev.GroupID)
			state := "开启"
			if !on {
				state = "关闭"
			}
			bot.Reply(ev, "本群 AI 状态: "+state)
			return true, true
		}
		bot.Reply(ev, "用法: /ai on | /ai off | /ai status")
		return true, true
	case "/compact", "/压缩":
		return true, s.handleCompact(ctx, bot, ev)
	case "/new", "/新会话":
		return true, s.handleNewSession(bot, ev)
	case "/sessions", "/历史会话":
		return true, s.handleListSessions(bot, ev)
	case "/resume":
		return true, s.handleResumeSession(bot, ev, arg)
	case "/rename":
		return true, s.handleRenameSession(bot, ev, arg)
	case "/delete":
		return true, s.handleDeleteSession(bot, ev, arg)
	case "/clear", "/清除":
		return true, s.handleClearSession(bot, ev)
	case "/models", "/模型":
		return true, s.handleListModels(ctx, bot, ev, isMaster)
	case "/model":
		return true, s.handleSwitchModel(ctx, bot, ev, arg, isMaster)
	case "/provider", "/providers", "/提供商":
		return true, s.handleProvider(ctx, bot, ev, arg, isMaster)
	case "/grant":
		return true, s.handleGrant(bot, ev, arg, isMaster)
	case "/ban":
		return true, s.handleBan(bot, ev, arg, isMaster)
	case "/allow":
		return true, s.handleAllow(bot, ev, arg, isMaster)
	case "/取消信任":
		return true, s.handleAllowRevoke(bot, ev, arg, isMaster)
	}
	return false, false
}

// cmdOf 提取命令首个 token 和剩余参数
func cmdOf(text string) (cmd, arg string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", ""
	}
	cmd = fields[0]
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	return cmd, arg
}

// ---- 会话管理命令 ----

func (s *Service) handleClearSession(bot *core.Bot, ev *core.Event) bool {
	scope, gid, uid := s.sessionScope(ev)
	ses, err := s.session.GetCurrent(scope, gid, uid, "")
	if err != nil || ses == nil {
		bot.Reply(ev, "清除失败: "+err.Error())
		return true
	}
	s.session.ReplaceMessages(ses.ID, []session.Message{})
	s.session.SetSummary(ses.ID, "")
	bot.Reply(ev, "已清除当前会话上下文")
	return true
}

func (s *Service) handleNewSession(bot *core.Bot, ev *core.Event) bool {
	scope, gid, uid := s.sessionScope(ev)
	title := defaultSessionTitle(ev)
	if _, err := s.session.Create(scope, gid, uid, title); err != nil {
		bot.Reply(ev, "创建会话失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "已开启新会话")
	return true
}

func (s *Service) handleListSessions(bot *core.Bot, ev *core.Event) bool {
	scope, gid, uid := s.sessionScope(ev)
	list, err := s.session.List(scope, gid, uid)
	if err != nil {
		bot.Reply(ev, "查询失败: "+err.Error())
		return true
	}
	if len(list) == 0 {
		bot.Reply(ev, "暂无历史会话")
		return true
	}
	cur, _ := s.session.GetCurrent(scope, gid, uid, "")
	msg := "📚 历史会话:\n"
	for _, se := range list {
		title := se.Title
		if title == "" {
			title = "(无标题)"
		}
		marker := " "
		if cur != nil && cur.ID == se.ID {
			marker = "▶"
		}
		t := timeFmt(se.UpdatedAt)
		msg += fmt.Sprintf("%s #%d %s [%s] 摘要:%s\n", marker, se.ID, title, t, truncate(se.Summary, 30))
	}
	msg += "\n用 /resume <编号> 切换会话, /new 新建"
	bot.Reply(ev, msg)
	return true
}

func (s *Service) handleResumeSession(bot *core.Bot, ev *core.Event, arg string) bool {
	id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil {
		bot.Reply(ev, "用法: /resume <会话编号>")
		return true
	}
	scope, gid, uid := s.sessionScope(ev)
	ses, err := s.session.Get(id)
	if err != nil || ses == nil {
		bot.Reply(ev, "会话不存在")
		return true
	}
	if err := s.session.Switch(scope, gid, uid, id); err != nil {
		bot.Reply(ev, "切换失败: "+err.Error())
		return true
	}
	title := ses.Title
	if title == "" {
		title = "#" + strconv.FormatInt(id, 10)
	}
	bot.Reply(ev, "已切换到会话: "+title)
	return true
}

func (s *Service) handleRenameSession(bot *core.Bot, ev *core.Event, arg string) bool {
	name := strings.TrimSpace(arg)
	if name == "" {
		bot.Reply(ev, "用法: /rename <新标题>")
		return true
	}
	scope, gid, uid := s.sessionScope(ev)
	ses, err := s.session.GetCurrent(scope, gid, uid, "")
	if err != nil || ses == nil {
		bot.Reply(ev, "会话不存在")
		return true
	}
	if err := s.session.Rename(ses.ID, name); err != nil {
		bot.Reply(ev, "重命名失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "会话已重命名为: "+name)
	return true
}

func (s *Service) handleDeleteSession(bot *core.Bot, ev *core.Event, arg string) bool {
	id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil {
		bot.Reply(ev, "用法: /delete <会话编号>")
		return true
	}
	if err := s.session.Delete(id); err != nil {
		bot.Reply(ev, "删除失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "已删除会话 #"+strconv.FormatInt(id, 10))
	return true
}

func (s *Service) handleCompact(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	scope, gid, uid := s.sessionScope(ev)
	ses, err := s.session.GetCurrent(scope, gid, uid, "")
	if err != nil || ses == nil {
		bot.Reply(ev, "会话不存在")
		return true
	}
	bot.Reply(ev, "🔄 正在压缩会话...")
	if err := s.Compact(ctx, ses); err != nil {
		bot.Reply(ev, "压缩失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "✅ 会话已压缩")
	return true
}

// ---- 模型/提供商管理命令 ----

func (s *Service) handleListModels(ctx context.Context, bot *core.Bot, ev *core.Event, isMaster bool) bool {
	if !isMaster {
		bot.Reply(ev, "仅主人可查看模型列表")
		return true
	}
	bot.Reply(ev, "🔄 正在获取模型列表...")
	if !s.models.HasFetched() {
		if _, err := s.models.Fetch(ctx); err != nil {
			bot.Reply(ev, "获取模型列表失败: "+err.Error())
			return true
		}
	}
	bot.Reply(ev, s.models.Render())
	return true
}

func (s *Service) handleSwitchModel(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	if !isMaster {
		bot.Reply(ev, "仅主人可切换模型")
		return true
	}
	sub, subArg := cmdOf(arg)

	// /model list — 列出可用模型
	if sub == "list" || sub == "ls" {
		return s.handleListModels(ctx, bot, ev, true)
	}
	// /model group <组> <模型ID> — 为用户组设置默认模型
	if sub == "group" || sub == "g" {
		if subArg == "" {
			bot.Reply(ev, s.models.GroupModelsRender())
			return true
		}
		return s.handleSetGroupModel(ctx, bot, ev, subArg, isMaster)
	}
	// /model groups — 查看各组模型配置
	if sub == "groups" {
		bot.Reply(ev, s.models.GroupModelsRender())
		return true
	}
	// /model (无参数) — 查看当前配置
	if sub == "" {
		bot.Reply(ev, "当前模型: "+s.models.Current()+"\n\n"+
			"用法:\n"+
			"  /model list — 列出可用模型\n"+
			"  /model <ID> — 切换全局默认模型\n"+
			"  /model groups — 查看用户组模型配置\n"+
			"  /model group <组> <ID> — 为用户组设置模型")
		return true
	}
	id := sub

	// 确保已拉取列表(仅用于展示建议, 不强制校验通过)
	if !s.models.HasFetched() {
		if _, err := s.models.Fetch(ctx); err != nil {
			log.Printf("[ai] 拉取模型列表失败: %v", err)
		}
	}
	// 校验模型存在于列表; 若不在, 仍允许设置(列表可能过时/别名)
	exists := false
	for _, m := range s.models.List() {
		if m.ID == id {
			exists = true
			break
		}
	}
	if !exists {
		// 列出可用模型作为提示, 但仍继续设置用户指定的模型
		bot.Reply(ev, "⚠️ 模型 "+id+" 不在已拉取列表中, 仍将尝试设置。可用模型: "+strings.Join(modelIDs(s.models.List()), ", ")+"\n用 /model list 刷新")
	}
	if err := s.models.SetCurrentModel(id); err != nil {
		bot.Reply(ev, "切换失败: "+err.Error())
		return true
	}
	bot.Reply(ev, fmt.Sprintf("✅ 已设置全局默认模型 %s (上下文 %d tokens)", id, s.models.ContextWindow()))
	return true
}

// handleSetGroupModel 为用户组设置默认模型: /model group <master|admin|member> <模型ID>
func (s *Service) handleSetGroupModel(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		bot.Reply(ev, "用法: /model group <主人|管理员|成员> <模型ID>")
		return true
	}
	var lvl userLevel
	switch fields[0] {
	case "master", "主人", "owner":
		lvl = levelMaster
	case "admin", "管理员":
		lvl = levelAdmin
	case "member", "成员", "普通":
		lvl = levelMember
	default:
		bot.Reply(ev, "组名只能是: master/admin/member")
		return true
	}
	id := fields[1]
	// 确保列表已拉取(仅提示, 不强制)
	if !s.models.HasFetched() {
		if _, err := s.models.Fetch(ctx); err != nil {
			log.Printf("[ai] 拉取模型列表失败: %v", err)
		}
	}
	exists := false
	for _, m := range s.models.List() {
		if m.ID == id {
			exists = true
			break
		}
	}
	if !exists {
		bot.Reply(ev, "⚠️ 模型 "+id+" 不在已拉取列表中, 仍将尝试设置。")
	}
	if err := s.models.SetGroupModel(lvl, id); err != nil {
		bot.Reply(ev, "设置失败: "+err.Error())
		return true
	}
	bot.Reply(ev, fmt.Sprintf("✅ 已为%s设置默认模型 %s", fields[0], id))
	return true
}

// handleProvider 统一处理 /provider 子命令: add/remove/use/空
func (s *Service) handleProvider(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	if !isMaster {
		bot.Reply(ev, "仅主人可管理提供商")
		return true
	}
	sub, subArg := cmdOf(arg)
	switch sub {
	case "":
		bot.Reply(ev, s.models.RenderProviders())
		return true
	case "add":
		return s.handleAddProvider(ctx, bot, ev, subArg, isMaster)
	case "remove", "rm", "del":
		return s.handleRemoveProvider(ctx, bot, ev, subArg, isMaster)
	case "use", "switch":
		return s.handleUseProvider(ctx, bot, ev, subArg, isMaster)
	case "proxy", "setproxy":
		return s.handleSetProxy(ctx, bot, ev, subArg, isMaster)
	default:
		bot.Reply(ev, "用法: /provider [add|remove|use|proxy] ...\n/provider 查看列表")
		return true
	}
}

// handleSetProxy 设置 provider 代理: /provider proxy <名称> <代理URL>
func (s *Service) handleSetProxy(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		bot.Reply(ev, "用法: /provider proxy <名称> <代理URL>  (如 http://127.0.0.1:7890)\n代理设为 off 或空 清除代理")
		return true
	}
	name := fields[0]
	proxy := fields[1]
	if proxy == "off" || proxy == "无" || proxy == "none" {
		proxy = ""
	}
	if err := s.models.SetProviderProxy(name, proxy); err != nil {
		bot.Reply(ev, "设置失败: "+err.Error())
		return true
	}
	if proxy == "" {
		bot.Reply(ev, "✅ 已清除 "+name+" 的代理")
	} else {
		bot.Reply(ev, "✅ 已为 "+name+" 设置代理: "+proxy)
	}
	return true
}

func (s *Service) handleAddProvider(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	fields := strings.Fields(arg)
	if len(fields) < 3 {
		bot.Reply(ev, "用法: /provider add <名称> <BaseURL> <APIKey> [代理] [force]")
		return true
	}
	name := fields[0]
	baseURL := fields[1]
	apiKey := fields[2]
	proxy := ""
	replace := false
	// 第4字段可能是代理或 force
	if len(fields) >= 4 {
		if fields[3] == "force" || fields[3] == "覆盖" {
			replace = true
		} else {
			proxy = fields[3]
		}
	}
	if len(fields) >= 5 && (fields[4] == "force" || fields[4] == "覆盖") {
		replace = true
	}
	// 校验 BaseURL 格式
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	p := provider.Provider{Name: name, BaseURL: baseURL, APIKey: apiKey, Proxy: proxy}
	if err := s.models.AddProvider(p, replace); err != nil {
		bot.Reply(ev, "添加失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "✅ 已添加提供商 "+name)
	// 自动拉取模型
	bot.Reply(ev, "🔄 正在获取模型列表...")
	if _, err := s.models.Fetch(ctx); err != nil {
		bot.Reply(ev, "获取模型列表失败: "+err.Error())
		return true
	}
	bot.Reply(ev, s.models.Render())
	return true
}

func (s *Service) handleRemoveProvider(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	name := strings.TrimSpace(arg)
	if name == "" {
		bot.Reply(ev, "用法: /provider remove <名称>")
		return true
	}
	if err := s.models.RemoveProvider(name); err != nil {
		bot.Reply(ev, "删除失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "✅ 已删除提供商 "+name)
	return true
}

func (s *Service) handleUseProvider(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	name := strings.TrimSpace(arg)
	if name == "" {
		bot.Reply(ev, "用法: /provider use <名称>")
		return true
	}
	if err := s.models.SwitchProvider(name); err != nil {
		bot.Reply(ev, "切换失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "✅ 已切换到提供商 "+name)
	return true
}

func timeFmt(unix int64) string {
	t := time.Unix(unix, 0)
	return t.Format("01-02 15:04")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func (s *Service) handleAllowList(bot *core.Bot, ev *core.Event) bool {
	scope, gid := allowScopeOf(ev)
	rows, err := s.allow.AllList(scope, gid, ev.UserID)
	if err != nil {
		bot.Reply(ev, "查询失败: "+err.Error())
		return true
	}
	if len(rows) == 0 {
		bot.Reply(ev, "当前没有「以后都允许」的记录")
		return true
	}
	msg := "📋 已永久允许的操作:\n"
	for _, r := range rows {
		msg += "- " + r.ToolName + "\n"
	}
	msg += "回复 /取消信任 <工具名> 可移除"
	bot.Reply(ev, msg)
	return true
}

// handleAllow 统一处理 /allow 子命令: list/revoke
func (s *Service) handleAllow(bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	sub, subArg := cmdOf(arg)
	switch sub {
	case "", "list":
		return s.handleAllowList(bot, ev)
	case "revoke", "rm":
		return s.handleAllowRevoke(bot, ev, subArg, isMaster)
	default:
		bot.Reply(ev, "用法: /allow [list|revoke <工具名>]")
		return true
	}
}

func (s *Service) handleAllowRevoke(bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	toolName := strings.TrimSpace(arg)
	if toolName == "" {
		bot.Reply(ev, "用法: /allow revoke <工具名> 或 /取消信任 <工具名>")
		return true
	}
	scope, gid := allowScopeOf(ev)
	if err := s.allow.Revoke(scope, gid, ev.UserID, toolName); err != nil {
		bot.Reply(ev, "移除失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "已移除「"+toolName+"」的永久允许")
	return true
}

// sessionScope 返回会话作用域。群内按 session_mode 决定是否合并:
//   - merged(默认): 全群共享一个会话, userID=0
//   - separate: 群内每个用户独立会话
func (s *Service) sessionScope(ev *core.Event) (scope string, groupID, userID int64) {
	if ev.IsGroup() {
		if s.cfg.AI.SessionMode == "separate" {
			return "group", ev.GroupID, ev.UserID
		}
		return "group", ev.GroupID, 0 // merged: 全群合并
	}
	return "private", 0, ev.UserID
}

func allowScopeOf(ev *core.Event) (string, int64) {
	if ev.IsGroup() {
		return "group", ev.GroupID
	}
	return "private", 0
}

func (s *Service) handleGrant(bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	parts := strings.Fields(arg)
	if len(parts) < 2 {
		bot.Reply(ev, "用法: /grant <QQ号> <admin|member>")
		return true
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		bot.Reply(ev, "QQ 号格式错误")
		return true
	}
	role := strings.ToLower(parts[1])
	if role != "admin" && role != "member" {
		bot.Reply(ev, "角色只能是 admin 或 member")
		return true
	}
	if !isMaster {
		bot.Reply(ev, "仅主人可执行")
		return true
	}
	if err := s.perm.SetGroupRole(ev.GroupID, uid, role); err != nil {
		bot.Reply(ev, "设置失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "已将 "+parts[0]+" 设为本群 "+role)
	return true
}

func (s *Service) handleBan(bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	parts := strings.Fields(arg)
	if len(parts) < 1 {
		bot.Reply(ev, "用法: /ban <QQ号>")
		return true
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		bot.Reply(ev, "QQ 号格式错误")
		return true
	}
	if !isMaster {
		bot.Reply(ev, "仅主人可执行")
		return true
	}
	if err := s.perm.SetGroupRole(ev.GroupID, uid, "banned"); err != nil {
		bot.Reply(ev, "设置失败: "+err.Error())
		return true
	}
	bot.Reply(ev, "已将 "+parts[0]+" 加入本群黑名单")
	return true
}

func isAdmin(bot *core.Bot, uid int64) bool {
	s := strconv.FormatInt(uid, 10)
	for _, a := range bot.Cfg.Bot.AdminIDs {
		if a == s {
			return true
		}
	}
	return false
}
