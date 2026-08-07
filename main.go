package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gobot/adapter"
	"gobot/core"
	"gobot/services/ai"
	"gobot/services/acp"
	"gobot/services/dashscope"
	"gobot/services/file"
	"gobot/services/mcp"
	"gobot/services/memory"
	"gobot/services/plugin"
	"gobot/services/shell"
	"gobot/services/skill"
	"gobot/services/sched"
	"gobot/services/web"
	"gobot/store/allow"
	"gobot/store/group"
	memstore "gobot/store/memory"
	moodstore "gobot/store/mood"
	personastore "gobot/store/persona"
	"gobot/store/perm"
	"gobot/store/provider"
	schedstore "gobot/store/sched"
	"gobot/store/session"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := core.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 存储层
	permStore, err := perm.New(cfg.Data.Dir)
	if err != nil {
		log.Fatalf("初始化权限存储失败: %v", err)
	}
	defer permStore.Close()

	sessStore, err := session.New(cfg.Data.Dir)
	if err != nil {
		log.Fatalf("初始化会话存储失败: %v", err)
	}
	defer sessStore.Close()

	allowStore, err := allow.New(cfg.Data.Dir)
	if err != nil {
		log.Fatalf("初始化免审批存储失败: %v", err)
	}
	defer allowStore.Close()

	// 核心 + 适配层
	bot := core.NewBot(cfg)
	ws := adapter.NewOneBot(cfg.OneBot.WSURL, cfg.OneBot.Token)
	bot.SetSender(ws)

	// 事件分发
	ws.SetEventHandler(func(ev *core.Event) {
		if ev == nil {
			log.Println("[adapter] WS 断开")
			return
		}
		if data, _ := json.Marshal(ev); len(data) > 0 {
			log.Printf("[event] %s", data)
		}
		bot.Dispatch(ev)
	})

	if err := ws.Connect(); err != nil {
		log.Fatalf("连接 OneBot 失败: %v", err)
	}

	// 加载 Skills
	skills, err := skill.LoadAll(cfg.Skills.Dir)
	if err != nil {
		log.Fatalf("加载 Skills 失败: %v", err)
	}
	log.Printf("[skill] 加载 %d 个 skills", len(skills))

	// AI 服务
	aiSvc := ai.New(cfg, permStore, sessStore, allowStore, bot)
	// Provider 持久化存储
	provStore, err := provider.New(cfg.Data.Dir)
	if err != nil {
		log.Fatalf("初始化 provider 存储失败: %v", err)
	}
	aiSvc.SetProviderStore(provStore)
	if err := aiSvc.ModelRegistry().InitFromConfig(); err != nil {
		log.Printf("[ai] 导入默认 provider 失败: %v", err)
	}
	// 用户组模型覆盖存储
	groupStore, err := group.New(cfg.Data.Dir)
	if err == nil {
		aiSvc.ModelRegistry().SetGroupStore(groupStore)
	}
	// 记忆机制
	if cfg.Memory.Enabled {
		memStore, err := memstore.New(cfg.Data.Dir)
		if err != nil {
			log.Printf("[memory] 初始化记忆存储失败: %v", err)
		} else {
			defer memStore.Close()
			embedder := memory.NewEmbedder(memory.EmbeddingConfig{
				BaseURL: cfg.Memory.EmbedBaseURL,
				APIKey:  cfg.Memory.EmbedAPIKey,
				Model:   cfg.Memory.EmbedModel,
			})
			memMgr := memory.New(memStore, embedder, cfg)
			aiSvc.SetMemoryManager(memMgr)
			log.Printf("[memory] 记忆机制已启用 (embed=%s)", cfg.Memory.EmbedModel)
		}
	}
	// 心情系统
	if cfg.Mood.Enabled {
		moodStore, err := moodstore.New(cfg.Data.Dir)
		if err != nil {
			log.Printf("[mood] 初始化心情存储失败: %v", err)
		} else {
			moodMgr := ai.NewMoodManager(moodStore, bot, cfg.Mood.EveryN)
			aiSvc.SetMoodManager(moodMgr)
			log.Printf("[mood] 心情系统已启用 (proactive=%v, everyN=%d)", cfg.Mood.Proactive, cfg.Mood.EveryN)
		}
	}
	// 人设系统
	if cfg.Persona.Enabled {
		personaStore, err := personastore.New(cfg.Data.Dir)
		if err != nil {
			log.Printf("[persona] 初始化人设存储失败: %v", err)
		} else {
			pm := ai.NewPersonaManager(personaStore, bot, cfg)
			if cfg.Persona.Base != "" {
				pm.SetBase(cfg.Persona.Base)
			}
			aiSvc.SetPersonaManager(pm)
			log.Printf("[persona] 人设系统已启用 (selfImprove=%v, everyN=%d)", cfg.Persona.SelfImprove, cfg.Persona.ImproveEvery)
		}
	}
	// Web 搜索/爬取/浏览器
	if cfg.Web.Enabled {
		webSvc := web.New(web.Config{
			Proxy:   cfg.Web.Proxy,
			Timeout: 30,
		})
		aiSvc.AddTool(webSvc.NewSearchTool())
		aiSvc.AddTool(webSvc.NewFetchTool())
		browserPath := cfg.Web.BrowserPath
		if browserPath == "" {
			browserPath = filepath.Join(cwd(), "scripts/browser.py")
		}
		if fileExists(browserPath) {
			aiSvc.AddTool(webSvc.NewBrowserTool(browserPath))
		} else {
			log.Printf("[web] 未找到浏览器脚本 %s, 跳过 browser 工具", browserPath)
		}
		log.Printf("[web] Web 能力已启用 (search/fetch/browser)")
	}

	// 定时任务服务
	var schedSvc *sched.Scheduler
	if cfg.Sched.Enabled {
		schedStore, err := schedstore.New(cfg.Data.Dir)
		if err != nil {
			log.Printf("[sched] 初始化定时存储失败: %v", err)
		} else {
			defer schedStore.Close()
			aiSvc.SetSchedStore(schedStore)
			schedSvc = sched.New(schedStore, bot)
			bot.RegisterService(schedSvc)
			log.Printf("[sched] 定时任务服务已启用 (scheduleTool=%v)", cfg.Sched.ScheduleTool)
		}
	}
	// 注入 skill 系统提示
	if len(skills) > 0 {
		aiSvc.SetSkillContext(skill.RenderAllSkills(skills))
	}
	// 图像生成/TTS
	if cfg.Media.Enabled && cfg.Media.APIKey != "" {
		mediaClient := dashscope.New(dashscope.Config{
			BaseURL:     cfg.Media.BaseURL,
			APIKey:      cfg.Media.APIKey,
			ImageModel:  cfg.Media.ImageModel,
			TTSEndpoint: cfg.Media.TTSEndpoint,
		})
		aiSvc.SetMediaClient(mediaClient, cfg.Media.Voice)
		aiSvc.SetFileDir(cfg.Data.Dir + "/files")
		log.Printf("[media] 图像生成(%s) + TTS(%s) 已启用", cfg.Media.ImageModel, cfg.Media.TTSEndpoint)
	}

	// ACP: 调用 opencode 干活
	var acpClient *acp.Client
	if cfg.ACP.Enabled {
		acpClient = acp.New(acp.Config{
			Binary: cfg.ACP.Binary,
			Cwd:    cfg.ACP.Cwd,
		})
		if err := acpClient.Start(ctx); err != nil {
			log.Printf("[acp] opencode ACP 启动失败: %v", err)
			acpClient = nil
		} else if cfg.ACP.Tool {
			aiSvc.SetACPClient(acpClient)
			log.Printf("[acp] opencode ACP 已连接")
		}
	}
	// 后台预拉取模型列表(若已配置 provider)
	go func() {
		if aiSvc.HasDefaultModel() {
			if _, err := aiSvc.ModelRegistry().Fetch(ctx); err != nil {
				log.Printf("[ai] 预拉取模型列表失败: %v", err)
			} else {
				log.Printf("[ai] 已拉取 %d 个模型", len(aiSvc.ModelRegistry().List()))
			}
		} else {
			log.Printf("[ai] 未配置模型提供商, 等待主人配置")
		}
	}()

	// 文件管理服务 (自动保存收到的图片/文件, 提供上传能力)
	fileMgr := file.New(cfg.Data.Dir, bot, ws, permStore, cfg.Data.FileRetentionDays)
	bot.RegisterService(fileMgr)

	// 加载 MCP
	mcpMgr := mcp.New(&cfg.MCP)
	if err := mcpMgr.ConnectAll(ctx); err != nil {
		log.Printf("[mcp] 警告: %v", err)
	}
	for _, t := range mcpMgr.Tools() {
		aiSvc.AddTool(t)
	}
	log.Printf("[mcp] 注册 %d 个 MCP 工具", len(mcpMgr.Tools()))

	// 文件管理 AI 工具 (含 read_file/write_file/list_directory)
	for _, t := range fileMgr.Tools() {
		aiSvc.AddTool(t)
	}
	log.Printf("[file] 注册 %d 个文件工具", len(fileMgr.Tools()))

	// 系统命令工具 (run_command, 需人工审批)
	aiSvc.AddTool(shell.NewShellTool())
	log.Printf("[shell] 注册系统命令工具 run_command")

	// 加载 .so 插件
	pluginMgr := plugin.New(cfg.Plugin.Dir)
	plugs, err := pluginMgr.LoadAll()
	if err != nil {
		log.Printf("[plugin] 警告: %v", err)
	}
	ps := plugin.NewService(plugs, aiSvc, bot, cfg, ws, permStore)
	bot.RegisterService(ps)

	// 注册 AI 服务(最后注册, 兜底响应)
	bot.RegisterService(aiSvc)

	bot.Run()
	if schedSvc != nil {
		schedSvc.Start()
	}
	log.Printf("[gobot] %s 启动完成", cfg.Bot.Name)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("收到退出信号, 正在关闭...")
	mcpMgr.Close()
	pluginMgr.Close()
	fileMgr.Close()
	if schedSvc != nil {
		schedSvc.Close()
	}
	if acpClient != nil {
		acpClient.Close()
	}
	ws.Close()
	cancel()
}

func cwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
