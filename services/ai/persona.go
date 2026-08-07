package ai

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/persona"
)

// PersonaManager 人设系统: 注入人设 + 自我优化
type PersonaManager struct {
	store    *persona.Store
	bot      *core.Bot
	cfg      *core.Config
	msgCount map[int64]int
	mu       sync.Mutex
	// 由 Service 注入的 LLM 调用函数
	llm func(ctx context.Context, prompt string) (string, error)
}

func NewPersonaManager(st *persona.Store, bot *core.Bot, cfg *core.Config) *PersonaManager {
	return &PersonaManager{store: st, bot: bot, cfg: cfg, msgCount: map[int64]int{}}
}

// SetLLM 注入 LLM 调用函数
func (m *PersonaManager) SetLLM(fn func(ctx context.Context, prompt string) (string, error)) {
	m.llm = fn
}

// Current 当前人设
func (m *PersonaManager) Current() (*persona.Persona, error) {
	return m.store.Load()
}

// SetBase 设置基础人设(初始化时)
func (m *PersonaManager) SetBase(base string) error {
	p, err := m.store.Load()
	if err != nil {
		return err
	}
	p.Base = base
	return m.store.Save(p)
}

// InjectPrompt 将人设注入 system prompt
func (m *PersonaManager) InjectPrompt(base string) string {
	p, err := m.store.Load()
	if err != nil || p == nil {
		return base
	}
	var b strings.Builder
	b.WriteString("=== 你的设定(人设) ===\n")
	if p.Base != "" {
		b.WriteString(p.Base + "\n")
	}
	if p.Style != "" {
		b.WriteString("说话风格: " + p.Style + "\n")
	}
	if len(p.Traits) > 0 {
		b.WriteString("性格特质: " + strings.Join(p.Traits, ", ") + "\n")
	}
	if len(p.Rules) > 0 {
		b.WriteString("行为准则:\n")
		for _, r := range p.Rules {
			b.WriteString("- " + r + "\n")
		}
	}
	if b.Len() == 0 {
		return base
	}
	return base + "\n\n" + b.String()
}

// MaybeSelfImprove 每隔N条消息反思一次对话, 自我优化人设
func (m *PersonaManager) MaybeSelfImprove(ctx context.Context, groupID int64, recentConversation string) {
	if !m.cfg.Persona.SelfImprove {
		return
	}
	m.mu.Lock()
	m.msgCount[groupID]++
	count := m.msgCount[groupID]
	if count < m.cfg.Persona.ImproveEvery {
		m.mu.Unlock()
		return
	}
	m.msgCount[groupID] = 0
	m.mu.Unlock()

	m.selfImprove(ctx, recentConversation)
}

// selfImprove 调用模型反思对话, 提取人设优化建议
func (m *PersonaManager) selfImprove(ctx context.Context, recentConversation string) {
	// 获取当前人设
	p, err := m.store.Load()
	if err != nil {
		return
	}

	prompt := "你是人设优化器。以下是当前人设和一个AI助手的最近对话。\n\n"
	prompt += "=== 当前人设 ===\n" + m.personaSummary(p) + "\n\n"
	prompt += "=== 最近对话 ===\n" + recentConversation + "\n\n"
	prompt += "请分析这段对话中, 用户对AI的偏好和反馈(比如是否喜欢简洁、幽默、详细, 是否纠正过AI的行为)。\n"
	prompt += "输出一个 JSON, 格式: {\"style\": \"优化后的说话风格描述\", \"traits\": [\"新特质\"], \"rules\": [\"新行为准则\"]}\n"
	prompt += "只输出 JSON, 不要其他内容。如果没有明显偏好, 输出 {\"style\":\"\", \"traits\":[], \"rules\":[]}"

	if m.llm == nil {
		return
	}
	msg, err := m.llm(ctx, prompt)
	if err != nil {
		log.Printf("[persona] 自我优化失败: %v", err)
		return
	}

	// 解析 JSON
	var result struct {
		Style  string   `json:"style"`
		Traits []string `json:"traits"`
		Rules  []string `json:"rules"`
	}
	if err := json.Unmarshal([]byte(msg), &result); err != nil {
		log.Printf("[persona] 优化结果解析失败: %v", err)
		return
	}

	changed := false
	if result.Style != "" && result.Style != p.Style {
		p.Style = result.Style
		changed = true
	}
	if len(result.Traits) > 0 {
		p.Traits = result.Traits
		changed = true
	}
	if len(result.Rules) > 0 {
		p.Rules = result.Rules
		changed = true
	}
	if changed {
		p.Learnings = append(p.Learnings, persona.Learning{
			Content: "style=" + result.Style + " traits=" + strings.Join(result.Traits, ",") + " rules=" + strings.Join(result.Rules, ","),
			Kind:    "self_improve", Source: "对话反思", CreatedAt: time.Now().Unix(),
		})
		if err := m.store.Save(p); err != nil {
			log.Printf("[persona] 保存失败: %v", err)
		}
		log.Printf("[persona] 已自我优化: %+v", result)
	}
}

func (m *PersonaManager) personaSummary(p *persona.Persona) string {
	var b strings.Builder
	if p.Base != "" {
		b.WriteString("基础设定: " + p.Base + "\n")
	}
	if p.Style != "" {
		b.WriteString("风格: " + p.Style + "\n")
	}
	if len(p.Traits) > 0 {
		b.WriteString("特质: " + strings.Join(p.Traits, ", ") + "\n")
	}
	if len(p.Rules) > 0 {
		b.WriteString("准则: " + strings.Join(p.Rules, "; ") + "\n")
	}
	if b.Len() == 0 {
		return "(无)"
	}
	return strings.TrimSpace(b.String())
}
