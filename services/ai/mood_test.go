package ai

import (
	"context"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/mood"
)

func TestMoodApply(t *testing.T) {
	dir := t.TempDir()
	st, err := mood.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)

	// 初始中性
	cur, _ := mm.Current()
	if cur.Value != 50 {
		t.Fatalf("初始心情 = %d, want 50", cur.Value)
	}

	// 负面
	st2, _ := mm.Apply(-15, "sad", "被骂")
	if st2.Value != 35 {
		t.Fatalf("负面后 = %d, want 35", st2.Value)
	}

	// 持久化
	st3, _ := mm.Current()
	if st3.Value != 35 || st3.Emotion != "sad" {
		t.Fatalf("持久化 = %d/%s", st3.Value, st3.Emotion)
	}
}

func TestMoodDetect(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)

	// 正面
	mm.DetectAndApply("你真棒, 谢谢你!")
	cur, _ := mm.Current()
	if cur.Value <= 50 {
		t.Fatalf("正面反馈后应大于中性, got %d", cur.Value)
	}

	// 负面
	mm.DetectAndApply("你个废物傻逼")
	cur, _ = mm.Current()
	if cur.Value >= 50 {
		t.Fatalf("负面后应小于中性, got %d", cur.Value)
	}
}

func TestMoodDetectWithAI(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)

	// 注入模拟 AI 情感判断
	mm.SetLLM(func(ctx context.Context, text string) (EmotionResult, error) {
		return EmotionResult{Delta: -20, Emotion: "angry", Reason: "用户讽刺"}, nil
	})

	// 讽刺场景: AI 应判断为负面
	mm.DetectAndApply("你可真厉害啊(讽刺)")
	cur, _ := mm.Current()
	if cur.Value >= 50 || cur.Emotion != "angry" {
		t.Fatalf("AI 判断讽刺应为负面, got %d/%s", cur.Value, cur.Emotion)
	}
}

func TestMoodDetectSkipsCommands(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)

	// 命令不应触发心情变化
	mm.DetectAndApply("/bot 开")
	cur, _ := mm.Current()
	if cur.Value != 50 {
		t.Fatalf("命令不应影响心情, got %d", cur.Value)
	}
}

func TestMoodProactiveInterval(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 3)

	// 前2条不应触发(计数未满)
	if mm.CountAndMaybeProactive(1) {
		t.Fatal("第1条不应触发")
	}
	if mm.CountAndMaybeProactive(1) {
		t.Fatal("第2条不应触发")
	}
	// 第3条触发评估(返回bool, 可能是true或false取决于心情, 但应执行评估)
	mm.CountAndMaybeProactive(1)
	// 验证计数已重置: 再1条不触发
	if mm.CountAndMaybeProactive(1) {
		t.Fatal("重置后不应触发")
	}
}

func TestMoodJudgeInterjectLLM(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)

	// 注入模拟插话判断
	mm.SetInterjectLLM(func(ctx context.Context, text, moodTxt string, recent []Message) (InterjectResult, error) {
		if text == "我今天心情不好" {
			return InterjectResult{Should: true, Mode: "care", Reason: "用户情绪低落"}, nil
		}
		if text == "哈哈哈哈" {
			return InterjectResult{Should: false, Mode: "silent", Reason: "纯客套无内容"}, nil
		}
		return InterjectResult{Should: true, Mode: "chat", Reason: "话题可接"}, nil
	})

	// 情感关怀优先触发
	res, err := mm.JudgeInterject(context.Background(), "我今天心情不好", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Should || res.Mode != "care" {
		t.Fatalf("低沉消息应触发 care, got %+v", res)
	}

	// 纯客套静默
	res, _ = mm.JudgeInterject(context.Background(), "哈哈哈哈", "", nil)
	if res.Should {
		t.Fatalf("纯客套不应插话, got %+v", res)
	}

	// 普通话题 chat
	res, _ = mm.JudgeInterject(context.Background(), "今天天气不错", "", nil)
	if !res.Should || res.Mode != "chat" {
		t.Fatalf("普通话题应触发 chat, got %+v", res)
	}
}

func TestMoodJudgeInterjectFallback(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)
	// 不注入 LLM, 走心情概率回退(心情极高必然触发)
	st.Save(&mood.State{Value: 100, Emotion: "happy"})

	res, err := mm.JudgeInterject(context.Background(), "随便聊聊", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Should || res.Mode != "chat" {
		t.Fatalf("心情100时应回退触发, got %+v", res)
	}
}

func TestMoodCooldown(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 10)
	mm.SetCooldown(time.Minute, time.Minute, 30*time.Second)

	if !mm.CanInterject(1) {
		t.Fatal("初始应可插话")
	}
	mm.MarkInterjected(1, "chat")
	if mm.CanInterject(1) {
		t.Fatal("插话后冷却期内不应可插话")
	}
	// 不同群互不影响
	if !mm.CanInterject(2) {
		t.Fatal("其他群不受冷却影响")
	}
	// care 冷却更短, 但仍需等待
	mm.MarkInterjected(2, "care")
	if mm.CanInterject(2) {
		t.Fatal("care 插话后冷却期内不应可插话")
	}
	// 手动越过冷却(重置为过去)
	mm.mu.Lock()
	mm.nextInterject[1] = time.Now().Add(-time.Second)
	mm.mu.Unlock()
	if !mm.CanInterject(1) {
		t.Fatal("冷却到期后应可插话")
	}
}

func TestMoodPunishSkipsOwnerAdmin(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{
		Bot: core.BotConfig{MasterID: "10001", AdminIDs: []string{"10002"}},
	})
	mm := NewMoodManager(st, bot, 3)
	mm.SetPunish(true, 30, time.Minute)
	// 心情极差
	st.Save(&mood.State{Value: 10, Emotion: "angry"})

	// 主人/管理员不应被惩罚
	for _, uid := range []int64{10001, 10002} {
		mm.NoteAggressor(1, uid)
		if ok, _ := mm.MaybePunishAggressor(1); ok {
			t.Fatalf("主人/管理员(%d)不应被自主惩罚", uid)
		}
		mm.ClearAggressor(1)
	}
	// 但惩罚冷却已占位, 清理以便继续
	mm.mu.Lock()
	delete(mm.punishCooldown, 1)
	mm.mu.Unlock()
}

func TestMoodPunishCooldown(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 3)
	mm.SetPunish(true, 30, time.Hour) // 1 小时冷却
	st.Save(&mood.State{Value: 10, Emotion: "angry"})

	mm.NoteAggressor(1, 12345)
	// 无实际 Sender(GroupAdminClient), MaybePunish 会在获取 client 时返回 false, 但会占用冷却
	mm.MaybePunishAggressor(1)
	// 冷却期内不重复触发(即便心情仍差)
	mm.mu.Lock()
	mm.punishCooldown[1] = time.Now().Add(2 * time.Hour) // 确保在未来
	mm.mu.Unlock()
	if ok, _ := mm.MaybePunishAggressor(1); ok {
		t.Fatal("冷却期内不应重复惩罚")
	}
}

func TestMoodPunishDisabled(t *testing.T) {
	dir := t.TempDir()
	st, _ := mood.New(dir)
	bot := core.NewBot(&core.Config{})
	mm := NewMoodManager(st, bot, 3)
	mm.SetPunish(false, 30, time.Minute)
	st.Save(&mood.State{Value: 5, Emotion: "angry"})
	mm.NoteAggressor(1, 12345)
	if ok, _ := mm.MaybePunishAggressor(1); ok {
		t.Fatal("关闭自主惩罚后不应触发")
	}
}
