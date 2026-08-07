package ai

import (
	"strings"
	"testing"

	"gobot/core"
	"gobot/store/perm"
)

func newPermStore(t *testing.T, dir string) *perm.Store {
	t.Helper()
	st, err := perm.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestRenderHelpLevels(t *testing.T) {
	cfg := coreCfgDefault()
	svc := &Service{cfg: cfg}

	member := svc.renderHelp(levelMember)
	if !strings.Contains(member, "基础命令") {
		t.Fatal("成员帮助应含基础命令")
	}
	if strings.Contains(member, "主人专属") {
		t.Fatal("成员帮助不应含主人专属")
	}
	if strings.Contains(member, "管理员命令") {
		t.Fatal("成员帮助不应含管理员命令")
	}

	admin := svc.renderHelp(levelAdmin)
	if !strings.Contains(admin, "管理员命令") {
		t.Fatal("管理员帮助应含管理员命令")
	}
	if strings.Contains(admin, "主人专属") {
		t.Fatal("管理员帮助不应含主人专属")
	}

	master := svc.renderHelp(levelMaster)
	if !strings.Contains(master, "主人专属") {
		t.Fatal("主人帮助应含主人专属")
	}
	if !strings.Contains(master, "/provider") {
		t.Fatal("主人帮助应含 provider 命令")
	}
	if !strings.Contains(master, "/grant") {
		t.Fatal("主人帮助应含 grant 命令")
	}
}

func TestLevelOf(t *testing.T) {
	dir := t.TempDir()
	bot := core.NewBot(&core.Config{
		Bot: core.BotConfig{MasterID: "3436464181"},
		AI:  core.AIConfig{},
	})
	// perm store 需要真实初始化
	pst := newPermStore(t, dir)
	defer pst.Close()
	svc := &Service{perm: pst, bot: bot}

	masterEv := &core.Event{Type: "message", UserID: 3436464181, DetailType: "private"}
	if lvl := svc.levelOf(bot, masterEv, true); lvl != levelMaster {
		t.Fatalf("主人级别 = %v, want levelMaster", lvl)
	}

	// 群内 admin
	ev := &core.Event{Type: "message", UserID: 555, GroupID: 123, DetailType: "group"}
	pst.SetGroupRole(123, 555, "admin")
	if lvl := svc.levelOf(bot, ev, false); lvl != levelAdmin {
		t.Fatalf("群admin级别 = %v, want levelAdmin", lvl)
	}

	// 普通成员
	ev2 := &core.Event{Type: "message", UserID: 666, GroupID: 123, DetailType: "group"}
	if lvl := svc.levelOf(bot, ev2, false); lvl != levelMember {
		t.Fatalf("普通成员级别 = %v, want levelMember", lvl)
	}
}
