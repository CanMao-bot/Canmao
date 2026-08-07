package ai

import (
	"context"
	"strings"

	"gobot/core"
)

// userLevel 权限级别
type userLevel int

const (
	levelMember userLevel = iota
	levelAdmin
	levelMaster
)

// levelOf 判定用户权限级别
func (s *Service) levelOf(bot *core.Bot, ev *core.Event, isMaster bool) userLevel {
	if isMaster {
		return levelMaster
	}
	if ev.IsGroup() {
		if role, _ := s.perm.GetGroupRole(ev.GroupID, ev.UserID); role == "admin" {
			return levelAdmin
		}
	}
	if isAdmin(bot, ev.UserID) {
		return levelAdmin
	}
	return levelMember
}

// handleHelp 按权限返回帮助
func (s *Service) handleHelp(ctx context.Context, bot *core.Bot, ev *core.Event, isMaster bool) bool {
	lvl := s.levelOf(bot, ev, isMaster)
	bot.Reply(ev, s.renderHelp(lvl))
	return true
}

func (s *Service) renderHelp(lvl userLevel) string {
	var b strings.Builder
	name := "gobot"
	if s.bot != nil && s.bot.Cfg != nil {
		name = s.bot.Cfg.Bot.Name
	}
	b.WriteString("🤖 " + name + " 帮助\n")

	// 基础命令(所有人)
	b.WriteString("\n📝 基础命令:\n")
	b.WriteString("  /help 显示帮助\n")
	b.WriteString("  /ai status 查看本群 AI 状态\n")
	b.WriteString("  /clear 清除当前会话上下文\n")
	b.WriteString("  /compact 压缩当前会话(保留摘要)\n")
	b.WriteString("  /new 开启新会话\n")
	b.WriteString("  /sessions 查看历史会话\n")
	b.WriteString("  /resume <编号> 切换历史会话\n")

	if lvl >= levelAdmin {
		// 管理员命令
		b.WriteString("\n⚙️ 管理员命令:\n")
		b.WriteString("  /ai on 开启本群 AI\n")
		b.WriteString("  /ai off 关闭本群 AI\n")
		b.WriteString("  /bot on 开启本群 bot\n")
		b.WriteString("  /bot off 关闭本群 bot(全体静默)\n")
		b.WriteString("  /rename <标题> 重命名当前会话\n")
		b.WriteString("  /delete <编号> 删除会话\n")
	}

	if lvl >= levelMaster {
		// 主人专属
		b.WriteString("\n👑 主人专属:\n")
		b.WriteString("  模型提供商:\n")
		b.WriteString("    /provider 查看提供商\n")
		b.WriteString("    /provider add <名称> <BaseURL> <APIKey> 添加提供商\n")
		b.WriteString("    /provider proxy <名称> <代理URL> 设置代理(如 http://127.0.0.1:7890)\n")
		b.WriteString("    /provider use <名称> 切换提供商\n")
		b.WriteString("    /provider remove <名称> 删除提供商\n")
		b.WriteString("    /model list 获取模型列表\n")
		b.WriteString("    /model <ID> 设置全局默认模型\n")
		b.WriteString("    /model groups 查看用户组模型\n")
		b.WriteString("    /model group <组> <ID> 为用户组设置模型(主人/管理员/成员)\n")
		b.WriteString("  权限管理:\n")
		b.WriteString("    /grant <QQ号> <admin|member> 授权(群内)\n")
		b.WriteString("    /ban <QQ号> 拉黑(群内)\n")
		b.WriteString("    /allow list 查看永久允许\n")
		b.WriteString("    /取消信任 <工具名> 移除永久允许\n")
	}

	return b.String()
}
