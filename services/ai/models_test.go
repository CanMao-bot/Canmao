package ai

import (
	"os"
	"path/filepath"
	"testing"

	"gobot/core"
	"gobot/store/group"
	"gobot/store/provider"
)

func cfgAI() *core.AIConfig { return &core.AIConfig{BaseURL: "x", APIKey: "y"} }

func TestGroupModelStore(t *testing.T) {
	dir := t.TempDir()
	gs, err := group.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := gs.Load()
	if err != nil || m == nil {
		t.Fatalf("Load = %v, %v", m, err)
	}
	if m.Master != "" || m.Admin != "" || m.Member != "" {
		t.Fatal("初始应全空")
	}
	m.Master = "gpt-4o"
	if err := gs.Save(m); err != nil {
		t.Fatal(err)
	}
	m2, _ := gs.Load()
	if m2.Master != "gpt-4o" {
		t.Fatalf("Master = %q", m2.Master)
	}
}

func TestModelForLevel(t *testing.T) {
	dir := t.TempDir()
	prov, err := provider.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 写入一个 provider
	d := &provider.Data{Current: "default", Providers: []provider.Provider{
		{Name: "default", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-x", Model: "deepseek-v4-flash"},
	}}
	if err := prov.Save(d); err != nil {
		t.Fatal(err)
	}

	gs, _ := group.New(dir)
	r := NewModelRegistry(cfgAI())
	r.SetStore(prov)
	r.SetGroupStore(gs)

	// 未覆盖时用 provider 默认
	if got := r.ModelForLevel(levelMember); got != "deepseek-v4-flash" {
		t.Fatalf("member model = %q", got)
	}
	// 设置 admin 组覆盖
	if err := r.SetGroupModel(levelAdmin, "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if got := r.ModelForLevel(levelAdmin); got != "gpt-4o" {
		t.Fatalf("admin model = %q, want gpt-4o", got)
	}
	if got := r.ModelForLevel(levelMember); got != "deepseek-v4-flash" {
		t.Fatalf("member model 不应受影响 = %q", got)
	}
	// 持久化检查
	m, _ := gs.Load()
	if m.Admin != "gpt-4o" {
		t.Fatalf("持久化 Admin = %q", m.Admin)
	}
	os.RemoveAll(filepath.Join(dir, "model_groups.json"))
}
