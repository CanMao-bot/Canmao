package community

import (
	"fmt"
	"strconv"
	"strings"

	"gobot/core"
	data "gobot/store/community"
)

func (s *Service) privateManagement(ev *core.Event, cmd, arg string) bool {
	if cmd != "开启私聊转发" && cmd != "关闭私聊转发" && cmd != "私聊转发状态" && cmd != "回复私聊" {
		return false
	}
	if !ev.IsPrivate() || !s.master(ev.UserID) {
		s.deny(ev)
		return true
	}
	if cmd == "私聊转发状态" {
		g, err := s.store.Settings(0)
		s.respond(ev, fmt.Sprintf("私聊转发：%v（默认关闭，仅转发未被命令处理的文本）", g.PrivateRelay), err)
		return true
	}
	if cmd == "回复私聊" {
		uidText, body := split(arg)
		uid, err := target(uidText)
		if err != nil || body == "" || len([]rune(body)) > 1000 {
			s.bot.Reply(ev, "用法：回复私聊 QQ 内容（1至1000字）")
			return true
		}
		allowed, err := s.store.Listed(0, uid, "contact")
		if err != nil || !allowed {
			s.bot.Reply(ev, "只能回复已转发给主人的私聊联系人")
			return true
		}
		copyEv := *ev
		copyEv.UserID = uid
		if !s.AllowEvent(&copyEv) {
			s.bot.Reply(ev, "该联系人已被屏蔽")
			return true
		}
		if err = s.store.Audit(0, ev.UserID, "private_reply_requested", id(uid)); err == nil {
			err = s.bot.Sender.SendPrivateMsg(uid, []core.Segment{core.TextSegment(body)})
		}
		s.respond(ev, "私聊回复已发送", err)
		return true
	}
	err := s.store.ChangeSettings(0, ev.UserID, func(g *data.Settings) error { g.PrivateRelay = cmd == "开启私聊转发"; return nil })
	s.respond(ev, "已更新私聊转发开关", err)
	return true
}
func (s *Service) relayPrivate(ev *core.Event, text string) {
	if !ev.IsPrivate() || s.master(ev.UserID) || text == "" || strings.HasPrefix(text, "/") {
		return
	}
	g, err := s.store.Settings(0)
	if err != nil || !g.PrivateRelay {
		return
	}
	lower := strings.ToLower(text)
	for _, secret := range []string{"sk-", "api_key", "apikey", "token", "password", "密码", "密钥"} {
		if strings.Contains(lower, secret) {
			return
		}
	}
	// One forwarded message per user per minute; no attachment URLs or CQ execution.
	ok, err := s.store.Claim(fmt.Sprintf("relay:%d:%d", ev.UserID, s.now().Unix()/60), s.now())
	if err != nil || !ok {
		return
	}
	owner, err := strconv.ParseInt(s.bot.Cfg.Bot.MasterID, 10, 64)
	if err != nil || owner <= 0 {
		return
	}
	if len([]rune(text)) > 1000 {
		text = string([]rune(text)[:1000]) + "…"
	}
	if err = s.bot.Sender.SendPrivateMsg(owner, []core.Segment{core.TextSegment("私聊转发 · 来自 " + id(ev.UserID) + "\n" + text + "\n回复：回复私聊 " + id(ev.UserID) + " 内容")}); err == nil {
		_ = s.store.SetList(0, ev.UserID, "contact", true, ev.UserID)
	}
}
