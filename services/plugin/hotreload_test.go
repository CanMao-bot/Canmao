package plugin

import (
	"context"
	"testing"

	"gobot/core"
	"gobot/pluginapi"
	"gobot/services/ai"
)

// fakePlugin 热加载测试用假插件
type fakePlugin struct {
	pluginapi.Base
	name   string
	closed *bool
	tools  []pluginapi.Tool
}

func (f *fakePlugin) Name() string { return f.name }
func (f *fakePlugin) OnEvent(ctx context.Context, ev *pluginapi.Event) (string, bool) {
	return "pong:" + f.name, true
}
func (f *fakePlugin) Tools() []pluginapi.Tool { return f.tools }
func (f *fakePlugin) Close() error {
	if f.closed != nil {
		*f.closed = true
	}
	return nil
}

// TestSwapPlugins 替换后旧插件被 Close、事件分发走新列表
func TestSwapPlugins(t *testing.T) {
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "10000"}})
	bot.SetSender(&fakeSenderT{})
	s := NewService(nil, nil, bot, &core.Config{}, nil, nil)

	oldClosed := new(bool)
	p1 := &fakePlugin{name: "old", closed: oldClosed}
	s.swapPlugins([]pluginapi.Plugin{p1})

	// 事件走旧插件
	ev := &core.Event{Type: "message", DetailType: "private", UserID: 1}
	if !s.Handle(context.Background(), bot, ev) {
		t.Fatal("插件应消费事件")
	}

	// 换血: 旧插件 Close, 新列表生效
	p2 := &fakePlugin{name: "new"}
	msg := s.swapPlugins([]pluginapi.Plugin{p2})
	if msg == "" {
		t.Fatal("swapPlugins 应返回摘要")
	}
	if !*oldClosed {
		t.Fatal("旧插件未被 Close")
	}
	if !s.Handle(context.Background(), bot, ev) {
		t.Fatal("新插件应消费事件")
	}
}

// TestHandleConsumesNewPluginAfterSwap 空列表换血后, 插件桥不再消费事件(交回 AI)
func TestHandleConsumesNewPluginAfterSwap(t *testing.T) {
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "10000"}})
	bot.SetSender(&fakeSenderT{})
	s := NewService(nil, nil, bot, &core.Config{}, nil, nil)
	s.swapPlugins([]pluginapi.Plugin{})

	ev := &core.Event{Type: "message", DetailType: "private", UserID: 1}
	if s.Handle(context.Background(), bot, ev) {
		t.Fatal("空插件列表不应消费事件")
	}
}

// TestPluginReloadCommand /plugin reload 权限边界: 主人可调, 旁观者拒绝
func TestPluginReloadCommand(t *testing.T) {
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "10000"}})
	bot.SetSender(&fakeSenderT{})
	s := NewService(nil, nil, bot, &core.Config{}, nil, nil)

	evMaster := &core.Event{Type: "message", DetailType: "private", UserID: 10000,
		Message: []core.Segment{core.TextSegment("/plugin reload")}}
	if !s.Handle(context.Background(), bot, evMaster) {
		t.Fatal("主人调用 /plugin reload 应被消费")
	}
	// loader 未配置 → 执行失败路径但命令被消费
	evOther := &core.Event{Type: "message", DetailType: "private", UserID: 20000,
		Message: []core.Segment{core.TextSegment("/plugin reload")}}
	if !s.Handle(context.Background(), bot, evOther) {
		t.Fatal("非主人调用 /plugin reload 也应被消费(拒绝并提示)")
	}
	if s.isManager(20000) {
		t.Fatal("20000 不应被判定为管理者")
	}
	if !s.isManager(10000) {
		t.Fatal("主人应被判定为管理者")
	}
}

// TestAttachToolPrefixReplace attach 的工具按插件名前缀幂等替换, 不重复注册
func TestAttachToolPrefixReplace(t *testing.T) {
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "10000"}})
	aiSvc := ai.New(&core.Config{}, nil, nil, nil, bot)
	bot.SetSender(&fakeSenderT{})

	mkTool := func(n string) pluginapi.Tool {
		return pluginapi.Tool{
			Name: n, Description: "t",
			Parameters: map[string]*pluginapi.Param{},
			Call: func(ctx context.Context, args map[string]interface{}) (string, error) {
				return "v2-" + n, nil
			},
		}
	}

	s := NewService(nil, aiSvc, bot, &core.Config{}, nil, nil)
	p := &fakePlugin{name: "demo", tools: []pluginapi.Tool{mkTool("greet")}}
	s.swapPlugins([]pluginapi.Plugin{p})

	// 再次换血同名插件: 工具不应重复
	s.swapPlugins([]pluginapi.Plugin{p})

	var greet []ai.Tool
	for _, t := range aiSvc.Tools() {
		if t.Function.Name == "demo_greet" {
			greet = append(greet, t)
		}
	}
	if len(greet) != 1 {
		t.Fatalf("demo_greet 重复注册: %d 个", len(greet))
	}
	out, err := greet[0].Callback(context.Background(), nil)
	if err != nil || out != "v2-greet" {
		t.Fatalf("工具回调 = %q, %v", out, err)
	}
}
