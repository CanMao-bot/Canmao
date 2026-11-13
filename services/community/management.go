package community

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gobot/core"
	data "gobot/store/community"
	"gobot/store/perm"
)

// Optional protocol capabilities keep existing adapters and plugins compatible.
type groupDirectory interface {
	GetGroupList() ([]map[string]interface{}, error)
}
type messageDeleter interface{ DeleteMsg(messageID int64) error }
type friendApprover interface {
	SetFriendAddRequest(flag string, approve bool, remark string) error
}

func (s *Service) management(ctx context.Context, ev *core.Event, text string) bool {
	cmd, arg := split(text)
	if s.privateManagement(ev, cmd, arg) {
		return true
	}
	if text == "/bot on" || text == "/bot 开" {
		cmd = "开启本群"
	}
	if text == "/bot off" || text == "/bot 关" {
		cmd = "关闭本群"
	}
	switch cmd {
	case "运行状态":
		msg := "群娱服务已运行（无需AI模型）\n娱乐数据范围：" + s.scope(ev)
		if ev.IsGroup() {
			g, e := s.perm.GroupAI(ev.GroupID)
			on := e == nil && g.Enabled && id(g.EnabledBy) == s.bot.Cfg.Bot.MasterID
			msg += fmt.Sprintf("\n本群 bot：%v，AI：%v（仅主人用 /ai 开关和设置）", s.perm.BotEnabled(ev.GroupID), on)
		}
		s.bot.Reply(ev, msg)
		return true
	case "群列表":
		s.groupList(ev)
		return true
	case "选择管理群":
		if !ev.IsPrivate() {
			s.bot.Reply(ev, "请在私聊中选择管理群")
			return true
		}
		gid, err := target(arg)
		if err != nil {
			s.respond(ev, "", err)
			return true
		}
		if s.level(gid, ev.UserID) < 30 || !s.member(gid, s.selfID(ev)) {
			s.deny(ev)
			return true
		}
		err = s.store.SelectGroup(ev.UserID, gid)
		s.respond(ev, "当前管理群："+id(gid)+"；每次操作都会重新校验权限", err)
		return true
	case "当前管理群":
		if !ev.IsPrivate() {
			s.bot.Reply(ev, "当前群："+id(ev.GroupID))
			return true
		}
		gid, err := s.targetGroup(ev)
		s.respond(ev, "当前管理群："+id(gid), err)
		return true
	case "添加超管", "取消超管", "超管列表":
		if !s.master(ev.UserID) {
			s.deny(ev)
			return true
		}
		if cmd == "超管列表" {
			ids, err := s.store.Roles(0)
			s.respond(ev, "数据库超管："+formatIDs(ids)+"\n配置中的管理员由配置管理；社区超管不具有系统命令审批权", err)
			return true
		}
		uid, err := target(arg)
		if err == nil {
			level := 50
			if cmd == "取消超管" {
				level = 0
			}
			if s.master(uid) {
				err = errors.New("不能修改主人权限")
			} else {
				err = s.store.SetRole(0, uid, level, ev.UserID)
			}
		}
		s.respond(ev, "超管权限已更新", err)
		return true
	case "私聊屏蔽", "取消私聊屏蔽":
		if !s.master(ev.UserID) {
			s.deny(ev)
			return true
		}
		uid, err := target(arg)
		if err == nil {
			if s.level(0, uid) >= 50 {
				err = errors.New("不能屏蔽主人或全局管理员")
			} else {
				err = s.store.SetList(0, uid, "private", cmd == "私聊屏蔽", ev.UserID)
			}
		}
		s.respond(ev, "私聊屏蔽已更新", err)
		return true
	case "全局审计":
		if !s.master(ev.UserID) {
			s.deny(ev)
			return true
		}
		s.showAudit(ev, 0)
		return true
	}
	// Global policy is deliberately owner-only; group authority never escalates it.
	global := false
	for _, verb := range []string{"添加", "删除", "查看", "开启", "关闭", "设置"} {
		prefix := verb + "全局"
		if strings.HasPrefix(cmd, prefix) {
			cmd = verb + strings.TrimPrefix(cmd, prefix)
			global = true
			break
		}
	}
	if cmd == "全局回复列表" {
		cmd = "回复列表"
		global = true
	}
	if cmd == "全局功能开关" {
		cmd = "功能开关"
		global = true
	}
	if cmd == "全局菜单设置" {
		cmd = "菜单设置"
		global = true
	}
	known := map[string]bool{"开启本群": true, "关闭本群": true, "功能开关": true, "开启功能": true, "关闭功能": true, "数据模式": true, "添加后台管理": true, "取消后台管理": true, "后台管理列表": true, "添加名单": true, "删除名单": true, "查看名单": true, "添加回复": true, "删除回复": true, "回复列表": true, "添加关键词": true, "删除关键词": true, "关键词列表": true, "设置事件": true, "删除事件": true, "事件列表": true, "发放金币": true, "扣除金币": true, "群审计": true, "警告": true, "查警告": true, "清空警告": true}
	known["设置菜单"] = true
	known["删除菜单"] = true
	known["菜单设置"] = true
	if isExternal(cmd) {
		known[cmd] = true
	}
	if !known[cmd] {
		return false
	}
	if global && cmd != "添加回复" && cmd != "删除回复" && cmd != "回复列表" && cmd != "添加名单" && cmd != "删除名单" && cmd != "查看名单" && cmd != "功能开关" && cmd != "开启功能" && cmd != "关闭功能" && cmd != "设置菜单" && cmd != "删除菜单" && cmd != "菜单设置" {
		s.bot.Reply(ev, "此命令不支持全局范围")
		return true
	}
	if global && !s.master(ev.UserID) {
		s.deny(ev)
		return true
	}
	gid := int64(0)
	var err error
	if !global {
		gid, err = s.targetGroup(ev)
		if err != nil {
			s.respond(ev, "", err)
			return true
		}
	}
	level := s.level(gid, ev.UserID)
	minimum := 30
	if cmd == "添加后台管理" || cmd == "取消后台管理" || cmd == "数据模式" {
		minimum = 40
	}
	if cmd == "发放金币" || cmd == "扣除金币" || cmd == "退出群聊" || cmd == "设置群管理" || cmd == "取消群管理" {
		minimum = 60
	}
	if cmd == "警告" || cmd == "查警告" || cmd == "清空警告" || isExternal(cmd) && minimum != 60 {
		minimum = 20
	}
	if level < minimum {
		s.deny(ev)
		return true
	}
	if isExternal(cmd) {
		if !s.enabled(gid, "management") {
			s.bot.Reply(ev, "群管功能未开启")
			return true
		}
		s.externalCommand(ctx, ev, gid, cmd, arg)
		return true
	}
	switch cmd {
	case "设置菜单", "删除菜单", "菜单设置":
		if cmd == "菜单设置" {
			g, e := s.store.Settings(gid)
			lines := []string{"当前菜单模板"}
			for _, k := range []string{"all", "娱乐", "排行", "农场", "坐骑", "钓鱼", "群管", "管理", "私聊", "主人"} {
				if v := g.Menus[k]; v != "" {
					lines = append(lines, k+"："+v)
				}
			}
			if e != nil {
				s.respond(ev, "", e)
			} else {
				s.plain(ev, strings.Join(lines, "\n"))
			}
			return true
		}
		key, body := split(arg)
		if !validMenu(key) {
			s.bot.Reply(ev, "类别：all/娱乐/排行/农场/坐骑/钓鱼/群管/管理/私聊/主人")
			return true
		}
		if cmd == "设置菜单" && (strings.Count(body, "$菜单$") != 1 || len([]rune(body)) > 1000) {
			s.bot.Reply(ev, "模板最多1000字，须包含一次 $菜单$（自动过滤权限和开关）；换行用 $换行$")
			return true
		}
		err = s.store.ChangeSettings(gid, ev.UserID, func(g *data.Settings) error {
			if cmd == "删除菜单" {
				delete(g.Menus, key)
			} else {
				g.Menus[key] = body
			}
			return nil
		})
		s.respond(ev, "菜单模板已更新", err)
	case "开启本群", "关闭本群":
		err = s.perm.SetGroupBot(gid, cmd == "开启本群")
		if err == nil {
			err = s.store.Audit(gid, ev.UserID, "bot", cmd)
		}
		s.respond(ev, cmd+"成功（群 "+id(gid)+"）", err)
	case "功能开关":
		lines := []string{"功能开关（群 " + id(gid) + "，含全局默认值）"}
		for _, name := range featureOrder {
			state := "关闭"
			if s.enabled(gid, featureNames[name]) {
				state = "开启"
			}
			lines = append(lines, name+"："+state)
		}
		s.bot.Reply(ev, strings.Join(lines, "\n"))
	case "开启功能", "关闭功能":
		key := featureKey(arg)
		if key == "" {
			s.bot.Reply(ev, "未知功能，可用："+strings.Join(featureOrder, "、"))
			return true
		}
		err = s.store.ChangeSettings(gid, ev.UserID, func(g *data.Settings) error { g.Features[key] = cmd == "开启功能"; return nil })
		s.respond(ev, "已更新群 "+id(gid)+" 的「"+arg+"」开关", err)
	case "数据模式":
		if arg == "" {
			g, e := s.store.Settings(gid)
			mode := g.Mode
			if mode == "" {
				mode = s.bot.Cfg.Community.DefaultMode
			}
			if mode == "" {
				mode = "separate"
			}
			s.respond(ev, "当前数据模式："+mode+"；global 全局共享，separate 分群独立。切换保留两套数据，私聊账户独立。", e)
			return true
		}
		if arg == "全局" {
			arg = "global"
		}
		if arg == "分群" {
			arg = "separate"
		}
		if arg != "global" && arg != "separate" {
			s.bot.Reply(ev, "用法：数据模式 global|separate")
			return true
		}
		err = s.store.ChangeSettings(gid, ev.UserID, func(g *data.Settings) error { g.Mode = arg; return nil })
		s.respond(ev, "已切换为 "+arg+"，原数据已保留", err)
	case "添加后台管理", "取消后台管理", "后台管理列表":
		if cmd == "后台管理列表" {
			ids, e := s.store.Roles(gid)
			s.respond(ev, "本群后台管理："+formatIDs(ids), e)
			return true
		}
		uid, e := target(arg)
		if e == nil {
			if s.level(gid, uid) >= level {
				e = errors.New("不能修改同级或更高权限")
			} else if cmd == "添加后台管理" && !s.member(gid, uid) {
				e = errors.New("对方不是本群成员")
			} else {
				v := 30
				if cmd == "取消后台管理" {
					v = 0
				}
				e = s.store.SetRole(gid, uid, v, ev.UserID)
				if e == nil && v == 0 {
					e = s.perm.SetGroupRole(gid, uid, perm.RoleMember)
				}
			}
		}
		s.respond(ev, "本群后台权限已更新", e)
	case "添加名单", "删除名单", "查看名单":
		f := strings.Fields(arg)
		if len(f) < 1 {
			s.bot.Reply(ev, "用法：添加/删除名单 black|white|gray QQ；查看名单 类型")
			return true
		}
		kind := listKind(f[0])
		if kind == "" {
			s.bot.Reply(ev, "名单类型须为 black/white/gray（黑/白/灰）")
			return true
		}
		if cmd == "查看名单" {
			ids, e := s.store.List(gid, kind)
			s.respond(ev, "名单 "+kind+"："+formatIDs(ids), e)
			return true
		}
		if len(f) != 2 {
			s.bot.Reply(ev, "请指定名单类型和用户编号")
			return true
		}
		uid, e := target(f[1])
		if e == nil {
			if cmd == "添加名单" && kind != "white" && s.level(gid, uid) >= level {
				e = errors.New("不能屏蔽同级或更高权限")
			} else {
				e = s.store.SetList(gid, uid, kind, cmd == "添加名单", ev.UserID)
			}
		}
		s.respond(ev, "名单已更新", e)
	case "添加回复", "删除回复", "回复列表", "添加关键词", "删除关键词", "关键词列表":
		s.rules(ev, gid, cmd, arg)
	case "设置事件", "删除事件", "事件列表":
		if cmd == "事件列表" {
			g, e := s.store.Settings(gid)
			lines := []string{"事件模板（群 " + id(gid) + "）"}
			for _, k := range []string{"welcome", "leave", "admin", "card"} {
				lines = append(lines, k+"："+g.Events[k])
			}
			s.respond(ev, strings.Join(lines, "\n"), e)
			return true
		}
		key, body := split(arg)
		if key != "welcome" && key != "leave" && key != "admin" && key != "card" {
			s.bot.Reply(ev, "事件类型：welcome/leave/admin/card")
			return true
		}
		if cmd == "设置事件" && (body == "" || len([]rune(body)) > 1000) {
			s.bot.Reply(ev, "模板须为1至1000字")
			return true
		}
		err = s.store.ChangeSettings(gid, ev.UserID, func(g *data.Settings) error {
			if cmd == "删除事件" {
				delete(g.Events, key)
			} else {
				g.Events[key] = body
			}
			return nil
		})
		s.respond(ev, "事件模板已更新", err)
	case "发放金币", "扣除金币":
		f := strings.Fields(arg)
		if len(f) != 2 {
			s.bot.Reply(ev, "用法："+cmd+" QQ 数量")
			return true
		}
		uid, e := target(f[0])
		if e != nil {
			s.respond(ev, "", e)
			return true
		}
		n, e := amount(f[1])
		if e != nil {
			s.respond(ev, "", e)
			return true
		}
		if !s.member(gid, uid) {
			s.bot.Reply(ev, "目标不是管理群的成员")
			return true
		}
		copyEv := *ev
		copyEv.DetailType = "group"
		copyEv.GroupID = gid
		msg, e := s.store.Change(s.scope(&copyEv), []int64{uid}, ev.UserID, "admin_coins", func(m map[int64]*data.Account) (string, error) {
			a := m[uid]
			if cmd == "扣除金币" {
				if a.Coins < n {
					return "", errors.New("目标余额不足")
				}
				a.Coins -= n
			} else {
				a.Coins += n
			}
			return fmt.Sprintf("%s %d，目标 %d 余额 %d", cmd, n, uid, a.Coins), nil
		})
		s.respond(ev, msg, e)
	case "警告", "查警告", "清空警告":
		uid, e := commandTarget(ev, arg)
		if e != nil {
			s.respond(ev, "", e)
			return true
		}
		delta := int64(0)
		if cmd != "查警告" {
			if s.level(gid, uid) >= level {
				s.deny(ev)
				return true
			}
			if cmd == "警告" {
				delta = 1
			} else {
				delta = -1_000_000_000
			}
		}
		n, e := s.store.Warn(gid, uid, delta)
		if e == nil && delta != 0 {
			e = s.store.Audit(gid, ev.UserID, "warning", fmt.Sprintf("user=%d count=%d", uid, n))
		}
		s.respond(ev, fmt.Sprintf("%d 本群警告：%d次", uid, n), e)
	case "群审计":
		s.showAudit(ev, gid)
	}
	return true
}
func formatIDs(ids []int64) string {
	if len(ids) == 0 {
		return "（空）"
	}
	v := make([]string, 0, len(ids))
	for _, uid := range ids {
		v = append(v, id(uid))
	}
	return strings.Join(v, "、")
}
func listKind(v string) string {
	switch v {
	case "black", "黑", "黑名单":
		return "black"
	case "white", "白", "白名单":
		return "white"
	case "gray", "灰", "灰名单":
		return "gray"
	}
	return ""
}
func commandTarget(ev *core.Event, arg string) (int64, error) {
	if at := ev.AtQQs(); len(at) == 1 && strings.TrimSpace(arg) == "" {
		return at[0], nil
	}
	return target(arg)
}
func (s *Service) selfID(ev *core.Event) int64 {
	if ev.SelfID > 0 {
		return ev.SelfID
	}
	n, _ := strconv.ParseInt(s.bot.Cfg.OneBot.SelfID, 10, 64)
	return n
}
func (s *Service) showAudit(ev *core.Event, gid int64) {
	list, err := s.store.Audits(gid)
	lines := []string{"最近20条审计（群 " + id(gid) + "）"}
	for _, a := range list {
		lines = append(lines, fmt.Sprintf("%s %d %s %s", time.Unix(a.At, 0).In(s.loc).Format("01-02 15:04"), a.Actor, a.Action, a.Detail))
	}
	s.respond(ev, strings.Join(lines, "\n"), err)
}
func (s *Service) groupList(ev *core.Event) {
	if !ev.IsPrivate() {
		s.bot.Reply(ev, "请在私聊查询管理群列表")
		return
	}
	okClaim, claimErr := s.store.Claim(fmt.Sprintf("grouplist:%d:%d", ev.UserID, s.now().Unix()/60), s.now())
	if claimErr != nil || !okClaim {
		s.bot.Reply(ev, "群列表每分钟最多查询一次")
		return
	}
	c, ok := s.bot.Sender.(groupDirectory)
	if !ok {
		s.bot.Reply(ev, "当前连接不支持群列表")
		return
	}
	groups, err := c.GetGroupList()
	if err != nil {
		s.respond(ev, "", err)
		return
	}
	lines := []string{"可管理群（最多100项）"}
	global := s.level(0, ev.UserID) >= 50
	count := 0
	for i, g := range groups {
		if i >= 100 {
			break
		}
		gid := number(g["group_id"])
		if gid <= 0 {
			continue
		}
		if !global && s.level(gid, ev.UserID) < 30 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d %v", gid, g["group_name"]))
		count++
		if count >= 100 {
			break
		}
	}
	if count == 0 {
		lines = append(lines, "暂无可管理群")
	}
	s.plain(ev, strings.Join(lines, "\n"))
}
func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		v, _ := strconv.ParseInt(n, 10, 64)
		return v
	}
	return 0
}

func (s *Service) rules(ev *core.Event, gid int64, cmd, arg string) {
	keyword := strings.Contains(cmd, "关键词")
	isAdd := strings.HasPrefix(cmd, "添加")
	isDelete := strings.HasPrefix(cmd, "删除")
	if !isAdd && !isDelete {
		g, err := s.store.Settings(gid)
		rules := g.Replies
		if keyword {
			rules = g.Keywords
		}
		lines := []string{cmd}
		for _, r := range rules {
			lines = append(lines, fmt.Sprintf("%s [%s/%s/权限%d/模糊%v] %s", r.Key, r.Scene, r.Action, r.MinLevel, r.Fuzzy, r.Text))
		}
		s.plain(ev, strings.Join(lines, "\n"))
		if err != nil {
			s.respond(ev, "", err)
		}
		return
	}
	var rule data.Rule
	if isAdd {
		left, body, ok := strings.Cut(arg, "|")
		if !ok || len([]rune(body)) > 2000 {
			s.bot.Reply(ev, "格式：添加回复 精准|模糊 群聊|私聊|全部 权限 关键词|正文；添加关键词 动作 关键词|正文（正文最多2000字）")
			return
		}
		if keyword {
			action, key := split(left)
			if action != "reply" && action != "warn" && action != "ban" && action != "kick" && action != "recall" && action != "black" {
				s.bot.Reply(ev, "动作：reply/warn/ban/kick/recall/black；按管理员配置自动执行")
				return
			}
			rule = data.Rule{Key: key, Text: body, Scene: "group", Fuzzy: true, Action: action}
		} else {
			f := strings.Fields(left)
			if len(f) < 4 {
				s.bot.Reply(ev, "用法：添加回复 精准 群聊 0 关键词|回复内容")
				return
			}
			if f[0] != "精准" && f[0] != "模糊" {
				s.bot.Reply(ev, "匹配方式为精准或模糊")
				return
			}
			scene := map[string]string{"群聊": "group", "私聊": "private", "全部": "all"}[f[1]]
			min, err := strconv.Atoi(f[2])
			if scene == "" || err != nil || (min != 0 && min != 20 && min != 30 && min != 40 && min != 50 && min != 60) {
				s.bot.Reply(ev, "场景须为群聊/私聊/全部，权限须为0/20/30/40/50/60")
				return
			}
			rule = data.Rule{Key: strings.Join(f[3:], " "), Text: body, Scene: scene, MinLevel: min, Fuzzy: f[0] == "模糊", Action: "reply"}
		}
		if strings.TrimSpace(rule.Key) == "" || len([]rune(rule.Key)) > 64 || strings.HasPrefix(rule.Key, "/") {
			s.bot.Reply(ev, "关键词须为1至64字且不能以 / 开头")
			return
		}
		if rule.Action == "reply" && body == "" {
			s.bot.Reply(ev, "回复正文不能为空")
			return
		}
		if rule.Action == "ban" {
			n, err := strconv.Atoi(body)
			if err != nil || n < 1 || n > 43200 {
				s.bot.Reply(ev, "禁言正文须为1至43200分钟")
				return
			}
		}
	} else {
		rule.Key = strings.TrimSpace(arg)
		if rule.Key == "" {
			s.bot.Reply(ev, "请指定关键词")
			return
		}
	}
	err := s.store.ChangeSettings(gid, ev.UserID, func(g *data.Settings) error {
		rules := &g.Replies
		if keyword {
			rules = &g.Keywords
		}
		out := make([]data.Rule, 0, len(*rules)+1)
		for _, r := range *rules {
			if r.Key != rule.Key {
				out = append(out, r)
			}
		}
		if isAdd {
			if len(out) >= 100 {
				return errors.New("每个范围最多100条规则")
			}
			out = append(out, rule)
		}
		*rules = out
		return nil
	})
	s.respond(ev, "规则已更新（模板仅显示文字，不执行文本代码）", err)
}
