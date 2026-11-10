// Package community provides deterministic menus, games and community management.
package community

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"gobot/core"
	data "gobot/store/community"
	"gobot/store/perm"
)

type Service struct {
	bot             *core.Bot
	store           *data.Store
	perm            *perm.Store
	friendMu        sync.Mutex
	friendRequests  map[string]*friendConfirmation
	friendTimeout   time.Duration
	friendSlots     chan struct{}
	loc             *time.Location
	now             func() time.Time
	moderationSlots chan struct{}
}

func New(bot *core.Bot, store *data.Store, permissions *perm.Store) (*Service, error) {
	zone := bot.Cfg.Community.Timezone
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("群娱时区: %w", err)
	}
	return &Service{bot: bot, store: store, perm: permissions, friendRequests: make(map[string]*friendConfirmation), friendTimeout: 120 * time.Second, friendSlots: make(chan struct{}, 8), loc: loc, now: time.Now, moderationSlots: make(chan struct{}, 8)}, nil
}
func (s *Service) Name() string          { return "community" }
func id(v int64) string                  { return strconv.FormatInt(v, 10) }
func (s *Service) master(uid int64) bool { return uid > 0 && id(uid) == s.bot.Cfg.Bot.MasterID }
func (s *Service) configuredAdmin(uid int64) bool {
	for _, v := range s.bot.Cfg.Bot.AdminIDs {
		if v == id(uid) {
			return true
		}
	}
	return false
}

// Level never derives identity from message text or a cached user supplied role.
// QQ roles are fetched fresh for authority checks; API failure cannot grant a role.
func (s *Service) level(gid, uid int64) int {
	if s.master(uid) {
		return 60
	}
	level := 0
	if s.configuredAdmin(uid) {
		level = 50
	}
	if v, err := s.store.Role(0, uid); err == nil && v == 50 {
		level = 50
	}
	if r, err := s.perm.GetUserRole(uid); err == nil && (r == perm.RoleOwner || r == perm.RoleAdmin) {
		level = 50
	}
	if gid <= 0 {
		return level
	}
	if v, err := s.store.Role(gid, uid); err == nil && v == 30 && level < 30 {
		level = 30
	}
	if r, err := s.perm.GetGroupRole(gid, uid); err == nil && r == perm.RoleAdmin && level < 30 {
		level = 30
	}
	if c, ok := s.bot.Sender.(core.GroupAdminClient); ok {
		if info, err := c.GetGroupMemberInfo(gid, uid); err == nil {
			switch info["role"] {
			case "owner":
				if level < 40 {
					level = 40
				}
			case "admin":
				if level < 20 {
					level = 20
				}
			}
		}
	}
	return level
}

// AllowEvent also gates the fast control path, before any other service sees it.
func (s *Service) AllowEvent(ev *core.Event) bool {
	if s.master(ev.UserID) {
		return true
	}
	if ev.Type != "message" && ev.Type != "request" && ev.NoticeType != "group_upload" && ev.NoticeType != "offline_file" {
		return true
	}
	if r, err := s.perm.GetUserRole(ev.UserID); err != nil || r == perm.RoleBanned {
		return false
	}
	scopes := []int64{0}
	if ev.GroupID > 0 {
		scopes = append(scopes, ev.GroupID)
	}
	for _, scope := range scopes {
		for _, kind := range []string{"black", "gray"} {
			v, err := s.store.Listed(scope, ev.UserID, kind)
			if err != nil || v {
				return false
			}
		}
	}
	if ev.IsPrivate() {
		v, err := s.store.Listed(0, ev.UserID, "private")
		if err != nil || v {
			return false
		}
	}
	return true
}

var featureNames = map[string]string{"签到": "sign", "运势": "fortune", "骰子": "dice", "转账": "transfer", "银行": "bank", "排行": "rank", "发言": "speech", "农场": "farm", "坐骑": "mount", "钓鱼": "fishing", "回复": "reply", "关键词": "keyword", "事件": "event", "群管": "management"}
var featureOrder = []string{"签到", "运势", "骰子", "转账", "银行", "排行", "发言", "农场", "坐骑", "钓鱼", "回复", "关键词", "事件", "群管"}

func featureKey(v string) string {
	if k := featureNames[v]; k != "" {
		return k
	}
	for _, k := range featureNames {
		if k == v {
			return k
		}
	}
	return ""
}
func (s *Service) enabled(gid int64, key string) bool {
	global, err := s.store.Settings(0)
	if err != nil {
		return false
	}
	on := true
	if v, ok := global.Features[key]; ok {
		on = v
	}
	if gid > 0 {
		g, err := s.store.Settings(gid)
		if err != nil {
			return false
		}
		if v, ok := g.Features[key]; ok {
			on = v
		}
	}
	return on
}
func (s *Service) scope(ev *core.Event) string {
	if !ev.IsGroup() {
		return "private:" + id(ev.UserID)
	}
	g, err := s.store.Settings(ev.GroupID)
	if err != nil {
		return "group:" + id(ev.GroupID)
	}
	mode := g.Mode
	if mode == "" {
		mode = s.bot.Cfg.Community.DefaultMode
	}
	if mode == "global" {
		return "global"
	}
	return "group:" + id(ev.GroupID)
}
func (s *Service) targetGroup(ev *core.Event) (int64, error) {
	if ev.IsGroup() {
		return ev.GroupID, nil
	}
	gid, err := s.store.Selected(ev.UserID)
	if err != nil {
		return 0, err
	}
	if gid <= 0 {
		return 0, errors.New("先用「选择管理群 群号」选择目标群")
	}
	if !s.member(gid, ev.UserID) && s.level(0, ev.UserID) < 50 {
		return 0, errors.New("已失去目标群的成员资格，请重新选择管理群")
	}
	return gid, nil
}
func (s *Service) member(gid, uid int64) bool {
	c, ok := s.bot.Sender.(core.GroupAdminClient)
	if !ok {
		return false
	}
	info, err := c.GetGroupMemberInfo(gid, uid)
	if err != nil {
		return false
	}
	r, _ := info["role"].(string)
	return r == "member" || r == "admin" || r == "owner"
}
func (s *Service) respond(ev *core.Event, msg string, err error) {
	if err != nil {
		msg = "操作失败：" + err.Error()
	}
	s.bot.Reply(ev, msg)
}
func (s *Service) plain(ev *core.Event, msg string) {
	if ev.IsGroup() {
		_ = s.bot.Sender.SendGroupMsg(ev.GroupID, 0, []core.Segment{core.TextSegment(msg)})
	} else {
		_ = s.bot.Sender.SendPrivateMsg(ev.UserID, []core.Segment{core.TextSegment(msg)})
	}
}
func (s *Service) deny(ev *core.Event) {
	if !ev.IsGroup() || !core.FeatureOn(s.bot.Cfg.Features.SilentDeny) {
		s.bot.Reply(ev, "无权执行此命令")
	}
}

func (s *Service) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if !s.AllowEvent(ev) {
		return true
	}
	if ev.Type == "notice" {
		return s.notice(ev)
	}
	if ev.Type == "request" && ev.RequestType == "friend" {
		return s.friendRequest(ctx, ev)
	}
	if ev.Type != "message" {
		if ev.GroupID > 0 && !s.perm.BotEnabled(ev.GroupID) {
			return true
		}
		return false
	}
	if ev.UserID <= 0 || ev.UserID == ev.SelfID {
		return true
	}
	text := strings.TrimSpace(ev.Text())
	if ev.IsGroup() && !s.perm.BotEnabled(ev.GroupID) {
		// Only these fixed local management commands can revive a disabled group.
		if text != "开启本群" && text != "/bot on" && text != "/bot 开" && text != "关闭本群" && text != "/bot off" {
			return true
		}
	}
	if ev.MessageID != 0 {
		key := fmt.Sprintf("message:%d:%s:%d:%d:%d", ev.SelfID, ev.DetailType, ev.GroupID, ev.UserID, ev.MessageID)
		ok, err := s.store.Claim(key, s.now())
		if err != nil {
			log.Printf("[community] dedup: %v", err)
			return true
		}
		if !ok {
			return true
		}
	}
	if s.management(ctx, ev, text) {
		return true
	}
	if ev.IsGroup() && s.enabled(ev.GroupID, "keyword") && s.keyword(ctx, ev, text) {
		return true
	}
	if ev.IsGroup() && s.enabled(ev.GroupID, "speech") {
		if _, err := s.speech(ev); err != nil {
			log.Printf("[community] speech: %v", err)
		}
	}
	if s.menu(ev, text) {
		return true
	}
	if s.entertainment(ev, text) {
		return true
	}
	// Slash commands belong to built-in services; reply rules cannot intercept them.
	if !strings.HasPrefix(text, "/") && s.enabled(ev.GroupID, "reply") {
		if s.customReply(ev, text) {
			return true
		}
	}
	s.relayPrivate(ev, text)
	return false
}

func (s *Service) speech(ev *core.Event) (string, error) {
	now := s.now().In(s.loc)
	day, month := now.Format("2006-01-02"), now.Format("2006-01")
	return s.store.Change(s.scope(ev), []int64{ev.UserID}, ev.UserID, "speech", func(m map[int64]*data.Account) (string, error) {
		a := m[ev.UserID]
		if a.SpeechDay != day {
			a.SpeechDay = day
			a.Today = 0
			a.Rewarded = 0
		}
		if a.SpeechMonth != month {
			a.SpeechMonth = month
			a.Month = 0
		}
		a.Today++
		a.Month++
		a.Total++
		if now.Unix()-a.LastSpeech >= 30 && a.Rewarded < 30 && a.Coins <= data.MaxMoney-2 {
			a.Coins += 2
			a.Rewarded++
			a.LastSpeech = now.Unix()
		}
		return "", nil
	})
}
