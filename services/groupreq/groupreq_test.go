package groupreq

import (
	"context"
	"strings"
	"testing"

	"gobot/core"
	"gobot/store/perm"
)

// approveCall 记录一次审批调用
type approveCall struct {
	flag    string
	approve bool
	reason  string
}

// fakeSender 实现 Sender + GroupRequestClient + GroupAdminClient
type fakeSender struct {
	sent      []string // 记录发送的群消息文本
	approves  []approveCall
	strangers map[int64]string // userID -> nickname
	roles     map[int64]string // userID -> 群内角色
}

func (f *fakeSender) SendGroupMsg(gid, uid int64, msg []core.Segment) error {
	for _, seg := range msg {
		if t, ok := seg.Data["text"].(string); ok {
			f.sent = append(f.sent, t)
		}
	}
	return nil
}
func (f *fakeSender) SendPrivateMsg(uid int64, msg []core.Segment) error { return nil }

func (f *fakeSender) SetGroupAddRequest(flag string, approve bool, reason string) error {
	f.approves = append(f.approves, approveCall{flag: flag, approve: approve, reason: reason})
	return nil
}
func (f *fakeSender) GetStrangerInfo(userID int64) (map[string]interface{}, error) {
	return map[string]interface{}{"nickname": f.strangers[userID]}, nil
}

func (f *fakeSender) SetGroupBan(groupID, userID int64, duration int) error    { return nil }
func (f *fakeSender) SetGroupWholeBan(groupID int64, enable bool) error        { return nil }
func (f *fakeSender) SetGroupKick(groupID, userID int64, reject bool) error    { return nil }
func (f *fakeSender) SetGroupAdmin(groupID, userID int64, enable bool) error   { return nil }
func (f *fakeSender) SetGroupCard(groupID, userID int64, card string) error    { return nil }
func (f *fakeSender) SetGroupName(groupID int64, name string) error            { return nil }
func (f *fakeSender) SetGroupLeave(groupID int64, isDismiss bool) error        { return nil }
func (f *fakeSender) SendGroupNotice(groupID int64, content string) error      { return nil }
func (f *fakeSender) GetGroupMemberList(groupID int64) ([]map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeSender) GetGroupMemberInfo(groupID, userID int64) (map[string]interface{}, error) {
	return map[string]interface{}{"role": f.roles[userID]}, nil
}

func newTestManager(t *testing.T) (*Manager, *core.Bot, *fakeSender) {
	t.Helper()
	bot := core.NewBot(&core.Config{
		Bot:    core.BotConfig{MasterID: "999", Prefix: "/"},
		OneBot: core.OneBotConfig{SelfID: "1000"},
	})
	fs := &fakeSender{
		strangers: map[int64]string{456: "小明"},
		roles:     map[int64]string{1000: "admin", 555: "member", 777: "owner"},
	}
	bot.SetSender(fs)
	pst, err := perm.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pst.SetGroupBot(123, true) // 群默认关闭, 先启用
	return New(bot, pst), bot, fs
}

func requestEvent() *core.Event {
	return &core.Event{
		Type: "request", RequestType: "group", SubType: "add",
		GroupID: 123, UserID: 456, SelfID: 1000,
		Comment: "问题：喜欢猫吗\n答案：喜欢", Flag: "flag-abc",
	}
}

func msgEvent(userID int64, text string) *core.Event {
	return &core.Event{
		Type: "message", DetailType: "group", GroupID: 123, UserID: userID,
		Message: []core.Segment{core.TextSegment(text)},
	}
}

// TestRequestCard 入群申请事件 → 群内发出含 QQ/昵称/flag 的卡片
func TestRequestCard(t *testing.T) {
	m, bot, fs := newTestManager(t)
	if !m.Handle(context.Background(), bot, requestEvent()) {
		t.Fatal("申请事件应被消费")
	}
	if len(fs.sent) != 1 {
		t.Fatalf("应发出 1 条卡片, 实际 %d", len(fs.sent))
	}
	card := fs.sent[0]
	for _, want := range []string{"#1", "456", "小明", "flag-abc", "问题: 喜欢猫吗", "答案: 喜欢"} {
		if !strings.Contains(card, want) {
			t.Errorf("卡片缺少 %q:\n%s", want, card)
		}
	}
}

// TestRequestNoAdmin 无管理权限时静默忽略
func TestRequestNoAdmin(t *testing.T) {
	m, bot, fs := newTestManager(t)
	fs.roles[1000] = "member" // bot 不是管理
	if !m.Handle(context.Background(), bot, requestEvent()) {
		t.Fatal("应静默消费")
	}
	if len(fs.sent) != 0 {
		t.Fatalf("无管理权限不应发卡, 实际 %d 条", len(fs.sent))
	}
}

// TestMasterApprove 主人 /同意 → 调用审批且 pending 移除
func TestMasterApprove(t *testing.T) {
	m, bot, fs := newTestManager(t)
	m.Handle(context.Background(), bot, requestEvent())
	sentBefore := len(fs.sent)

	if !m.Handle(context.Background(), bot, msgEvent(999, "/同意 1")) {
		t.Fatal("审批命令应被消费")
	}
	if len(fs.approves) != 1 {
		t.Fatalf("应调用 1 次审批, 实际 %d", len(fs.approves))
	}
	c := fs.approves[0]
	if c.flag != "flag-abc" || !c.approve {
		t.Errorf("审批参数错误: %+v", c)
	}
	last := fs.sent[len(fs.sent)-1]
	if len(fs.sent) != sentBefore+1 || !strings.Contains(last, "已同意 #1") || !strings.Contains(last, "小明 456") {
		t.Errorf("审批回复不对: %q", last)
	}
	// pending 已移除, 再审批同编号应提示未找到
	m.Handle(context.Background(), bot, msgEvent(999, "/同意 1"))
	if !strings.Contains(fs.sent[len(fs.sent)-1], "未找到该编号的申请") {
		t.Errorf("重复审批应提示未找到, 实际: %q", fs.sent[len(fs.sent)-1])
	}
}

// TestOwnerRejectWithReason 群主 /拒绝 带理由
func TestOwnerRejectWithReason(t *testing.T) {
	m, bot, fs := newTestManager(t)
	m.Handle(context.Background(), bot, requestEvent())
	m.Handle(context.Background(), bot, msgEvent(777, "/拒绝 1 广告号"))
	if len(fs.approves) != 1 {
		t.Fatalf("应调用 1 次审批, 实际 %d", len(fs.approves))
	}
	c := fs.approves[0]
	if c.approve || c.reason != "广告号" {
		t.Errorf("拒绝参数错误: %+v", c)
	}
	if !strings.Contains(fs.sent[len(fs.sent)-1], "已拒绝 #1") {
		t.Errorf("拒绝回复不对: %q", fs.sent[len(fs.sent)-1])
	}
}

// TestMemberRejectSilent 普通成员 /拒绝 → 静默不执行
func TestMemberRejectSilent(t *testing.T) {
	m, bot, fs := newTestManager(t)
	m.Handle(context.Background(), bot, requestEvent())
	sentBefore := len(fs.sent)

	if !m.Handle(context.Background(), bot, msgEvent(555, "/拒绝 1")) {
		t.Fatal("无权限命令应静默消费")
	}
	if len(fs.approves) != 0 {
		t.Fatalf("普通成员不应触发审批, 实际 %d 次", len(fs.approves))
	}
	if len(fs.sent) != sentBefore {
		t.Fatalf("无权限不应刷提示, 多发 %d 条", len(fs.sent)-sentBefore)
	}
	// 申请仍在 pending
	m.Handle(context.Background(), bot, msgEvent(555, "/入群申请"))
	if !strings.Contains(fs.sent[len(fs.sent)-1], "#1 小明 (456)") {
		t.Errorf("申请应仍在列表: %q", fs.sent[len(fs.sent)-1])
	}
}

// TestListRequests /入群申请 列表(任何人可看, 空列表有提示)
func TestListRequests(t *testing.T) {
	m, bot, fs := newTestManager(t)
	m.Handle(context.Background(), bot, msgEvent(555, "/入群申请"))
	if !strings.Contains(fs.sent[len(fs.sent)-1], "暂无待审批申请") {
		t.Errorf("空列表提示不对: %q", fs.sent[len(fs.sent)-1])
	}

	m.Handle(context.Background(), bot, requestEvent())
	m.Handle(context.Background(), bot, msgEvent(555, "/requests"))
	last := fs.sent[len(fs.sent)-1]
	if !strings.Contains(last, "#1 小明 (456)") {
		t.Errorf("列表缺少申请: %q", last)
	}
}

// TestDisabledGroupSilent 群 bot 未启用时静默忽略申请
func TestDisabledGroupSilent(t *testing.T) {
	m, bot, fs := newTestManager(t)
	pst, _ := perm.New(t.TempDir())
	pst.SetGroupBot(123, false)
	m.perm = pst
	if !m.Handle(context.Background(), bot, requestEvent()) {
		t.Fatal("应静默消费")
	}
	if len(fs.sent) != 0 {
		t.Fatalf("未启用群不应发卡, 实际 %d 条", len(fs.sent))
	}
}
