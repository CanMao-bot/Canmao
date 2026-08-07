package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gobot/pluginapi"
)

// 群管理员插件: 提供禁言/踢人/全体禁言/公告/群名片/群名/管理员设置等能力
type groupAdminPlugin struct {
	pluginapi.Base
	ga  pluginapi.GroupAdmin
	pm  pluginapi.Perm
	snd pluginapi.Sender
}

var Plugin = func() pluginapi.Plugin {
	return &groupAdminPlugin{}
}

func (g *groupAdminPlugin) Name() string { return "group-admin" }

func (g *groupAdminPlugin) Setup(caps *pluginapi.Capabilities) error {
	g.ga = caps.GroupAdmin
	g.pm = caps.Perm
	g.snd = caps.Sender
	return nil
}

// canAdmin 是否有管理权限: 主人 或 群管理员
func (g *groupAdminPlugin) canAdmin(groupID, userID int64) bool {
	if g.pm == nil {
		return false
	}
	return g.pm.IsMaster(userID) || g.pm.IsGroupAdmin(groupID, userID)
}

func (g *groupAdminPlugin) OnEvent(ctx context.Context, ev *pluginapi.Event) (string, bool) {
	if !ev.IsGroup {
		return "", false
	}
	t := strings.TrimSpace(ev.Message)
	if !strings.HasPrefix(t, "/") {
		return "", false
	}
	if !g.canAdmin(ev.GroupID, ev.UserID) {
		return "无权限执行群管理操作", true
	}
	return g.dispatch(ev, t)
}

func (g *groupAdminPlugin) dispatch(ev *pluginapi.Event, t string) (string, bool) {
	cmd, arg := splitCmd(t)
	switch cmd {
	case "/ban": // /ban <QQ> [分钟]
		return g.doBan(ev, arg)
	case "/unban": // /unban <QQ>
		return g.doUnban(ev, arg)
	case "/mute": // /mute 全体禁言
		return g.doWholeBan(ev, true)
	case "/unmute":
		return g.doWholeBan(ev, false)
	case "/kick": // /kick <QQ>
		return g.doKick(ev, arg)
	case "/notice": // /notice <内容>
		return g.doNotice(ev, arg)
	case "/setcard": // /setcard <QQ> <名片>
		return g.doSetCard(ev, arg)
	case "/setname": // /setname <群名>
		return g.doSetName(ev, arg)
	case "/setadmin": // /setadmin <QQ> 或 /setadmin <QQ> off
		return g.doSetAdmin(ev, arg)
	case "/memberlist", "/成员列表":
		return g.doMemberList(ev)
	}
	return "", false
}

func (g *groupAdminPlugin) doBan(ev *pluginapi.Event, arg string) (string, bool) {
	fields := strings.Fields(arg)
	if len(fields) < 1 {
		return "用法: /ban <QQ号> [分钟(默认10)]", true
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return "QQ号格式错误", true
	}
	duration := 600
	if len(fields) > 1 {
		if m, err := strconv.Atoi(fields[1]); err == nil && m > 0 {
			duration = m * 60
		}
	}
	if err := g.ga.SetGroupBan(ev.GroupID, uid, duration); err != nil {
		return "禁言失败: " + err.Error(), true
	}
	return fmt.Sprintf("✅ 已禁言 %d (%d分钟)", uid, duration/60), true
}

func (g *groupAdminPlugin) doUnban(ev *pluginapi.Event, arg string) (string, bool) {
	uid, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil {
		return "用法: /unban <QQ号>", true
	}
	if err := g.ga.SetGroupBan(ev.GroupID, uid, 0); err != nil {
		return "解除禁言失败: " + err.Error(), true
	}
	return fmt.Sprintf("✅ 已解除 %d 的禁言", uid), true
}

func (g *groupAdminPlugin) doWholeBan(ev *pluginapi.Event, enable bool) (string, bool) {
	if err := g.ga.SetGroupWholeBan(ev.GroupID, enable); err != nil {
		state := "开启"
		if !enable {
			state = "解除"
		}
		return state + "全体禁言失败: " + err.Error(), true
	}
	if enable {
		return "✅ 已开启全体禁言", true
	}
	return "✅ 已解除全体禁言", true
}

func (g *groupAdminPlugin) doKick(ev *pluginapi.Event, arg string) (string, bool) {
	fields := strings.Fields(arg)
	if len(fields) < 1 {
		return "用法: /kick <QQ号>", true
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return "QQ号格式错误", true
	}
	reject := false
	if len(fields) > 1 && fields[1] == "reject" {
		reject = true
	}
	if err := g.ga.SetGroupKick(ev.GroupID, uid, reject); err != nil {
		return "踢人失败: " + err.Error(), true
	}
	return fmt.Sprintf("✅ 已移出成员 %d", uid), true
}

func (g *groupAdminPlugin) doNotice(ev *pluginapi.Event, arg string) (string, bool) {
	if strings.TrimSpace(arg) == "" {
		return "用法: /notice <公告内容>", true
	}
	if err := g.ga.SendGroupNotice(ev.GroupID, arg); err != nil {
		return "发布公告失败: " + err.Error(), true
	}
	return "✅ 公告已发布", true
}

func (g *groupAdminPlugin) doSetCard(ev *pluginapi.Event, arg string) (string, bool) {
	fields := strings.Fields(arg)
	if len(fields) < 2 {
		return "用法: /setcard <QQ号> <名片>", true
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return "QQ号格式错误", true
	}
	card := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
	if err := g.ga.SetGroupCard(ev.GroupID, uid, card); err != nil {
		return "设置名片失败: " + err.Error(), true
	}
	return fmt.Sprintf("✅ 已将 %d 的名片设为 %s", uid, card), true
}

func (g *groupAdminPlugin) doSetName(ev *pluginapi.Event, arg string) (string, bool) {
	if strings.TrimSpace(arg) == "" {
		return "用法: /setname <新群名>", true
	}
	if err := g.ga.SetGroupName(ev.GroupID, arg); err != nil {
		return "设置群名失败: " + err.Error(), true
	}
	return "✅ 群名已修改为: " + arg, true
}

func (g *groupAdminPlugin) doSetAdmin(ev *pluginapi.Event, arg string) (string, bool) {
	fields := strings.Fields(arg)
	if len(fields) < 1 {
		return "用法: /setadmin <QQ号> [off]", true
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return "QQ号格式错误", true
	}
	enable := true
	if len(fields) > 1 && (fields[1] == "off" || fields[1] == "取消") {
		enable = false
	}
	if err := g.ga.SetGroupAdmin(ev.GroupID, uid, enable); err != nil {
		return "设置管理员失败: " + err.Error(), true
	}
	if enable {
		return fmt.Sprintf("✅ 已将 %d 设为群管理员", uid), true
	}
	return fmt.Sprintf("✅ 已取消 %d 的管理员", uid), true
}

func (g *groupAdminPlugin) doMemberList(ev *pluginapi.Event) (string, bool) {
	list, err := g.ga.GetGroupMemberList(ev.GroupID)
	if err != nil {
		return "获取成员列表失败: " + err.Error(), true
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("👥 群成员 %d 人:\n", len(list)))
	for _, m := range list {
		uid, _ := toInt64(m["user_id"])
		nick, _ := m["nickname"].(string)
		card, _ := m["card"].(string)
		role, _ := m["role"].(string)
		if card != "" {
			nick = card
		}
		roleMark := ""
		switch role {
		case "owner":
			roleMark = "👑"
		case "admin":
			roleMark = "🛡"
		}
		b.WriteString(fmt.Sprintf("%s %s (%d)\n", roleMark, nick, uid))
	}
	return b.String(), true
}

func (g *groupAdminPlugin) Tools() []pluginapi.Tool {
	return []pluginapi.Tool{
		{
			Name:        "group_ban",
			Description: "禁言指定QQ一段时间(秒)",
			Risk:        4, // 极危, 仅主人
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
				"user_id":  {Type: "integer", Description: "被禁言QQ", Required: true},
				"duration": {Type: "integer", Description: "禁言秒数", Required: true},
			},
			Call: g.toolBan,
		},
		{
			Name:        "group_whole_ban",
			Description: "开启或解除全体禁言",
			Risk:        4,
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
				"enable":   {Type: "boolean", Description: "true=开启 false=解除", Required: true},
			},
			Call: g.toolWholeBan,
		},
		{
			Name:        "group_kick",
			Description: "将成员移出群",
			Risk:        4,
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
				"user_id":  {Type: "integer", Description: "被移出QQ", Required: true},
			},
			Call: g.toolKick,
		},
		{
			Name:        "group_notice",
			Description: "发布群公告",
			Risk:        3,
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
				"content":  {Type: "string", Description: "公告内容", Required: true},
			},
			Call: g.toolNotice,
		},
		{
			Name:        "group_set_card",
			Description: "设置群成员名片",
			Risk:        3,
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
				"user_id":  {Type: "integer", Description: "QQ号", Required: true},
				"card":     {Type: "string", Description: "名片内容", Required: true},
			},
			Call: g.toolSetCard,
		},
		{
			Name:        "group_set_name",
			Description: "修改群名称",
			Risk:        3,
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
				"name":     {Type: "string", Description: "新群名", Required: true},
			},
			Call: g.toolSetName,
		},
		{
			Name:        "group_member_list",
			Description: "获取群成员列表",
			Risk:        1,
			Parameters: map[string]*pluginapi.Param{
				"group_id": {Type: "integer", Description: "群号", Required: true},
			},
			Call: g.toolMemberList,
		},
	}
}

// ---- AI 工具实现 ----

func (g *groupAdminPlugin) toolBan(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	uid := int64(args["user_id"].(float64))
	dur := int(args["duration"].(float64))
	if err := g.ga.SetGroupBan(gid, uid, dur); err != nil {
		return "禁言失败: " + err.Error(), nil
	}
	return fmt.Sprintf("已禁言 %d %d秒", uid, dur), nil
}

func (g *groupAdminPlugin) toolWholeBan(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	enable, _ := args["enable"].(bool)
	if err := g.ga.SetGroupWholeBan(gid, enable); err != nil {
		return "操作失败: " + err.Error(), nil
	}
	if enable {
		return "已开启全体禁言", nil
	}
	return "已解除全体禁言", nil
}

func (g *groupAdminPlugin) toolKick(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	uid := int64(args["user_id"].(float64))
	if err := g.ga.SetGroupKick(gid, uid, false); err != nil {
		return "踢人失败: " + err.Error(), nil
	}
	return fmt.Sprintf("已移出成员 %d", uid), nil
}

func (g *groupAdminPlugin) toolNotice(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	content, _ := args["content"].(string)
	if err := g.ga.SendGroupNotice(gid, content); err != nil {
		return "发布公告失败: " + err.Error(), nil
	}
	return "公告已发布", nil
}

func (g *groupAdminPlugin) toolSetCard(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	uid := int64(args["user_id"].(float64))
	card, _ := args["card"].(string)
	if err := g.ga.SetGroupCard(gid, uid, card); err != nil {
		return "设置名片失败: " + err.Error(), nil
	}
	return fmt.Sprintf("已将 %d 的名片设为 %s", uid, card), nil
}

func (g *groupAdminPlugin) toolSetName(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	name, _ := args["name"].(string)
	if err := g.ga.SetGroupName(gid, name); err != nil {
		return "设置群名失败: " + err.Error(), nil
	}
	return "群名已修改为: " + name, nil
}

func (g *groupAdminPlugin) toolMemberList(ctx context.Context, args map[string]interface{}) (string, error) {
	gid := int64(args["group_id"].(float64))
	list, err := g.ga.GetGroupMemberList(gid)
	if err != nil {
		return "获取失败: " + err.Error(), nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("群成员 %d 人:\n", len(list)))
	for _, m := range list {
		uid, _ := toInt64(m["user_id"])
		nick, _ := m["nickname"].(string)
		if card, ok := m["card"].(string); ok && card != "" {
			nick = card
		}
		b.WriteString(fmt.Sprintf("- %s (%d)\n", nick, uid))
	}
	return b.String(), nil
}

// ---- 工具函数 ----

func splitCmd(text string) (cmd, arg string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", ""
	}
	cmd = fields[0]
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	return cmd, arg
}

func toInt64(v interface{}) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int64:
		return x, true
	case string:
		i, err := strconv.ParseInt(x, 10, 64)
		return i, err == nil
	}
	return 0, false
}
