package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gobot/core"
	"gobot/store/perm"
)

type groupAIGuardKey struct{}

func (s *Service) checkRunGroupAI(ctx context.Context) error {
	if active, _ := ctx.Value(groupAIGuardKey{}).(bool); !active {
		return nil
	}
	ev := core.EventFrom(ctx)
	if ev == nil || !s.groupAIEnabled(ev.GroupID) {
		return errors.New("本群 AI 已关闭或主人授权已失效")
	}
	return nil
}

func (s *Service) groupAISettings(gid int64) (perm.GroupAISettings, error) {
	g, err := s.perm.GroupAI(gid)
	owner, parseErr := strconv.ParseInt(s.cfg.Bot.MasterID, 10, 64)
	if parseErr != nil || owner <= 0 || g.EnabledBy != owner {
		g.Enabled = false
	}
	return g, err
}
func (s *Service) groupAIEnabled(gid int64) bool {
	g, err := s.groupAISettings(gid)
	return err == nil && g.Enabled && s.perm.BotEnabled(gid)
}
func (s *Service) groupTriggers(g perm.GroupAISettings) (bool, float64) {
	mention := s.cfg.AI.MentionOnly
	p := 1.0
	if s.cfg.AI.ReplyProbability != nil {
		p = *s.cfg.AI.ReplyProbability
	}
	if g.MentionOnly != nil {
		mention = *g.MentionOnly
	}
	if g.ReplyProbability != nil {
		p = *g.ReplyProbability
	}
	return mention, p
}
func (s *Service) handleGroupAICommand(bot *core.Bot, ev *core.Event, arg string) bool {
	if !ev.IsGroup() {
		bot.Reply(ev, "群 AI 设置请由主人在目标群里操作；模型提供商请在私聊用 /provider 和 /model 配置")
		return true
	}
	f := strings.Fields(arg)
	cmd := "settings"
	if len(f) > 0 {
		cmd = f[0]
	}
	usage := "群 AI 独立设置（仅主人可修改）\n/ai on 开启本群 AI\n/ai off 关闭本群 AI\n/ai mention on|off 仅@触发\n/ai probability 0到1 普通消息回复概率\n/ai reset 恢复全局触发配置\n/ai settings 或 /ai status 查看设置"
	if cmd == "settings" || cmd == "status" {
		g, err := s.groupAISettings(ev.GroupID)
		if err != nil {
			bot.Reply(ev, "读取群 AI 设置失败")
			return true
		}
		mention, p := s.groupTriggers(g)
		state := "关闭"
		if g.Enabled {
			state = "开启"
		}
		ready := "未配置，请主人私聊 /provider、/model"
		if s.HasDefaultModel() {
			ready = "已配置"
		}
		bot.Reply(ev, fmt.Sprintf("本群 AI：%s（仅主人可开关）\n仅@触发：%v\n普通消息回复概率：%g\n模型：%s\n开启 bot 与群娱不会开启 AI。\n%s", state, mention, p, ready, usage))
		return true
	}
	// Ignore caller flags, group roles, and message claims: only the actual sender counts.
	if ev.UserID <= 0 || fmtID(ev.UserID) != bot.Cfg.Bot.MasterID {
		if !core.FeatureOn(s.cfg.Features.SilentDeny) {
			bot.Reply(ev, "仅主人可修改群 AI 设置")
		}
		return true
	}
	owner, err := strconv.ParseInt(bot.Cfg.Bot.MasterID, 10, 64)
	if err != nil || owner <= 0 {
		bot.Reply(ev, "主人配置无效")
		return true
	}
	patch := perm.GroupAIPatch{}
	reply := "已更新本群 AI 设置"
	value := len(f) == 2
	switch cmd {
	case "on", "开":
		if len(f) != 1 {
			bot.Reply(ev, usage)
			return true
		}
		if !s.HasDefaultModel() {
			bot.Reply(ev, "群 AI 尚未开启：请主人先私聊用 /provider 配置提供商、用 /model 选择模型，再回本群发送 /ai on")
			return true
		}
		v := true
		patch.Enabled = &v
		reply = "主人已开启本群 AI；使用 /ai settings 查看触发设置"
	case "off", "关":
		if len(f) != 1 {
			bot.Reply(ev, usage)
			return true
		}
		v := false
		patch.Enabled = &v
		reply = "主人已关闭本群 AI"
	case "mention":
		if !value || (f[1] != "on" && f[1] != "off") {
			bot.Reply(ev, usage)
			return true
		}
		v := f[1] == "on"
		patch.MentionOnly = &v
	case "probability":
		if !value {
			bot.Reply(ev, usage)
			return true
		}
		v, e := strconv.ParseFloat(f[1], 64)
		if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			bot.Reply(ev, "回复概率须为0到1之间的数值，例如 /ai probability 0.1")
			return true
		}
		patch.ReplyProbability = &v
	case "reset":
		if len(f) != 1 {
			bot.Reply(ev, usage)
			return true
		}
		patch.ResetTriggers = true
		reply = "已恢复全局触发设置，AI 开关保持不变"
	default:
		bot.Reply(ev, usage)
		return true
	}
	if err = s.perm.ConfigureGroupAI(ev.GroupID, ev.UserID, owner, patch); err != nil {
		bot.Reply(ev, "保存群 AI 设置失败："+err.Error())
		return true
	}
	bot.Reply(ev, reply)
	return true
}
