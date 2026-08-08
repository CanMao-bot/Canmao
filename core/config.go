package core

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bot    BotConfig    `yaml:"bot"`
	OneBot OneBotConfig `yaml:"onebot"`
	Data   DataConfig   `yaml:"data"`
	AI     AIConfig     `yaml:"ai"`
	Memory MemoryConfig `yaml:"memory"`
	Mood   MoodConfig   `yaml:"mood"`
	Persona PersonaConfig `yaml:"persona"`
	Web    WebConfig    `yaml:"web"`
	Sched  SchedConfig  `yaml:"sched"`
	Media  MediaConfig  `yaml:"media"`
	ACP    ACPConfig    `yaml:"acp"`
	MCP    MCPConfig    `yaml:"mcp"`
	Skills SkillsConfig `yaml:"skills"`
	Plugin PluginConfig `yaml:"plugin"`
}

// ACPConfig 调用 opencode 等 ACP agent 干活
type ACPConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Binary   string `yaml:"binary"`   // opencode 二进制路径
	Cwd      string `yaml:"cwd"`      // 默认工作目录
	Tool     bool   `yaml:"tool"`     // 给 AI 提供 opencode_task 工具
	Timeout  int    `yaml:"timeout"`  // 单任务超时秒
}

// MediaConfig 图像生成/TTS 配置
type MediaConfig struct {
	Enabled     bool   `yaml:"enabled"`
	BaseURL     string `yaml:"base_url"`     // DashScope API, 默认 https://dashscope.aliyuncs.com
	APIKey      string `yaml:"api_key"`
	ImageModel  string `yaml:"image_model"`  // 图像生成模型, 默认 wanx-v1
	TTSEndpoint string `yaml:"tts_endpoint"` // TTS 模型, 默认 qwen3-tts-flash
	Voice       string `yaml:"voice"`        // 默认音色
}

// SchedConfig 定时任务配置
type SchedConfig struct {
	Enabled     bool `yaml:"enabled"`
	ScheduleTool bool `yaml:"schedule_tool"` // 给 AI 定时任务工具
}

// WebConfig web 搜索/爬取/浏览器配置
type WebConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Proxy       string `yaml:"proxy"`        // HTTP 代理(境内访问国外网站)
	BrowserPath string `yaml:"browser_path"` // playwright 脚本路径
}

// PersonaConfig 人设系统配置
type PersonaConfig struct {
	Enabled     bool   `yaml:"enabled"`      // 人设系统开关
	Base        string `yaml:"base"`         // 基础人设描述
	SelfImprove bool   `yaml:"self_improve"` // 自我优化开关
	ImproveEvery int   `yaml:"improve_every"`// 每N条消息反思一次
}

// MoodConfig 心情/主动回复配置
type MoodConfig struct {
	Enabled      bool `yaml:"enabled"`        // 心情系统开关
	Proactive    bool `yaml:"proactive"`      // 主动回复开关
	EveryN       int  `yaml:"every_n"`        // 每N条群消息评估一次主动回复
	RememberTool bool `yaml:"remember_tool"`  // 是否给 AI 主动记忆工具
}

// MemoryConfig 记忆机制配置
type MemoryConfig struct {
	Enabled       bool   `yaml:"enabled"`
	EmbedBaseURL  string `yaml:"embed_base_url"`
	EmbedAPIKey   string `yaml:"embed_api_key"`
	EmbedModel    string `yaml:"embed_model"`
	RecallLimit   int    `yaml:"recall_limit"`
	VisionModel   string `yaml:"vision_model"` // 识图转述模型(如 qwen3.6-flash)
}

type BotConfig struct {
	Name                string   `yaml:"name"`
	MasterID            string   `yaml:"master_id"`
	Prefix              string   `yaml:"prefix"`
	AdminIDs            []string `yaml:"admin_ids"`
	LongMessageForward  bool     `yaml:"long_message_forward"`  // 长消息用合并转发发送
	LongMessageThreshold int    `yaml:"long_message_threshold"` // 长消息阈值(字符数)
}

type OneBotConfig struct {
	WSURL  string `yaml:"ws_url"`
	Token  string `yaml:"token"`
	SelfID string `yaml:"self_id"`
}

type DataConfig struct {
	Dir               string `yaml:"dir"`
	FileRetentionDays int    `yaml:"file_retention_days"` // 缓存文件保留天数, 0=不清理
}

type AIConfig struct {
	BaseURL      string  `yaml:"base_url"`
	APIKey       string  `yaml:"api_key"`
	Model        string  `yaml:"model"`
	SystemPrompt string  `yaml:"system_prompt"`
	MaxTokens    int     `yaml:"max_tokens"`
	Temperature  float64 `yaml:"temperature"`
	Timeout      int     `yaml:"timeout"`
	DefaultOn    bool    `yaml:"default_on"`
	MaxHistory   int     `yaml:"max_history"`
	CompactToken int     `yaml:"compact_token"`
	SessionMode  string  `yaml:"session_mode"`  // group: merged=全群合并 / separate=按用户独立
	MentionOnly  bool    `yaml:"mention_only"`  // 群内仅 @bot 时响应
	ReplyProbability *float64 `yaml:"reply_probability"` // 群消息概率触发 AI(0~1, 0=禁用), nil=默认全触发
	ContextWindow int    `yaml:"context_window"` // 模型上下文窗口 token 数
	MaxIterations int    `yaml:"max_iterations"` // Agent 工具调用最大轮数
	ToolResultMax int    `yaml:"tool_result_max"` // 工具结果最大字符数(超出截断)
}

type MCPConfig struct {
	Servers []MCPServer `yaml:"servers"`
}

type MCPServer struct {
	Name      string   `yaml:"name"`
	Transport string   `yaml:"transport"`
	Command   string   `yaml:"command"`
	Args      []string `yaml:"args"`
	Env       []string `yaml:"env"`
	Enabled   bool     `yaml:"enabled"`
}

type SkillsConfig struct {
	Dir string `yaml:"dir"`
}

type PluginConfig struct {
	Dir string `yaml:"dir"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s: %w", path, err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	cfg.applyDefaults()
	abs, err := filepath.Abs(path)
	if err == nil {
		cfg.Data.Dir = absDir(filepath.Dir(abs), cfg.Data.Dir)
		cfg.Skills.Dir = absDir(filepath.Dir(abs), cfg.Skills.Dir)
		cfg.Plugin.Dir = absDir(filepath.Dir(abs), cfg.Plugin.Dir)
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Bot.Name == "" {
		c.Bot.Name = "罐头喵"
	}
	if c.Bot.Prefix == "" {
		c.Bot.Prefix = "/"
	}
	if c.Bot.LongMessageThreshold <= 0 {
		c.Bot.LongMessageThreshold = 200 // 群里约200汉字即霸屏, 超过转合并转发
	}
	if c.Data.Dir == "" {
		c.Data.Dir = "data"
	}
	if c.AI.MaxTokens <= 0 {
		c.AI.MaxTokens = 4096
	}
	if c.AI.Temperature == 0 {
		c.AI.Temperature = 0.7
	}
	if c.AI.Timeout <= 0 {
		c.AI.Timeout = 120
	}
	if c.AI.MaxHistory <= 0 {
		c.AI.MaxHistory = 200
	}
	if c.AI.SessionMode == "" {
		c.AI.SessionMode = "merged"
	}
	if c.AI.ReplyProbability == nil {
		v := 1.0 // 默认全触发
		c.AI.ReplyProbability = &v
	} else if *c.AI.ReplyProbability < 0 {
		*c.AI.ReplyProbability = 0
	} else if *c.AI.ReplyProbability > 1 {
		*c.AI.ReplyProbability = 1.0
	}
	if c.AI.MaxIterations <= 0 {
		c.AI.MaxIterations = 200
	}
	if c.AI.ToolResultMax <= 0 {
		c.AI.ToolResultMax = 3000
	}
	if c.AI.ContextWindow <= 0 {
		c.AI.ContextWindow = 64000
	}
	if c.Skills.Dir == "" {
		c.Skills.Dir = "skills"
	}
	if c.Plugin.Dir == "" {
		c.Plugin.Dir = "plugins"
	}
	if c.Memory.RecallLimit <= 0 {
		c.Memory.RecallLimit = 5
	}
	if c.Memory.EmbedModel == "" {
		c.Memory.EmbedModel = "text-embedding-v4"
	}
	if c.Media.BaseURL == "" {
		c.Media.BaseURL = "https://dashscope.aliyuncs.com"
	}
	if c.Media.ImageModel == "" {
		c.Media.ImageModel = "qwen-image-2.0"
	}
	if c.Media.TTSEndpoint == "" {
		c.Media.TTSEndpoint = "qwen3-tts-flash"
	}
	if c.ACP.Timeout <= 0 {
		c.ACP.Timeout = 300
	}
	if c.Mood.EveryN <= 0 {
		c.Mood.EveryN = 10
	}
	if c.Persona.ImproveEvery <= 0 {
		c.Persona.ImproveEvery = 20
	}
}

func absDir(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
