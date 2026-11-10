package community

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gobot/core"
)

type action struct {
	Kind                       string
	GroupID, Target, MessageID int64
	Duration                   int
	Text                       string
}

func isExternal(cmd string) bool {
	switch cmd {
	case "禁言", "解禁", "移出", "群名片", "群名", "全体禁言", "全体解禁", "群公告", "撤回", "退出群聊", "设置群管理", "取消群管理":
		return true
	}
	return false
}
func (s *Service) externalCommand(ctx context.Context, ev *core.Event, gid int64, cmd, arg string) {
	a := action{Kind: cmd, GroupID: gid}
	var err error
	switch cmd {
	case "禁言", "群名片":
		f := strings.Fields(arg)
		if at := ev.AtQQs(); len(at) == 1 {
			a.Target = at[0]
			a.Text = arg
		} else if len(f) >= 2 {
			a.Target, err = target(f[0])
			a.Text = strings.TrimSpace(strings.TrimPrefix(arg, f[0]))
		} else {
			err = errors.New("用法：" + cmd + " QQ 内容（禁言单位为分钟）")
		}
		if err == nil && cmd == "禁言" {
			a.Duration, err = banSeconds(a.Text)
			a.Text = ""
		}
	case "解禁", "移出", "设置群管理", "取消群管理":
		a.Target, err = commandTarget(ev, arg)
	case "群名", "群公告":
		a.Text = arg
	case "撤回":
		if !ev.IsGroup() {
			err = errors.New("撤回须在目标群中引用原消息")
		} else {
			a.MessageID = ev.ReplyID()
			if a.MessageID == 0 {
				err = errors.New("请引用要撤回的消息")
			}
		}
	}
	if err != nil {
		s.respond(ev, "", err)
		return
	}
	s.executeAction(ctx, ev, a, false)
}
func (s *Service) validateAction(ev *core.Event, a action, automatic bool) error {
	if a.GroupID <= 0 {
		return errors.New("目标群无效")
	}
	if !s.perm.BotEnabled(a.GroupID) {
		return errors.New("本群 bot 已关闭")
	}
	if !s.AllowEvent(ev) {
		return errors.New("请求者已被屏蔽")
	}
	if !s.enabled(a.GroupID, "management") {
		return errors.New("群管功能已关闭")
	}
	minimum := 20
	if a.Kind == "退出群聊" || a.Kind == "设置群管理" || a.Kind == "取消群管理" {
		minimum = 60
	}
	actorLevel := s.level(a.GroupID, ev.UserID)
	if !automatic && actorLevel < minimum {
		return errors.New("请求者已无权执行此操作")
	}
	c, ok := s.bot.Sender.(core.GroupAdminClient)
	if !ok {
		return errors.New("当前连接不支持群管理")
	}
	self := s.selfID(ev)
	info, err := c.GetGroupMemberInfo(a.GroupID, self)
	if err != nil {
		return errors.New("无法确认 bot 群身份")
	}
	botRole, _ := info["role"].(string)
	if a.Kind != "退出群聊" && botRole != "owner" && botRole != "admin" {
		return errors.New("bot 需要成为 QQ 群管理")
	}
	if (a.Kind == "设置群管理" || a.Kind == "取消群管理") && botRole != "owner" {
		return errors.New("设置群管理需要 bot 是 QQ 群主")
	}
	if a.Target > 0 {
		if a.Target == self || s.master(a.Target) || s.level(a.GroupID, a.Target) >= 50 {
			return errors.New("禁止操作主人、全局管理员或 bot 自身")
		}
		targetInfo, err := c.GetGroupMemberInfo(a.GroupID, a.Target)
		if err != nil {
			return errors.New("无法确认目标群成员身份")
		}
		role, _ := targetInfo["role"].(string)
		if role != "member" && role != "admin" && role != "owner" {
			return errors.New("目标已不在群中")
		}
		if a.Kind != "设置群管理" && a.Kind != "取消群管理" {
			if role == "admin" || role == "owner" || s.level(a.GroupID, a.Target) >= 30 {
				return errors.New("目标为受保护的 QQ 管理或后台管理")
			}
		}
	}
	if a.Kind == "禁言" && (a.Target <= 0 || a.Duration < 1 || a.Duration > 30*24*3600) {
		return errors.New("禁言目标或时长无效")
	}
	if (a.Kind == "移出" || a.Kind == "解禁" || a.Kind == "群名片" || a.Kind == "设置群管理" || a.Kind == "取消群管理" || a.Kind == "拉黑") && a.Target <= 0 {
		return errors.New("目标用户无效")
	}
	if (a.Kind == "群名" || a.Kind == "群公告" || a.Kind == "群名片") && (strings.TrimSpace(a.Text) == "" || len([]rune(a.Text)) > 500) {
		return errors.New("内容须为1至500字")
	}
	if a.Kind == "撤回" {
		if !ev.IsGroup() || ev.GroupID != a.GroupID || a.MessageID <= 0 {
			return errors.New("只能在原群中引用撤回消息")
		}
		if _, ok := s.bot.Sender.(messageDeleter); !ok {
			return errors.New("当前连接不支持撤回")
		}
		g, ok := s.bot.Sender.(core.MessageGetter)
		if !ok {
			return errors.New("无法验证被引用消息的发送者")
		}
		m, err := g.GetMsg(a.MessageID)
		if err != nil || m == nil || m.UserID <= 0 {
			return errors.New("无法确认被引用消息")
		}
		// For manual recalls require a verified group_id, never a guessed message ID.
		if m.GroupID != a.GroupID {
			return errors.New("被引用消息不属于当前群")
		}
		if s.level(a.GroupID, m.UserID) >= 20 || s.master(m.UserID) {
			return errors.New("禁止撤回受保护成员的消息")
		}
	}
	return nil
}
func (s *Service) executeAction(ctx context.Context, ev *core.Event, a action, automatic bool) {
	if ctx.Err() != nil {
		return
	}
	if err := s.validateAction(ev, a, automatic); err != nil {
		if !automatic {
			s.respond(ev, "", err)
		}
		return
	}
	if automatic {
		key := fmt.Sprintf("moderation:%d:%d:%d", a.GroupID, ev.UserID, s.now().Unix()/60)
		ok, err := s.store.Claim(key, s.now())
		if err != nil || !ok {
			return
		}
	}
	select {
	case s.moderationSlots <- struct{}{}:
		defer func() { <-s.moderationSlots }()
	default:
		if !automatic {
			s.bot.Reply(ev, "群管操作已达并发上限，请稍后重试")
		}
		return
	}
	// Commands and configured rules are deterministic administrator operations.
	// Agent tool approval is enforced separately in ai.Service.executeTool.
	if automatic && !s.keywordStillMatches(ev, a) {
		s.bot.Reply(ev, "关键词规则或开关已改变，操作已取消")
		return
	}
	if err := s.validateAction(ev, a, automatic); err != nil {
		s.respond(ev, "", err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := s.store.Audit(a.GroupID, ev.UserID, "executing", fmt.Sprintf("%+v", a)); err != nil {
		s.respond(ev, "", err)
		return
	}
	c := s.bot.Sender.(core.GroupAdminClient)
	var err error
	switch a.Kind {
	case "禁言":
		err = c.SetGroupBan(a.GroupID, a.Target, a.Duration)
	case "解禁":
		err = c.SetGroupBan(a.GroupID, a.Target, 0)
	case "移出":
		err = c.SetGroupKick(a.GroupID, a.Target, false)
	case "群名片":
		err = c.SetGroupCard(a.GroupID, a.Target, a.Text)
	case "群名":
		err = c.SetGroupName(a.GroupID, a.Text)
	case "群公告":
		err = c.SendGroupNotice(a.GroupID, a.Text)
	case "全体禁言", "全体解禁":
		err = c.SetGroupWholeBan(a.GroupID, a.Kind == "全体禁言")
	case "设置群管理", "取消群管理":
		err = c.SetGroupAdmin(a.GroupID, a.Target, a.Kind == "设置群管理")
	case "退出群聊":
		err = c.SetGroupLeave(a.GroupID, false)
	case "撤回":
		err = s.bot.Sender.(messageDeleter).DeleteMsg(a.MessageID)
	case "拉黑":
		err = s.store.SetList(a.GroupID, a.Target, "black", true, ev.UserID)
	default:
		err = errors.New("未知管理动作")
	}
	status := "completed"
	if err != nil {
		status = "failed_or_uncertain"
	}
	auditErr := s.store.Audit(a.GroupID, ev.UserID, status, fmt.Sprintf("%s target=%d", a.Kind, a.Target))
	if err == nil {
		err = auditErr
	}
	s.respond(ev, "群 "+id(a.GroupID)+"「"+a.Kind+"」已执行", err)
}

func (s *Service) keyword(ctx context.Context, ev *core.Event, text string) bool {
	if strings.HasPrefix(text, "/") {
		return false
	}
	g, err := s.store.Settings(ev.GroupID)
	if err != nil {
		return true
	}
	if len(g.Keywords) == 0 {
		return false
	}
	for _, scope := range []int64{0, ev.GroupID} {
		v, err := s.store.Listed(scope, ev.UserID, "white")
		if err != nil {
			return true
		}
		if v {
			return false
		}
	}
	if s.level(ev.GroupID, ev.UserID) >= 20 {
		return false
	}
	sort.Slice(g.Keywords, func(i, j int) bool { return g.Keywords[i].Key < g.Keywords[j].Key })
	for _, r := range g.Keywords {
		if !strings.Contains(text, r.Key) {
			continue
		}
		switch r.Action {
		case "reply":
			s.plain(ev, s.render(r.Text, ev))
		case "warn":
			n, err := s.store.Warn(ev.GroupID, ev.UserID, 1)
			if err == nil {
				err = s.store.Audit(ev.GroupID, ev.UserID, "keyword_warning", fmt.Sprintf("count=%d", n))
			}
			s.respond(ev, fmt.Sprintf("关键词提醒：本群累计%d次警告", n), err)
		default:
			a := action{GroupID: ev.GroupID, Target: ev.UserID}
			switch r.Action {
			case "ban":
				a.Kind = "禁言"
				n, _ := strconv.Atoi(r.Text)
				a.Duration = n * 60
			case "kick":
				a.Kind = "移出"
			case "recall":
				a.Kind = "撤回"
				a.MessageID = ev.MessageID
			case "black":
				a.Kind = "拉黑"
			default:
				return false
			}
			s.executeAction(ctx, ev, a, true)
		}
		return true
	}
	return false
}
func (s *Service) keywordStillMatches(ev *core.Event, a action) bool {
	if !s.enabled(a.GroupID, "keyword") {
		return false
	}
	g, err := s.store.Settings(a.GroupID)
	if err != nil {
		return false
	}
	for _, scope := range []int64{0, a.GroupID} {
		v, err := s.store.Listed(scope, ev.UserID, "white")
		if err != nil || v {
			return false
		}
	}
	for _, r := range g.Keywords {
		if !strings.Contains(ev.Text(), r.Key) {
			continue
		}
		kind := map[string]string{"ban": "禁言", "kick": "移出", "recall": "撤回", "black": "拉黑"}[r.Action]
		if kind == a.Kind {
			if kind == "禁言" {
				n, _ := strconv.Atoi(r.Text)
				if n*60 != a.Duration {
					continue
				}
			}
			return true
		}
	}
	return false
}
func (s *Service) notice(ev *core.Event) bool {
	if ev.GroupID <= 0 {
		return false
	}
	if !s.perm.BotEnabled(ev.GroupID) {
		return true
	}
	if !s.enabled(ev.GroupID, "event") {
		return false
	}
	key := ""
	switch ev.NoticeType {
	case "group_increase":
		key = "welcome"
	case "group_decrease":
		key = "leave"
	case "group_admin":
		key = "admin"
	case "group_card":
		key = "card"
	default:
		return false
	}
	g, err := s.store.Settings(ev.GroupID)
	if err != nil {
		return true
	}
	template := g.Events[key]
	if template == "" {
		return true
	}
	// Notices have no message_type in OneBot; address the group explicitly.
	copyEv := *ev
	copyEv.DetailType = "group"
	s.plain(&copyEv, s.render(template, &copyEv))
	return true
}
