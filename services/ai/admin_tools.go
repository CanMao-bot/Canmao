package ai

import (
	"context"
	"strconv"

	"gobot/core"
)

// registerAdminTools 注册群管理工具。
// 这些工具由"用户要求"触发, 全部为高风险, 走人工审批流程;
// 而心情驱动下 AI 自主执行的禁言/改昵称由 MoodManager.MaybePunishAggressor
// 直接调用 GroupAdminClient, 不走工具审批(属 AI 自主行为)。
func (s *Service) registerAdminTools() {
	gac := func() (core.GroupAdminClient, bool) {
		g, ok := s.bot.Sender.(core.GroupAdminClient)
		return g, ok
	}
	curGroup := func(ctx context.Context) (int64, bool) {
		if ev, ok := ctx.Value("event").(*core.Event); ok && ev != nil && ev.IsGroup() {
			return ev.GroupID, true
		}
		return 0, false
	}

	ban := NewTool("group_ban", "禁言群成员。需要主人/管理员确认。时长以秒计, 0 表示解除禁言。",
		map[string]*ToolParam{
			"group_id":    {Type: "integer", Description: "群号(可选, 默认当前群)"},
			"user_id":     {Type: "integer", Description: "要禁言成员的QQ号"},
			"duration_sec": {Type: "integer", Description: "禁言时长(秒), 0=解除禁言"},
		}, []string{"user_id"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			g, ok := gac()
			if !ok {
				return "禁言失败: 当前连接不支持群管理操作", nil
			}
			gid := int64From(args["group_id"])
			if gid <= 0 {
				var ok2 bool
				if gid, ok2 = curGroup(ctx); !ok2 {
					return "禁言失败: 无法确定群号, 请提供 group_id", nil
				}
			}
			uid := int64From(args["user_id"])
			if uid <= 0 {
				return "禁言失败: 请提供有效的 user_id", nil
			}
			dur := int(int64From(args["duration_sec"]))
			if err := g.SetGroupBan(gid, uid, dur); err != nil {
				return "禁言失败: " + err.Error(), nil
			}
			if dur <= 0 {
				return "✅ 已解除禁言 " + strconv.FormatInt(uid, 10), nil
			}
			return "✅ 已禁言 " + strconv.FormatInt(uid, 10) + " (" + strconv.Itoa(dur) + "秒)", nil
		})
	ban.Risk = RiskHigh
	s.tools = append(s.tools, ban)

	kick := NewTool("group_kick", "把群成员移出群聊。需要主人/管理员确认。",
		map[string]*ToolParam{
			"group_id":           {Type: "integer", Description: "群号(可选, 默认当前群)"},
			"user_id":            {Type: "integer", Description: "要移出的成员QQ号"},
			"reject_add_request": {Type: "boolean", Description: "是否拒绝该成员再次加群(可选, 默认false)"},
		}, []string{"user_id"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			g, ok := gac()
			if !ok {
				return "踢人失败: 当前连接不支持群管理操作", nil
			}
			gid := int64From(args["group_id"])
			if gid <= 0 {
				var ok2 bool
				if gid, ok2 = curGroup(ctx); !ok2 {
					return "踢人失败: 无法确定群号, 请提供 group_id", nil
				}
			}
			uid := int64From(args["user_id"])
			if uid <= 0 {
				return "踢人失败: 请提供有效的 user_id", nil
			}
			reject, _ := args["reject_add_request"].(bool)
			if err := g.SetGroupKick(gid, uid, reject); err != nil {
				return "踢人失败: " + err.Error(), nil
			}
			return "✅ 已将 " + strconv.FormatInt(uid, 10) + " 移出群聊", nil
		})
	kick.Risk = RiskHigh
	s.tools = append(s.tools, kick)

	setCard := NewTool("group_set_card", "修改群成员的群昵称(群名片)。需要主人/管理员确认。",
		map[string]*ToolParam{
			"group_id": {Type: "integer", Description: "群号(可选, 默认当前群)"},
			"user_id":  {Type: "integer", Description: "成员QQ号"},
			"card":     {Type: "string", Description: "新的群昵称, 空字符串表示恢复默认"},
		}, []string{"user_id", "card"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			g, ok := gac()
			if !ok {
				return "改昵称失败: 当前连接不支持群管理操作", nil
			}
			gid := int64From(args["group_id"])
			if gid <= 0 {
				var ok2 bool
				if gid, ok2 = curGroup(ctx); !ok2 {
					return "改昵称失败: 无法确定群号, 请提供 group_id", nil
				}
			}
			uid := int64From(args["user_id"])
			if uid <= 0 {
				return "改昵称失败: 请提供有效的 user_id", nil
			}
			card, _ := args["card"].(string)
			if err := g.SetGroupCard(gid, uid, card); err != nil {
				return "改昵称失败: " + err.Error(), nil
			}
			return "✅ 已将 " + strconv.FormatInt(uid, 10) + " 的群昵称设为「" + card + "」", nil
		})
	setCard.Risk = RiskHigh
	s.tools = append(s.tools, setCard)

	setName := NewTool("group_set_name", "修改群名称。需要主人/管理员确认。",
		map[string]*ToolParam{
			"group_id": {Type: "integer", Description: "群号(可选, 默认当前群)"},
			"name":     {Type: "string", Description: "新的群名称"},
		}, []string{"name"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			g, ok := gac()
			if !ok {
				return "改群名失败: 当前连接不支持群管理操作", nil
			}
			gid := int64From(args["group_id"])
			if gid <= 0 {
				var ok2 bool
				if gid, ok2 = curGroup(ctx); !ok2 {
					return "改群名失败: 无法确定群号, 请提供 group_id", nil
				}
			}
			name, _ := args["name"].(string)
			if name == "" {
				return "改群名失败: name 不能为空", nil
			}
			if err := g.SetGroupName(gid, name); err != nil {
				return "改群名失败: " + err.Error(), nil
			}
			return "✅ 群名称已改为「" + name + "」", nil
		})
	setName.Risk = RiskHigh
	s.tools = append(s.tools, setName)
}
