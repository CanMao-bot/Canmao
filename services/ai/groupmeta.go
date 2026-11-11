package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gobot/core"
)

// groupMetaTTL 群 meta 信息缓存时长
const groupMetaTTL = 10 * time.Minute

// groupMetaMaxAdmins 环境信息里列出的管理员最大人数
const groupMetaMaxAdmins = 8

// selfID bot 自身 QQ(配置里为字符串)
func (s *Service) selfID() int64 {
	id, _ := strconv.ParseInt(s.bot.Cfg.OneBot.SelfID, 10, 64)
	return id
}

// ownerIdentityTxt 主人/管理员的身份说明(供 AI 上下文感知, 避免自主操作误伤)
func (s *Service) ownerIdentityTxt() string {
	var parts []string
	if m := s.bot.Cfg.Bot.MasterID; m != "" {
		parts = append(parts, "主人(最高权限): QQ "+m)
	}
	if len(s.bot.Cfg.Bot.AdminIDs) > 0 {
		parts = append(parts, "管理员: "+strings.Join(s.bot.Cfg.Bot.AdminIDs, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "=== 权限身份 ===\n" + strings.Join(parts, "。") +
		"。\n自主执行任何涉及禁言、改昵称、踢人等群管理操作时, 绝不能对主人和管理员执行。"
}

// roleText 群成员身份中文
func roleText(role string) string {
	switch role {
	case "owner":
		return "群主"
	case "admin":
		return "管理员"
	default:
		return "成员"
	}
}

func int64From(v interface{}) int64 {
	switch n := v.(type) {
	case json.Number:
		id, _ := n.Int64()
		return id
	case float64:
		return int64(n)
	case int64:
		return n
	case string:
		id, _ := strconv.ParseInt(n, 10, 64)
		return id
	}
	return 0
}

// memberDisplay 群成员展示名(群名片优先)
func memberDisplay(m map[string]interface{}) string {
	if card, _ := m["card"].(string); card != "" {
		return card
	}
	if nick, _ := m["nickname"].(string); nick != "" {
		return nick
	}
	return int64ToStr(int64From(m["user_id"]))
}

// groupMetaOf 群 meta 信息: 自己的群身份/群昵称/头衔 + 群主/管理员名单(缓存 10 分钟, 低调注入)
func (s *Service) groupMetaOf(groupID int64) string {
	s.groupMu.Lock()
	if s.groupMetas == nil {
		s.groupMetas = map[int64]cachedGroupMeta{}
	}
	if c, ok := s.groupMetas[groupID]; ok && time.Since(c.at) < groupMetaTTL {
		text := c.text
		s.groupMu.Unlock()
		return text
	}
	s.groupMu.Unlock()

	text := s.fetchGroupMeta(groupID)

	s.groupMu.Lock()
	s.groupMetas[groupID] = cachedGroupMeta{text: text, at: time.Now()}
	s.groupMu.Unlock()
	return text
}

func (s *Service) fetchGroupMeta(groupID int64) string {
	gac, ok := s.bot.Sender.(core.GroupAdminClient)
	if !ok {
		return ""
	}
	info, _ := gac.GetGroupMemberInfo(groupID, s.selfID())
	list, _ := gac.GetGroupMemberList(groupID)
	return formatGroupMeta(s.selfID(), info, list)
}

func formatGroupMeta(selfID int64, info map[string]interface{}, list []map[string]interface{}) string {
	var b strings.Builder

	// 自己在群里的身份/群昵称/头衔
	if info != nil {
		role, _ := info["role"].(string)
		card, _ := info["card"].(string)
		title, _ := info["title"].(string)
		b.WriteString("你的群身份: " + roleText(role))
		if card != "" {
			b.WriteString(", 群昵称「" + card + "」")
		}
		if title != "" {
			b.WriteString(", 头衔「" + title + "」")
		}
		b.WriteString("。")
	}

	// 群主/管理员名单(不含自己)
	if list != nil {
		var owners, admins []string
		adminTotal := 0
		for _, m := range list {
			role, _ := m["role"].(string)
			if role != "owner" && role != "admin" {
				continue
			}
			uid := int64From(m["user_id"])
			if uid == selfID {
				continue
			}
			item := fmt.Sprintf("%s(%d)", memberDisplay(m), uid)
			if role == "owner" {
				owners = append(owners, item)
			} else {
				adminTotal++
				if len(admins) < groupMetaMaxAdmins {
					admins = append(admins, item)
				}
			}
		}
		if len(owners) > 0 {
			b.WriteString(" 群主: " + strings.Join(owners, ", ") + "。")
		}
		if len(admins) > 0 {
			b.WriteString(" 管理员: " + strings.Join(admins, ", "))
			if adminTotal > len(admins) {
				b.WriteString(fmt.Sprintf(" 等%d人", adminTotal))
			}
			b.WriteString("。")
		}
	}
	return b.String()
}

// Environment enrichment must not consume three consecutive OneBot timeouts.
// Native calls run concurrently with one shared two-second deadline; stale cached
// values survive failures. Legacy senders retain their existing compatibility path.
func (s *Service) groupEnvironment(ctx context.Context, groupID int64, wantMeta bool) (string, string) {
	client, ok := s.bot.Sender.(core.QQAPIClient)
	if !ok {
		meta := ""
		if wantMeta {
			meta = s.groupMetaOf(groupID)
		}
		return s.groupNameOf(groupID), meta
	}
	s.groupMu.Lock()
	nameCache, nameOK := s.groupNames[groupID]
	metaCache, metaOK := s.groupMetas[groupID]
	s.groupMu.Unlock()
	if !wantMeta {
		metaCache = cachedGroupMeta{}
	}
	needName := !nameOK || time.Since(nameCache.at) >= groupMetaTTL
	needMeta := wantMeta && (!metaOK || time.Since(metaCache.at) >= groupMetaTTL)
	if !needName && !needMeta {
		return nameCache.name, metaCache.text
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var info map[string]interface{}
	var members []map[string]interface{}
	var name map[string]interface{}
	var nameErr, infoErr, listErr error
	call := func(action string, params map[string]interface{}, dst interface{}) error {
		raw, err := client.CallQQAPI(ctx, action, params, nil)
		if err != nil {
			return err
		}
		return decodeQQJSON(raw, dst)
	}
	if needName {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nameErr = call("get_group_info", map[string]interface{}{"group_id": groupID}, &name)
		}()
	}
	if needMeta {
		wg.Add(2)
		go func() {
			defer wg.Done()
			infoErr = call("get_group_member_info", map[string]interface{}{"group_id": groupID, "user_id": s.selfID()}, &info)
		}()
		go func() {
			defer wg.Done()
			listErr = call("get_group_member_list", map[string]interface{}{"group_id": groupID}, &members)
		}()
	}
	wg.Wait()
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if s.groupNames == nil {
		s.groupNames = map[int64]cachedGroupName{}
	}
	if s.groupMetas == nil {
		s.groupMetas = map[int64]cachedGroupMeta{}
	}
	if needName {
		at := time.Now()
		if nameErr == nil {
			nameCache.name, _ = name["group_name"].(string)
		} else {
			at = at.Add(-groupMetaTTL + 15*time.Second)
		}
		nameCache.at = at
		s.groupNames[groupID] = nameCache
	}
	if needMeta {
		at := time.Now()
		if infoErr == nil && listErr == nil {
			metaCache.text = formatGroupMeta(s.selfID(), info, members)
		} else {
			at = at.Add(-groupMetaTTL + 15*time.Second)
		}
		metaCache.at = at
		s.groupMetas[groupID] = metaCache
	}
	return nameCache.name, metaCache.text
}

// registerMemberInfoTool 注册群成员信息查询工具(他人头衔/身份等按需查询)
func (s *Service) registerMemberInfoTool() {
	t := NewTool("get_member_info", "查询群成员的信息(昵称、群名片、身份、专属头衔等)。需要了解某个群成员的身份或头衔时使用。",
		map[string]*ToolParam{
			"qq":       {Type: "integer", Description: "成员QQ号"},
			"group_id": {Type: "integer", Description: "群号(可选, 默认当前群)"},
		}, []string{"qq"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			uid := int64From(args["qq"])
			if uid <= 0 {
				return "错误: 请提供有效的 qq", nil
			}
			gid := int64From(args["group_id"])
			if gid <= 0 {
				if ev := core.EventFrom(ctx); ev != nil {
					gid = ev.GroupID
				}
			}
			if gid <= 0 {
				return "错误: 无法确定群号, 请提供 group_id", nil
			}
			gac, ok := s.bot.Sender.(core.GroupAdminClient)
			if !ok {
				return "查询失败: 当前连接不支持", nil
			}
			info, err := gac.GetGroupMemberInfo(gid, uid)
			if err != nil {
				return "查询失败: " + err.Error(), nil
			}
			role, _ := info["role"].(string)
			out := fmt.Sprintf("%s(QQ:%d) 身份:%s", memberDisplay(info), uid, roleText(role))
			if card, _ := info["card"].(string); card != "" {
				out += ", 群名片「" + card + "」"
			}
			if title, _ := info["title"].(string); title != "" {
				out += ", 头衔「" + title + "」"
			}
			return out, nil
		})
	t.Risk = RiskLow
	s.tools = append(s.tools, t)
}
