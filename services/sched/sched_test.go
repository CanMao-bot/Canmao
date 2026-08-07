package sched

import (
	"testing"
	"time"

	"gobot/core"
	"gobot/store/sched"
)

type fakeSender struct {
	groupMsgs  []string
	privateMsgs []string
}

func (f *fakeSender) SendGroupMsg(gid, uid int64, msg []core.Segment) error {
	for _, s := range msg {
		if s.Type == "text" {
			f.groupMsgs = append(f.groupMsgs, s.Data["text"].(string))
		}
	}
	return nil
}
func (f *fakeSender) SendPrivateMsg(uid int64, msg []core.Segment) error {
	for _, s := range msg {
		if s.Type == "text" {
			f.privateMsgs = append(f.privateMsgs, s.Data["text"].(string))
		}
	}
	return nil
}

func TestSchedulerOnce(t *testing.T) {
	dir := t.TempDir()
	st, err := sched.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	snd := &fakeSender{}
	bot := core.NewBot(&core.Config{})
	bot.SetSender(snd)

	s := New(st, bot)

	// 添加一个已到期的一次性任务(私聊)
	id, _ := st.Add(&sched.Task{
		Type: "once", Content: "提醒喝水", Scope: "private", UserID: 100,
		TargetAt: time.Now().Unix() - 1, Enabled: true, CreatedAt: time.Now().Unix(),
	})

	s.runDue()
	if len(snd.privateMsgs) != 1 || snd.privateMsgs[0] != "提醒喝水" {
		t.Fatalf("私聊任务未触发: %v", snd.privateMsgs)
	}

	// 任务应被停用
	tasks, _ := st.List("private", 0, 100)
	for _, tk := range tasks {
		if tk.ID == id && tk.Enabled {
			t.Fatal("一次性任务执行后应停用")
		}
	}
}

func TestSchedulerRepeat(t *testing.T) {
	dir := t.TempDir()
	st, _ := sched.New(dir)
	defer st.Close()

	snd := &fakeSender{}
	bot := core.NewBot(&core.Config{})
	bot.SetSender(snd)
	s := New(st, bot)

	st.Add(&sched.Task{
		Type: "repeat", Content: "每10分钟", Scope: "group", GroupID: 123, UserID: 100,
		TargetAt: time.Now().Unix() - 1, Interval: 600, Enabled: true, CreatedAt: time.Now().Unix(),
	})
	s.runDue()

	if len(snd.groupMsgs) != 1 {
		t.Fatalf("群任务未触发: %v", snd.groupMsgs)
	}
	// 重复任务应保留启用且更新下次时间
	tasks, _ := st.List("group", 123, 100)
	for _, tk := range tasks {
		if !tk.Enabled {
			t.Fatal("重复任务应保持启用")
		}
		if tk.TargetAt <= time.Now().Unix() {
			t.Fatalf("重复任务下次时间应更新, got %d", tk.TargetAt)
		}
		if tk.RunCount != 1 {
			t.Fatalf("RunCount = %d, want 1", tk.RunCount)
		}
	}
}
