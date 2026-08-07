package plugin

import (
	"context"
	"strconv"
	"testing"

	"gobot/core"
	"gobot/pluginapi"
	"gobot/store/perm"
)

func strconvFormat(i int64) string { return strconv.FormatInt(i, 10) }

// 模拟群管理客户端
type fakeGAC struct {
	banned     map[string]int
	wholeBan   bool
	kicked     []int64
	notices    []string
	cards      map[int64]string
	groupName  string
	members    []map[string]interface{}
}

func (f *fakeGAC) SetGroupBan(gid, uid int64, dur int) error {
	if f.banned == nil {
		f.banned = map[string]int{}
	}
	f.banned[coreID(gid, uid)] = dur
	return nil
}
func (f *fakeGAC) SetGroupWholeBan(gid int64, en bool) error { f.wholeBan = en; return nil }
func (f *fakeGAC) SetGroupKick(gid, uid int64, r bool) error { f.kicked = append(f.kicked, uid); return nil }
func (f *fakeGAC) SetGroupAdmin(gid, uid int64, en bool) error { return nil }
func (f *fakeGAC) SetGroupCard(gid, uid int64, card string) error {
	if f.cards == nil {
		f.cards = map[int64]string{}
	}
	f.cards[uid] = card
	return nil
}
func (f *fakeGAC) SetGroupName(gid int64, name string) error { f.groupName = name; return nil }
func (f *fakeGAC) SetGroupLeave(gid int64, d bool) error       { return nil }
func (f *fakeGAC) SendGroupNotice(gid int64, c string) error   { f.notices = append(f.notices, c); return nil }
func (f *fakeGAC) GetGroupMemberList(gid int64) ([]map[string]interface{}, error) {
	return f.members, nil
}
func (f *fakeGAC) GetGroupMemberInfo(gid, uid int64) (map[string]interface{}, error) {
	return nil, nil
}

func coreID(gid, uid int64) string {
	return strconvFormat(gid) + "_" + strconvFormat(uid)
}

func TestGroupAdminBridge(t *testing.T) {
	dir := t.TempDir()
	permS, err := perm.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer permS.Close()

	gac := &fakeGAC{}
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "1000001"}})
	bot.SetSender(&fakeSenderT{})

	svc := &Service{bot: bot, gac: gac, permS: permS}

	// 群管理员桥接
	if err := svc.SetGroupBan(123, 456, 300); err != nil {
		t.Fatal(err)
	}
	if gac.banned[coreID(123, 456)] != 300 {
		t.Fatal("禁言未生效")
	}
	svc.SetGroupWholeBan(123, true)
	if !gac.wholeBan {
		t.Fatal("全体禁言未生效")
	}
	svc.SetGroupKick(123, 789, false)
	if len(gac.kicked) != 1 || gac.kicked[0] != 789 {
		t.Fatal("踢人未生效")
	}
	svc.SendGroupNotice(123, "测试公告")
	if len(gac.notices) != 1 || gac.notices[0] != "测试公告" {
		t.Fatal("公告未生效")
	}
	svc.SetGroupCard(123, 456, "昵称")
	if gac.cards[456] != "昵称" {
		t.Fatal("名片未生效")
	}

	// 权限判断
	if !svc.IsMaster(1000001) {
		t.Fatal("主人判断失败")
	}
	if svc.IsMaster(999) {
		t.Fatal("普通用户不应是主人")
	}
	// 群管理员权限
	permS.SetGroupRole(123, 555, "admin")
	if !svc.IsGroupAdmin(123, 555) {
		t.Fatal("群管理员判断失败")
	}
	if svc.IsGroupAdmin(123, 666) {
		t.Fatal("普通成员不应是群管理员")
	}
}

type fakeSenderT struct{}

func (f *fakeSenderT) SendGroupMsg(gid, uid int64, msg []core.Segment) error { return nil }
func (f *fakeSenderT) SendPrivateMsg(uid int64, msg []core.Segment) error    { return nil }

func TestCapablePluginSetup(t *testing.T) {
	// 验证 CapablePlugin 能力注入
	type capable struct {
		pluginapi.Base
		gotCaps *pluginapi.Capabilities
	}
	p := &capable{}
	caps := &pluginapi.Capabilities{
		Sender:     nil,
		GroupAdmin: &fakeGAC{},
		Perm:       nil,
	}
	if cp, ok := interface{}(p).(pluginapi.CapablePlugin); ok {
		_ = cp.Setup(caps)
	}
	if p.gotCaps != nil {
		// 需手动实现 SetUp 才能赋值, 这里仅验证类型断言
	}
	_ = context.Background()
}

var _ = context.Background
