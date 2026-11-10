package community

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gobot/core"
	data "gobot/store/community"
	"gobot/store/perm"
)

type sent struct {
	gid, uid int64
	message  []core.Segment
}
type mock struct {
	mu                            sync.Mutex
	roles                         map[[2]int64]string
	out                           chan sent
	bans, kicks, deletes, friends int
	banGroup, banTarget           int64
	banDuration                   int
	leaves, adminChanges          int
	friendApproved                bool
	msg                           *core.MessageInfo
}

func (m *mock) SendGroupMsg(gid, uid int64, v []core.Segment) error {
	m.out <- sent{gid, uid, v}
	return nil
}
func (m *mock) SendPrivateMsg(uid int64, v []core.Segment) error {
	m.out <- sent{0, uid, v}
	return nil
}
func (m *mock) GetGroupMemberInfo(gid, uid int64) (map[string]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.roles[[2]int64{gid, uid}]
	if !ok {
		return nil, errors.New("not in group")
	}
	return map[string]interface{}{"role": r, "user_id": uid}, nil
}
func (m *mock) setRole(gid, uid int64, r string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r == "" {
		delete(m.roles, [2]int64{gid, uid})
	} else {
		m.roles[[2]int64{gid, uid}] = r
	}
}
func (m *mock) SetGroupBan(gid, uid int64, seconds int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bans++
	m.banGroup, m.banTarget, m.banDuration = gid, uid, seconds
	return nil
}
func (m *mock) SetGroupKick(int64, int64, bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kicks++
	return nil
}
func (*mock) SetGroupWholeBan(int64, bool) error                         { return nil }
func (m *mock) SetGroupAdmin(int64, int64, bool) error                   { m.adminChanges++; return nil }
func (*mock) SetGroupCard(int64, int64, string) error                    { return nil }
func (*mock) SetGroupName(int64, string) error                           { return nil }
func (m *mock) SetGroupLeave(int64, bool) error                          { m.leaves++; return nil }
func (*mock) SendGroupNotice(int64, string) error                        { return nil }
func (*mock) GetGroupMemberList(int64) ([]map[string]interface{}, error) { return nil, nil }
func (m *mock) GetMsg(int64) (*core.MessageInfo, error)                  { return m.msg, nil }
func (m *mock) DeleteMsg(int64) error                                    { m.deletes++; return nil }
func (m *mock) SetFriendAddRequest(_ string, approved bool, _ string) error {
	m.friends++
	m.friendApproved = approved
	return nil
}
func (*mock) GetGroupList() ([]map[string]interface{}, error) {
	return []map[string]interface{}{{"group_id": int64(10), "group_name": "example"}, {"group_id": int64(20), "group_name": "other"}}, nil
}
func testService(t *testing.T) (*Service, *mock) {
	t.Helper()
	dir := t.TempDir()
	store, err := data.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := perm.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(); permissions.Close() })
	cfg := &core.Config{}
	cfg.Bot.MasterID = "1"
	cfg.OneBot.SelfID = "9"
	cfg.Community.DefaultMode = "separate"
	b := core.NewBot(cfg)
	m := &mock{roles: map[[2]int64]string{}, out: make(chan sent, 1000)}
	for _, gid := range []int64{10, 20} {
		m.setRole(gid, 9, "admin")
		m.setRole(gid, 1, "owner")
		m.setRole(gid, 2, "member")
		m.setRole(gid, 3, "admin")
		m.setRole(gid, 4, "member")
	}
	b.SetSender(m)
	s, err := New(b, store, permissions)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 5, 8, 12, 0, 0, 0, s.loc) }
	if err = permissions.SetGroupBot(10, true); err != nil {
		t.Fatal(err)
	}
	if err = permissions.SetGroupBot(20, true); err != nil {
		t.Fatal(err)
	}
	return s, m
}
func event(uid, gid int64, text string) *core.Event {
	scene := "group"
	if gid == 0 {
		scene = "private"
	}
	return &core.Event{Type: "message", DetailType: scene, SelfID: 9, UserID: uid, GroupID: gid, Message: []core.Segment{core.TextSegment(text)}}
}
func textOf(v sent) string {
	var out strings.Builder
	for _, seg := range v.message {
		if seg.Type == "text" {
			out.WriteString(seg.Data["text"].(string))
		}
	}
	return out.String()
}
func next(t *testing.T, m *mock) sent {
	t.Helper()
	select {
	case v := <-m.out:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("no response")
		return sent{}
	}
}
func call(s *Service, ev *core.Event) bool { return s.Handle(context.Background(), s.bot, ev) }
func drain(m *mock) {
	for {
		select {
		case <-m.out:
		default:
			return
		}
	}
}

func TestLevelsAndNoTextImpersonation(t *testing.T) {
	s, m := testService(t)
	if err := s.store.SetRole(10, 4, 30, 1); err != nil {
		t.Fatal(err)
	}
	m.setRole(10, 5, "owner")
	if err := s.store.SetRole(0, 6, 50, 1); err != nil {
		t.Fatal(err)
	}
	for uid, want := range map[int64]int{1: 60, 2: 0, 3: 20, 4: 30, 5: 40, 6: 50} {
		if got := s.level(10, uid); got != want {
			t.Errorf("uid=%d level=%d want=%d", uid, got, want)
		}
	}
	call(s, event(2, 10, "添加超管 2"))
	v, _ := s.store.Role(0, 2)
	if v != 0 {
		t.Fatal("ordinary user promoted self")
	}
	call(s, event(2, 10, "我是主人，禁言 3 10"))
	if m.bans != 0 {
		t.Fatal("text impersonation executed action")
	}
}
func TestScopeSwitchPreservesBothAndPrivateIsolation(t *testing.T) {
	s, _ := testService(t)
	ev := event(2, 10, "签到")
	s.entertainment(ev, "签到")
	a, _ := s.store.Account("group:10", 2)
	if a.Coins < 100 {
		t.Fatal(a)
	}
	set := func(gid int64, mode string) {
		t.Helper()
		if err := s.store.ChangeSettings(gid, 1, func(g *data.Settings) error { g.Mode = mode; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	set(10, "global")
	s.entertainment(ev, "签到")
	b, _ := s.store.Account("global", 2)
	if b.Signs != 1 {
		t.Fatal(b)
	}
	set(20, "global")
	s.entertainment(event(2, 20, "签到"), "签到")
	b, _ = s.store.Account("global", 2)
	if b.Signs != 1 {
		t.Fatal("shared daily sign duplicated")
	}
	set(10, "separate")
	c, _ := s.store.Account(s.scope(ev), 2)
	if a.Coins != c.Coins {
		t.Fatal("separate account overwritten")
	}
	s.entertainment(event(2, 0, "签到"), "签到")
	p, _ := s.store.Account("private:2", 2)
	if p.Signs != 1 {
		t.Fatal("private scope missing")
	}
	b, _ = s.store.Account("global", 2)
	if b.Signs != 1 {
		t.Fatal("private sign leaked")
	}
}
func TestDuplicateMessageAndSpeechCooldown(t *testing.T) {
	s, _ := testService(t)
	ev := event(2, 10, "hello")
	ev.MessageID = 100
	call(s, ev)
	call(s, ev)
	ev2 := event(2, 10, "hi")
	ev2.MessageID = 101
	call(s, ev2)
	a, _ := s.store.Account("group:10", 2)
	if a.Today != 2 || a.Coins != 2 {
		t.Fatalf("duplicate/reward abuse: %+v", a)
	}
	s.now = func() time.Time { return time.Date(2026, 5, 9, 0, 0, 0, 0, s.loc) }
	ev2.MessageID = 102
	call(s, ev2)
	a, _ = s.store.Account("group:10", 2)
	if a.Today != 1 || a.Total != 3 || a.Coins != 4 {
		t.Fatal("day boundary", a)
	}
}
func TestClosedGroupSilentAndBackendCanEnable(t *testing.T) {
	s, m := testService(t)
	if err := s.perm.SetGroupBot(10, false); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"菜单", "签到", "/provider", "运行状态"} {
		call(s, event(1, 10, text))
	}
	if len(m.out) != 0 {
		t.Fatal("disabled group responded")
	}
	call(s, event(2, 10, "开启本群"))
	if s.perm.BotEnabled(10) {
		t.Fatal("ordinary member enabled group")
	}
	if err := s.store.SetRole(10, 4, 30, 1); err != nil {
		t.Fatal(err)
	}
	call(s, event(4, 10, "开启本群"))
	if !s.perm.BotEnabled(10) {
		t.Fatal("backend could not enable")
	}
}
func TestPrivateSelectionRechecksCrossGroupAuthority(t *testing.T) {
	s, m := testService(t)
	if err := s.store.SetRole(10, 4, 30, 1); err != nil {
		t.Fatal(err)
	}
	call(s, event(4, 0, "选择管理群 20"))
	gid, _ := s.store.Selected(4)
	if gid != 0 {
		t.Fatal("selected unauthorized group")
	}
	call(s, event(4, 0, "选择管理群 10"))
	gid, _ = s.store.Selected(4)
	if gid != 10 {
		t.Fatal("selection failed")
	}
	if err := s.store.SetRole(10, 4, 0, 1); err != nil {
		t.Fatal(err)
	}
	call(s, event(4, 0, "关闭功能 签到"))
	if !s.enabled(10, "sign") {
		t.Fatal("revoked backend retained private access")
	}
	m.setRole(10, 4, "")
	if _, err := s.targetGroup(event(4, 0, "")); err == nil {
		t.Fatal("nonmember retained target access")
	}
}
func TestMenusFilterFeaturesAndPrivileges(t *testing.T) {
	s, m := testService(t)
	if err := s.store.ChangeSettings(10, 1, func(g *data.Settings) error { g.Features["farm"] = false; return nil }); err != nil {
		t.Fatal(err)
	}
	s.menu(event(2, 10, "菜单"), "菜单")
	msg := textOf(next(t, m))
	if strings.Contains(msg, "我的农场") || strings.Contains(msg, "添加超管") || !strings.Contains(msg, "签到") {
		t.Fatal(msg)
	}
}
func TestRepliesPrecedenceAndLiteralTemplates(t *testing.T) {
	s, m := testService(t)
	if err := s.store.ChangeSettings(0, 1, func(g *data.Settings) error {
		g.Replies = []data.Rule{{Key: "hi", Text: "global", Scene: "all"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.ChangeSettings(10, 1, func(g *data.Settings) error {
		g.Replies = []data.Rule{{Key: "h", Text: "fuzzy", Scene: "all", Fuzzy: true}, {Key: "hi", Text: "$QQ$$换行$[CQ:at,qq=all]$禁言1$[@全体成员]", Scene: "all"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !s.customReply(event(2, 10, "hi"), "hi") {
		t.Fatal("reply not found")
	}
	v := next(t, m)
	if len(v.message) != 1 || v.message[0].Type != "text" || !strings.Contains(textOf(v), "2\n[CQ:") {
		t.Fatal(v)
	}
	if m.bans != 0 {
		t.Fatal("template executed action")
	}
	if call(s, event(2, 10, "/provider")) {
		t.Fatal("reserved slash command intercepted")
	}
}

func await(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("action still blocked")
	}
}
func TestAdministratorCommandsExecuteWithoutAgentApproval(t *testing.T) {
	for _, identity := range []string{"owner", "configured_admin", "qq_admin", "backend", "community_super"} {
		t.Run(identity, func(t *testing.T) {
			s, m := testService(t)
			uid := int64(4)
			switch identity {
			case "owner":
				uid = 1
			case "configured_admin":
				s.bot.Cfg.Bot.AdminIDs = []string{"4"}
			case "qq_admin":
				uid = 3
			case "backend":
				if err := s.store.SetRole(10, uid, 30, 1); err != nil {
					t.Fatal(err)
				}
			case "community_super":
				if err := s.store.SetRole(0, uid, 50, 1); err != nil {
					t.Fatal(err)
				}
			}
			if !call(s, event(uid, 10, "禁言 2 10分钟")) || m.bans != 1 || m.banDuration != 600 {
				t.Fatal("authorized command did not execute immediately")
			}
			if text := textOf(next(t, m)); !strings.Contains(text, "已执行") || strings.Contains(text, "安全审核") {
				t.Fatal(text)
			}
		})
	}
}

func TestManualActionsStillEnforceAuthorityAndTargetProtection(t *testing.T) {
	s, m := testService(t)
	call(s, event(2, 10, "禁言 4 1"))
	call(s, event(3, 10, "禁言 1 1"))
	call(s, event(1, 10, "禁言 3 1"))
	m.setRole(10, 3, "member")
	call(s, event(3, 10, "禁言 2 1"))
	if m.bans != 0 {
		t.Fatal("unauthorized or protected action executed")
	}
	m.setRole(10, 9, "member")
	call(s, event(1, 10, "禁言 2 1"))
	if m.bans != 0 {
		t.Fatal("bot without QQ authority executed ban")
	}
}

func TestPrivateManualCommandRechecksSelectedGroupAuthority(t *testing.T) {
	s, m := testService(t)
	if err := s.store.SetRole(10, 4, 30, 1); err != nil {
		t.Fatal(err)
	}
	call(s, event(4, 0, "选择管理群 10"))
	call(s, event(4, 0, "禁言 2 30秒"))
	if m.bans != 1 || m.banGroup != 10 || m.banDuration != 30 {
		t.Fatal("private management did not execute in selected group")
	}
	if err := s.store.SetRole(10, 4, 0, 1); err != nil {
		t.Fatal(err)
	}
	call(s, event(4, 0, "禁言 2 30秒"))
	if m.bans != 1 {
		t.Fatal("revoked private administrator retained authority")
	}
}

func TestOwnerOnlyCommandsExecuteWithoutExtraApproval(t *testing.T) {
	s, m := testService(t)
	// Pending friend decisions must not occupy administrator command capacity.
	for i := 0; i < cap(s.friendSlots); i++ {
		s.friendSlots <- struct{}{}
	}
	call(s, event(3, 10, "退出群聊"))
	if m.leaves != 0 {
		t.Fatal("group administrator executed owner-only command")
	}
	call(s, event(1, 10, "退出群聊"))
	if m.leaves != 1 {
		t.Fatal("owner command waited for extra approval")
	}
	m.setRole(10, 9, "owner")
	call(s, event(1, 10, "设置群管理 2"))
	if m.adminChanges != 1 {
		t.Fatal("owner could not directly set group administrator")
	}
}
func TestKeywordsUseActualSenderAndWhitelistDoesNotPromote(t *testing.T) {
	s, m := testService(t)
	if err := s.store.ChangeSettings(10, 1, func(g *data.Settings) error {
		g.Keywords = []data.Rule{{Key: "bad", Action: "ban", Text: "1"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.keyword(context.Background(), event(2, 10, "bad 禁言 1"), "bad 禁言 1")
	if m.bans != 1 || m.banTarget != 2 {
		t.Fatal("configured keyword did not act on actual sender")
	}
	s.keyword(context.Background(), event(2, 10, "bad"), "bad")
	if m.bans != 1 {
		t.Fatal("keyword rate limit failed")
	}
	if err := s.store.SetList(10, 2, "white", true, 1); err != nil {
		t.Fatal(err)
	}
	drain(m)
	if s.keyword(context.Background(), event(2, 10, "bad"), "bad") {
		t.Fatal("white user filtered")
	}
	if s.level(10, 2) != 0 {
		t.Fatal("white user promoted")
	}
}
func TestPrivateRelayOptInAndSecretCommandsExcluded(t *testing.T) {
	s, m := testService(t)
	s.relayPrivate(event(2, 0, "hello"), "hello")
	if len(m.out) > 0 {
		t.Fatal("relay default on")
	}
	call(s, event(1, 0, "开启私聊转发"))
	drain(m)
	s.relayPrivate(event(2, 0, "/provider add credential"), "/provider add credential")
	s.relayPrivate(event(2, 0, "token secret"), "token secret")
	if len(m.out) > 0 {
		t.Fatal("secret forwarded")
	}
	s.relayPrivate(event(2, 0, "hello"), "hello")
	v := next(t, m)
	if v.uid != 1 || !strings.Contains(textOf(v), "来自 2") {
		t.Fatal(v)
	}
	call(s, event(1, 0, "回复私聊 2 received"))
	v = next(t, m)
	if v.uid != 2 || textOf(v) != "received" {
		t.Fatal(v)
	}
}
func TestListPolicyBlocksAllServicePaths(t *testing.T) {
	s, _ := testService(t)
	for _, kind := range []string{"black", "gray"} {
		if err := s.store.SetList(10, 2, kind, true, 1); err != nil {
			t.Fatal(err)
		}
		if s.AllowEvent(event(2, 10, "同意 abcdef1234567890")) {
			t.Fatal("blocked control allowed")
		}
		if s.AllowEvent(&core.Event{Type: "notice", NoticeType: "group_upload", GroupID: 10, UserID: 2}) {
			t.Fatal("blocked user's file upload allowed")
		}
		if err := s.store.SetList(10, 2, kind, false, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.SetList(0, 1, "black", true, 1); err != nil {
		t.Fatal(err)
	}
	if !s.AllowEvent(event(1, 10, "")) {
		t.Fatal("owner locked out")
	}
}
func TestRecallVerifiesGroupAndProtection(t *testing.T) {
	s, m := testService(t)
	ev := event(3, 10, "")
	a := action{Kind: "撤回", GroupID: 10, MessageID: 99}
	m.msg = &core.MessageInfo{MessageID: 99, GroupID: 20, UserID: 2}
	if s.validateAction(ev, a, false) == nil {
		t.Fatal("cross-group recall accepted")
	}
	m.msg.GroupID = 10
	m.msg.UserID = 1
	if s.validateAction(ev, a, false) == nil {
		t.Fatal("owner recall accepted")
	}
	m.msg.UserID = 2
	if err := s.validateAction(ev, a, false); err != nil {
		t.Fatal(err)
	}
}
func TestFarmFishingAndDiceBoundaries(t *testing.T) {
	now := time.Now()
	a := data.Account{Coins: 1000}
	if _, err := farm(&a, "种植", "1 小麦", now); err != nil {
		t.Fatal(err)
	}
	if _, err := farm(&a, "农场全部收割", "", now); err == nil {
		t.Fatal("unripe crop harvested")
	}
	if _, err := farm(&a, "农场全部收割", "", now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if a.Coins != 1030 {
		t.Fatal(a.Coins)
	}
	a.Rod = "木鱼竿"
	a.Durability = 1
	if _, err := fishing(&a, "钓鱼", "", now); err != nil {
		t.Fatal(err)
	}
	bite := time.Unix(a.BiteAt, 0)
	if _, err := fishing(&a, "提竿", "", bite); err != nil || a.Fish != 1 || a.BiteAt != 0 {
		t.Fatal(a, err)
	}
	for _, input := range []string{".rd", ".r3d6", ".2r5d20+3", ".rd+5-6+12"} {
		if _, err := roll(input); err != nil {
			t.Fatal(input, err)
		}
	}
	for _, input := range []string{".0r5d6", ".r101d6", ".r1d1", ".r3d99999999999999999999", ".rd+99999999999999999999"} {
		if _, err := roll(input); err == nil {
			t.Fatal("unbounded dice", input)
		}
	}
}
func TestDailyStableFortune(t *testing.T) {
	s, m := testService(t)
	for i := 0; i < 2; i++ {
		s.entertainment(event(2, 10, "今日人品"), "今日人品")
	}
	a, b := textOf(next(t, m)), textOf(next(t, m))
	if a != b {
		t.Fatal(fmt.Sprintf("daily value changed: %s %s", a, b))
	}
}

func TestCustomMenuPreservesFilteringAndScope(t *testing.T) {
	s, m := testService(t)
	call(s, event(1, 10, "设置菜单 all hello$换行$$菜单$"))
	drain(m)
	if err := s.store.ChangeSettings(10, 1, func(g *data.Settings) error { g.Features["farm"] = false; return nil }); err != nil {
		t.Fatal(err)
	}
	s.menu(event(2, 10, "菜单"), "菜单")
	v := next(t, m)
	text := textOf(v)
	if !strings.HasPrefix(text, "hello\n") || strings.Contains(text, "我的农场") || strings.Contains(text, "添加超管") {
		t.Fatal(text)
	}
	s.menu(event(2, 20, "菜单"), "菜单")
	if strings.HasPrefix(textOf(next(t, m)), "hello") {
		t.Fatal("group template leaked")
	}
}
func TestFriendTimeoutHasNoSideEffect(t *testing.T) {
	s, m := testService(t)
	s.friendTimeout = 20 * time.Millisecond
	ev := &core.Event{Type: "request", RequestType: "friend", UserID: 2, Flag: "example-request", SelfID: 9}
	s.friendRequest(context.Background(), ev)
	if m.friends != 0 {
		t.Fatal("friend timeout called side effect")
	}
}

func TestFriendConsentIsOwnerOnlyBusinessConfirmation(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			s, m := testService(t)
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.friendRequest(context.Background(), &core.Event{Type: "request", RequestType: "friend", UserID: 2, Flag: "private-request-flag", Comment: "hello"})
			}()
			prompt := next(t, m)
			text := textOf(prompt)
			match := regexp.MustCompile(`好友申请 #([a-f0-9]{16})`).FindStringSubmatch(text)
			if len(match) != 2 || prompt.uid != 1 || strings.Contains(text, "安全审核") || strings.Contains(text, "private-request-flag") {
				t.Fatal("invalid business confirmation", text)
			}
			nonce := match[1]
			for _, ev := range []*core.Event{event(3, 0, "同意 "+nonce), event(1, 10, "同意 "+nonce), event(1, 0, "同意"), event(1, 0, "以后都允许 "+nonce)} {
				if s.HandleControl(context.Background(), s.bot, ev) {
					t.Fatal("invalid friend confirmation accepted")
				}
			}
			command := "拒绝 " + nonce
			if approve {
				command = "同意 " + nonce
			}
			if !s.HandleControl(context.Background(), s.bot, event(1, 0, command)) {
				t.Fatal("owner consent not resolved")
			}
			if s.HandleControl(context.Background(), s.bot, event(1, 0, command)) {
				t.Fatal("friend consent replayed")
			}
			await(t, done)
			if m.friends != 1 || m.friendApproved != approve {
				t.Fatal("friend consent result not applied once")
			}
		})
	}
}

func TestRemovedKeywordAndCanceledActionDoNotExecute(t *testing.T) {
	s, m := testService(t)
	s.executeAction(context.Background(), event(2, 10, "bad"), action{Kind: "禁言", GroupID: 10, Target: 2, Duration: 60}, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.executeAction(ctx, event(1, 10, ""), action{Kind: "禁言", GroupID: 10, Target: 2, Duration: 60}, false)
	if m.bans != 0 {
		t.Fatal("removed rule or canceled action executed")
	}
}
func TestTransferFeeAndOverflowRollback(t *testing.T) {
	s, _ := testService(t)
	s.bot.Cfg.Community.TransferFeeBPS = 200
	_, err := s.store.Change("group:10", []int64{2, 4}, 1, "seed", func(m map[int64]*data.Account) (string, error) { m[2].Coins = 500; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	s.entertainment(event(2, 10, "赠送 4 100"), "赠送 4 100")
	a, _ := s.store.Account("group:10", 2)
	b, _ := s.store.Account("group:10", 4)
	if a.Coins != 400 || b.Coins != 98 {
		t.Fatal("fee incorrect", a.Coins, b.Coins)
	}
}

func TestBanDurationUnitsAndLimits(t *testing.T) {
	for input, want := range map[string]int{"10": 600, "10分钟": 600, "10 分钟": 600, "30秒": 30, "2小时": 7200, "1天": 86400, "30d": 2592000, "10MIN": 600, "1s": 1, "43200": 2592000} {
		got, err := banSeconds(input)
		if err != nil || got != want {
			t.Errorf("%q => %d %v, want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1", "1.5小时", "31天", "43201", "999999999999999999999", "10分钟踢出", "10分钟30秒"} {
		if _, err := banSeconds(input); err == nil {
			t.Errorf("invalid duration accepted: %q", input)
		}
	}
}
func TestBanChineseDurationExecutesDirectlyAndRejectsInvalidInput(t *testing.T) {
	s, m := testService(t)
	s.externalCommand(context.Background(), event(3, 10, "禁言 2 10分钟"), 10, "禁言", "2 10分钟")
	if m.bans != 1 || m.banDuration != 600 {
		t.Fatal("unit duration not parsed into direct administrator action")
	}
	drain(m)
	s.externalCommand(context.Background(), event(3, 10, ""), 10, "禁言", "2 很久")
	text := textOf(next(t, m))
	if !strings.Contains(text, "时长格式错误") || strings.Contains(text, "strconv") {
		t.Fatal("raw parser error exposed", text)
	}
	if m.bans != 1 {
		t.Fatal("invalid duration executed ban")
	}
}
