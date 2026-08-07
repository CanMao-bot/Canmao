package ai

import (
	"context"
	"testing"

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
