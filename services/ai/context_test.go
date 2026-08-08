package ai

import (
	"testing"

	"gobot/core"
	"gobot/store/session"
)

func coreCfgCompact() *core.Config {
	return &core.Config{AI: core.AIConfig{CompactToken: 9999}}
}

func coreCfgDefault() *core.Config {
	return &core.Config{AI: core.AIConfig{MaxTokens: 2048}}
}

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"你好世界", 4},                                  // 4 个中文字 = 4
		{"hello world", 2},                              // 11 字符 / 4 = 2
		{"", 0},                                         // 空
		{"你好 hello 世界 world", 4 + 13/4},               // 4 + 3
	}
	for _, c := range cases {
		got := estimateTokens(c.in)
		if got != c.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSessionStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	st, err := session.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 首次获取自动创建
	s1, err := st.GetCurrent("private", 0, 10001, "会话A")
	if err != nil {
		t.Fatal(err)
	}
	if s1.ID == 0 {
		t.Fatal("会话 ID 不应为 0")
	}

	// 追加
	st.Append(s1.ID, []session.Message{{Role: "user", Content: "hi"}}, 10)
	s2, _ := st.GetCurrent("private", 0, 10001, "")
	if s2.ID != s1.ID {
		t.Fatal("GetCurrent 应返回同一会话")
	}
	if len(s2.Messages) != 1 {
		t.Fatalf("消息数 = %d, want 1", len(s2.Messages))
	}

	// 新建会话并切换
	s3, err := st.Create("private", 0, 10001, "会话B")
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := st.GetCurrent("private", 0, 10001, "")
	if cur.ID != s3.ID {
		t.Fatalf("当前会话应为新会话 %d, got %d", s3.ID, cur.ID)
	}

	// 列表
	list, _ := st.List("private", 0, 10001)
	if len(list) != 2 {
		t.Fatalf("会话数 = %d, want 2", len(list))
	}

	// 摘要
	st.SetSummary(s1.ID, "测试摘要")
	s1b, _ := st.Get(s1.ID)
	if s1b.Summary != "测试摘要" {
		t.Fatalf("summary = %q", s1b.Summary)
	}

	// 删除
	st.Delete(s1.ID)
	gone, _ := st.Get(s1.ID)
	if gone != nil {
		t.Fatal("删除后应返回 nil")
	}
}

func TestCompactThreshold(t *testing.T) {
	cfg := coreCfgCompact()
	svc := &Service{cfg: cfg, models: NewModelRegistry(&cfg.AI)}
	if svc.compactThreshold() != 9999 {
		t.Fatalf("compactThreshold = %d, want 9999", svc.compactThreshold())
	}
	// 未显式配置 CompactToken 时按上下文窗口的 95%
	cfg2 := coreCfgDefault() // MaxTokens=2048, ContextWindow=0 -> 默认 64000
	svc2 := &Service{cfg: cfg2, models: NewModelRegistry(&cfg2.AI)}
	if svc2.compactThreshold() != 60800 {
		t.Fatalf("compactThreshold = %d, want 60800 (64000*95%%)", svc2.compactThreshold())
	}
	// 显式上下文窗口
	cfg3 := &core.Config{AI: core.AIConfig{ContextWindow: 128000}}
	svc3 := &Service{cfg: cfg3, models: NewModelRegistry(&cfg3.AI)}
	if svc3.compactThreshold() != 121600 {
		t.Fatalf("compactThreshold = %d, want 121600 (128000*95%%)", svc3.compactThreshold())
	}
	// 切换到已知模型自动更新上下文窗口
	cfg4 := &core.Config{AI: core.AIConfig{ContextWindow: 64000}}
	svc4 := &Service{cfg: cfg4, models: NewModelRegistry(&cfg4.AI)}
	// 用已知模型名直接验证上下文窗口推导
	_ = knownContextWindows["gpt-4o"]
	if got := knownContextWindows["gpt-4o"]; got != 128000 {
		t.Fatalf("gpt-4o 上下文窗口 = %d, want 128000", got)
	}
	if got := svc4.models.ContextWindow(); got != 64000 {
		t.Fatalf("未设置模型时 ContextWindow = %d, want 64000", got)
	}
}
