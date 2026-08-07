package ai

import (
	"context"
	"strings"
	"testing"

	"gobot/core"
	"gobot/store/persona"
)

func ctxT() context.Context { return context.Background() }

func TestPersonaStore(t *testing.T) {
	dir := t.TempDir()
	st, err := persona.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.Load()
	if p == nil {
		t.Fatal("初始人设应为空结构")
	}
	p.Style = "简洁幽默"
	p.Traits = []string{"友善", "聪明"}
	p.Rules = []string{"不卖萌"}
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	p2, _ := st.Load()
	if p2.Style != "简洁幽默" || len(p2.Traits) != 2 {
		t.Fatalf("持久化失败: %+v", p2)
	}
}

func TestPersonaInject(t *testing.T) {
	dir := t.TempDir()
	st, _ := persona.New(dir)
	bot := core.NewBot(&core.Config{})
	cfg := &core.Config{Persona: core.PersonaConfig{Enabled: true}}
	pm := NewPersonaManager(st, bot, cfg)

	pm.SetBase("你叫gobot")
	base := "原始系统提示"
	result := pm.InjectPrompt(base)
	if !strings.Contains(result, "你叫gobot") {
		t.Fatal("应注入基础人设")
	}
	if !strings.Contains(result, "=== 你的设定(人设) ===") {
		t.Fatal("应包含人设标记")
	}
	if !strings.Contains(result, base) {
		t.Fatal("应保留原始提示")
	}

	// 优化后注入特质
	p, _ := st.Load()
	p.Style = "话痨"
	p.Traits = []string{"热情"}
	st.Save(p)
	result2 := pm.InjectPrompt("基础")
	if !strings.Contains(result2, "话痨") || !strings.Contains(result2, "热情") {
		t.Fatal("应注入优化后的人设")
	}
}

func TestPersonaSelfImproveParse(t *testing.T) {
	dir := t.TempDir()
	st, _ := persona.New(dir)
	bot := core.NewBot(&core.Config{})
	cfg := &core.Config{Persona: core.PersonaConfig{Enabled: true, SelfImprove: true, ImproveEvery: 2}}
	pm := NewPersonaManager(st, bot, cfg)

	// 模拟 LLM 返回优化建议
	called := false
	pm.SetLLM(func(ctx context.Context, prompt string) (string, error) {
		called = true
		return `{"style":"更简洁","traits":["高效"],"rules":["先给结论"]}`, nil
	})

	// 第1次不触发
	pm.MaybeSelfImprove(ctxT(), 1, "对话1")
	if called {
		t.Fatal("第1次不应触发")
	}
	// 第2次触发
	pm.MaybeSelfImprove(ctxT(), 1, "对话2")
	if !called {
		t.Fatal("第2次应触发")
	}
	p, _ := st.Load()
	if p.Style != "更简洁" || len(p.Traits) != 1 || p.Traits[0] != "高效" {
		t.Fatalf("自我优化未生效: %+v", p)
	}
}
