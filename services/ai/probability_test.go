package ai

import (
	"os"
	"path/filepath"
	"testing"

	"gobot/core"
)

func TestReplyProbabilityConfig(t *testing.T) {
	dir := t.TempDir()
	// 未配置 → 默认全触发(1.0)
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("bot:\n  master_id: \"1\"\n"), 0o644)
	cfg, err := core.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.ReplyProbability == nil || *cfg.AI.ReplyProbability != 1.0 {
		t.Fatalf("未配置时应默认 1.0, got %v", cfg.AI.ReplyProbability)
	}

	// 配置 1 → 全触发
	os.WriteFile(cfgPath, []byte("ai:\n  reply_probability: 1\n"), 0o644)
	cfg, _ = core.LoadConfig(cfgPath)
	if *cfg.AI.ReplyProbability != 1.0 {
		t.Fatalf("reply_probability: 1 应解析为 1.0")
	}

	// 配置 0 → 禁用
	os.WriteFile(cfgPath, []byte("ai:\n  reply_probability: 0\n"), 0o644)
	cfg, _ = core.LoadConfig(cfgPath)
	if *cfg.AI.ReplyProbability != 0.0 {
		t.Fatalf("reply_probability: 0 应解析为 0.0")
	}

	// 配置 0.5 → 0.5
	os.WriteFile(cfgPath, []byte("ai:\n  reply_probability: 0.5\n"), 0o644)
	cfg, _ = core.LoadConfig(cfgPath)
	if *cfg.AI.ReplyProbability != 0.5 {
		t.Fatalf("reply_probability: 0.5 应解析为 0.5")
	}
}

func TestProbabilityGate(t *testing.T) {
	// p=1 全触发
	svc := &Service{cfg: &core.Config{AI: core.AIConfig{ReplyProbability: fp(1.0)}}}
	if !svc.probabilityGate() {
		t.Fatal("p=1 应始终触发")
	}
	// p=0 禁用
	svc2 := &Service{cfg: &core.Config{AI: core.AIConfig{ReplyProbability: fp(0.0)}}}
	if svc2.probabilityGate() {
		t.Fatal("p=0 应禁用")
	}
	// nil → 全触发
	svc3 := &Service{cfg: &core.Config{AI: core.AIConfig{}}}
	if !svc3.probabilityGate() {
		t.Fatal("nil 应默认全触发")
	}
}

func fp(v float64) *float64 { return &v }
