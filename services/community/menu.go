package community

import (
	"fmt"
	"sort"
	"strings"

	"gobot/core"
	data "gobot/store/community"
)

type menuItem struct {
	category, feature, usage string
	level                    int
	group                    bool
}

var menuItems = []menuItem{
	{"娱乐", "sign", "签到 / 查询", 0, false},
	{"娱乐", "fortune", "今日人品 / 抽签", 0, false},
	{"娱乐", "dice", ".rd / .r3d6 / .2r5d20+3", 0, false},
	{"娱乐", "transfer", "赠送 对方QQ 数量", 0, true},
	{"娱乐", "bank", "存款 数量 / 取款 数量 / 我的存款", 0, false},
	{"娱乐", "speech", "我的发言", 0, true},
	{"排行", "rank", "金币排行 / 经验排行 / 银行排行 / 签到排行 / 钓鱼排行", 0, false},
	{"排行", "rank", "今日发言排行 / 本月发言排行 / 累计发言排行", 0, true},
	{"农场", "farm", "我的农场 / 种子商店 / 开耕 格号", 0, false},
	{"农场", "farm", "种植 格号 作物 / 全部种植 作物 / 农场收割 格号 / 农场全部收割", 0, false},
	{"坐骑", "mount", "坐骑商店 / 购买坐骑 名称 / 我的坐骑 / 当前坐骑 / 设置坐骑 名称 / 取消坐骑", 0, false},
	{"钓鱼", "fishing", "鱼竿商店 / 购买鱼竿 名称 / 我的鱼竿 / 钓鱼 / 提竿", 0, false},
	{"群管", "management", "禁言 QQ 分钟 / 解禁 QQ / 移出 QQ / 群名片 QQ 内容", 20, true},
	{"群管", "management", "群名 内容 / 全体禁言 / 全体解禁 / 群公告 内容 / 撤回（引用目标消息）", 20, true},
	{"群管", "management", "警告 QQ / 查警告 QQ / 清空警告 QQ", 20, true},
	{"管理", "", "功能开关 / 开启功能 功能名 / 关闭功能 功能名 / 开启本群 / 关闭本群", 30, false},
	{"管理", "", "数据模式 / 数据模式 global|separate / 添加后台管理 QQ / 取消后台管理 QQ / 后台管理列表", 40, false},
	{"管理", "", "添加名单 black|white|gray QQ / 删除名单 类型 QQ / 查看名单 类型", 30, false},
	{"管理", "", "添加回复 精准|模糊 群聊|私聊|全部 权限 关键词|正文 / 删除回复 关键词 / 回复列表", 30, false},
	{"管理", "", "添加关键词 reply|warn|ban|kick|recall|black 关键词|正文 / 删除关键词 关键词 / 关键词列表", 30, false},
	{"管理", "", "设置事件 welcome|leave|admin|card 模板 / 删除事件 类型 / 事件列表", 30, false},
	{"管理", "", "设置菜单 all 标题$换行$$菜单$ / 删除菜单 类别 / 菜单设置", 30, false},
	{"私聊", "", "群列表 / 选择管理群 群号 / 当前管理群；选择后可使用管理命令", 30, false},
	{"主人", "", "添加超管 QQ / 取消超管 QQ / 超管列表 / 全局功能开关", 60, false},
	{"主人", "", "/ai on|off 开关本群AI / /ai settings 独立设置 / /ai mention on|off / /ai probability 0到1（在目标群操作）", 60, false},
	{"主人", "", "添加全局回复 … / 删除全局回复 关键词 / 全局回复列表", 60, false},
	{"主人", "", "添加全局名单 类型 QQ / 删除全局名单 类型 QQ / 查看全局名单 类型", 60, false},
	{"主人", "", "发放金币 QQ 数量 / 扣除金币 QQ 数量 / 全局审计", 60, false},
	{"主人", "", "私聊屏蔽 QQ / 取消私聊屏蔽 QQ / 退出群聊 / 设置群管理 QQ / 取消群管理 QQ", 60, false},
	{"主人", "", "开启私聊转发 / 关闭私聊转发 / 私聊转发状态 / 回复私聊 QQ 内容（仅私聊）", 60, false},
}

func validMenu(key string) bool {
	switch key {
	case "all", "娱乐", "排行", "农场", "坐骑", "钓鱼", "群管", "管理", "私聊", "主人":
		return true
	}
	return false
}

func (s *Service) menu(ev *core.Event, text string) bool {
	category := ""
	switch text {
	case "功能", "菜单", "功能菜单", "/群娱", "/community":
		category = "all"
	case "娱乐功能":
		category = "娱乐"
	case "农场功能":
		category = "农场"
	case "坐骑功能":
		category = "坐骑"
	case "钓鱼系统":
		category = "钓鱼"
	case "排行功能":
		category = "排行"
	case "群管功能":
		category = "群管"
	case "管理功能", "后台管理功能":
		category = "管理"
	case "主人功能":
		category = "主人"
	case "超管功能":
		category = "管理"
	case "私聊功能":
		category = "私聊"
	case "我的权限":
		s.bot.Reply(ev, fmt.Sprintf("你的权限：%d（成员0 / QQ管理20 / 后台30 / QQ群主40 / 超管50 / 主人60）", s.level(ev.GroupID, ev.UserID)))
		return true
	case "我的警告":
		n, err := s.store.Warn(ev.GroupID, ev.UserID, 0)
		s.respond(ev, fmt.Sprintf("本群警告：%d 次", n), err)
		return true
	default:
		return false
	}
	level := s.level(ev.GroupID, ev.UserID)
	lines := []string{"罐头喵 · 功能菜单", "娱乐数据范围：" + s.scope(ev)}
	for _, m := range menuItems {
		if m.level > level || (m.group && !ev.IsGroup() && m.level == 0) {
			continue
		}
		if category != "all" && m.category != category {
			continue
		}
		if m.category == "私聊" && ev.IsGroup() {
			continue
		}
		if m.feature != "" && !s.enabled(ev.GroupID, m.feature) {
			continue
		}
		lines = append(lines, "【"+m.category+"】"+m.usage)
	}
	if len(lines) == 2 {
		lines = append(lines, "当前场景或权限下没有已开启的项目")
	}
	lines = append(lines, "我的权限 / 运行状态；AI 帮助用 /help")
	body := strings.Join(lines, "\n")
	global, err := s.store.Settings(0)
	if err != nil {
		return true
	}
	template := global.Menus[category]
	if ev.IsGroup() {
		g, err := s.store.Settings(ev.GroupID)
		if err != nil {
			return true
		}
		if v, ok := g.Menus[category]; ok {
			template = v
		}
	}
	if template != "" {
		body = strings.ReplaceAll(s.render(template, ev), "$菜单$", body)
		s.plain(ev, body)
	} else {
		s.bot.Reply(ev, body)
	}
	return true
}

// Templates are rendered as literal text segments. No CQ, file, network or action interpreter.
func (s *Service) render(text string, ev *core.Event) string {
	now := s.now().In(s.loc)
	return strings.NewReplacer("$换行$", "\n", "$QQ$", id(ev.UserID), "$群号$", id(ev.GroupID), "$时间$", now.Format("2006-01-02 15:04:05"), "$昵称$", id(ev.UserID), "$操作者$", id(ev.OperatorID)).Replace(text)
}
func (s *Service) customReply(ev *core.Event, text string) bool {
	global, err := s.store.Settings(0)
	if err != nil {
		return true
	}
	rules := map[string]data.Rule{}
	for _, r := range global.Replies {
		rules[r.Key] = r
	}
	if ev.IsGroup() {
		g, err := s.store.Settings(ev.GroupID)
		if err != nil {
			return true
		}
		for _, r := range g.Replies {
			rules[r.Key] = r
		}
	}
	list := make([]data.Rule, 0, len(rules))
	for _, r := range rules {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Fuzzy != list[j].Fuzzy {
			return !list[i].Fuzzy
		}
		if len(list[i].Key) != len(list[j].Key) {
			return len(list[i].Key) > len(list[j].Key)
		}
		return list[i].Key < list[j].Key
	})
	level := -1
	for _, r := range list {
		if (r.Scene == "group" && !ev.IsGroup()) || (r.Scene == "private" && !ev.IsPrivate()) {
			continue
		}
		if text != r.Key && !(r.Fuzzy && strings.Contains(text, r.Key)) {
			continue
		}
		if r.MinLevel > 0 {
			if level < 0 {
				level = s.level(ev.GroupID, ev.UserID)
			}
			if level < r.MinLevel {
				continue
			}
		}
		s.plain(ev, s.render(r.Text, ev))
		return true
	}
	return false
}
