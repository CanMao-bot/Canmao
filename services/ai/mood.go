package ai

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/mood"
	"gobot/store/session"
)

// MoodManager 全局心情管理: 情绪检测 + 主动回复评估
type MoodManager struct {
	store *mood.Store
	bot   *core.Bot
	mu    sync.Mutex
	// 群消息计数 (groupID -> 计数)
	msgCount map[int64]int
	// 主动回复阈值 (每N条消息评估一次)
	interval int
	// AI 情感判断函数(由 Service 注入)
	llm func(ctx context.Context, text string) (EmotionResult, error)
}

func NewMoodManager(st *mood.Store, bot *core.Bot, interval int) *MoodManager {
	if interval <= 0 {
		interval = 10
	}
	return &MoodManager{store: st, bot: bot, msgCount: map[int64]int{}, interval: interval}
}

// EmotionResult AI 情感分析结果
type EmotionResult struct {
	Delta  int    `json:"delta"`  // 心情变化 -30 ~ +30
	Emotion string `json:"emotion"` // happy / neutral / sad / angry
	Reason string `json:"reason"`  // 变化原因(中文)
}

// SetLLM 注入 AI 情感判断函数
func (m *MoodManager) SetLLM(fn func(ctx context.Context, text string) (EmotionResult, error)) {
	m.llm = fn
}

// Current 当前心情状态
func (m *MoodManager) Current() (*mood.State, error) {
	return m.store.Load()
}

// Apply 应用情绪变化, 返回新状态
func (m *MoodManager) Apply(delta int, emotion, reason string) (*mood.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil {
		return nil, err
	}
	from := st.Value
	to := st.Value + delta
	if to < 0 {
		to = 0
	}
	if to > 100 {
		to = 100
	}
	st.Value = to
	st.Emotion = emotion
	st.Reason = reason
	st.UpdatedAt = time.Now().Unix()
	st.History = append(st.History, mood.MoodLog{From: from, To: to, Reason: reason, Time: st.UpdatedAt})
	if len(st.History) > 50 {
		st.History = st.History[len(st.History)-50:]
	}
	if err := m.store.Save(st); err != nil {
		return nil, err
	}
	log.Printf("[mood] 心情变化 %d→%d (%s): %s", from, to, emotion, reason)
	return st, nil
}

// DetectAndApply 从消息中检测情绪并应用
// 优先用 AI 判断(理解讽刺/语气), AI 不可用时回退关键词规则
func (m *MoodManager) DetectAndApply(text string) *mood.State {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	// 排除命令/纯 CQ 码
	if strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, "[CQ:") {
		return nil
	}

	// AI 情感判断
	if m.llm != nil {
		res, err := m.llm(context.Background(), trimmed)
		if err == nil && (res.Delta != 0 || res.Emotion != "") {
			emotion := res.Emotion
			if emotion == "" {
				emotion = "neutral"
			}
			st, aerr := m.Apply(res.Delta, emotion, res.Reason)
			if aerr == nil {
				return st
			}
		}
	}

	// 回退: 关键词规则
	return m.ruleBasedDetect(trimmed)
}

// ruleBasedDetect 关键词规则(AI 不可用时的兜底)
func (m *MoodManager) ruleBasedDetect(text string) *mood.State {
	negative := []string{"废物", "垃圾", "蠢", "滚", "傻逼", "去死", "没用", "讨厌你", "恶心", "混蛋", "笨"}
	positive := []string{"谢谢", "好棒", "厉害", "聪明", "真棒", "太好了", "喜欢你", "感谢", "不错", "优秀", "辛苦了"}
	help := []string{"帮我", "求", "麻烦你", "请你", "能不能"}

	for _, w := range negative {
		if strings.Contains(text, w) {
			st, _ := m.Apply(-15, "sad", "被用户负面评价")
			return st
		}
	}
	for _, w := range positive {
		if strings.Contains(text, w) {
			st, _ := m.Apply(10, "happy", "收到用户正面反馈")
			return st
		}
	}
	for _, w := range help {
		if strings.Contains(text, w) {
			st, _ := m.Apply(3, "happy", "被用户求助(乐于助人)")
			return st
		}
	}
	return nil
}

// CountAndMaybeProactive 群消息计数, 每N条触发主动回复评估
// 返回是否应主动回复
func (m *MoodManager) CountAndMaybeProactive(groupID int64) bool {
	m.mu.Lock()
	m.msgCount[groupID]++
	count := m.msgCount[groupID]
	if count >= m.interval {
		m.msgCount[groupID] = 0
		m.mu.Unlock()
		return m.shouldProactive()
	}
	m.mu.Unlock()
	return false
}

// shouldProactive 根据心情决定是否主动回复
// 心情越好越愿意搭话; 生气/难过时倾向沉默
func (m *MoodManager) shouldProactive() bool {
	st, err := m.store.Load()
	if err != nil {
		return false
	}
	// 心情 0-100 → 主动概率 15%-80%
	p := 0.15 + float64(st.Value)/100.0*0.65
	return rand.Float64() < p
}

// ProactivePrompt 生成主动回复的提示(带当前心情)
func (m *MoodManager) ProactivePrompt() (string, error) {
	st, err := m.store.Load()
	if err != nil {
		return "", err
	}
	emotionTxt := map[string]string{
		"happy":   "你心情很好, 心情愉悦, 想和群里的人聊聊天。",
		"neutral": "你心情平稳, 可以偶尔参与一下群聊。",
		"sad":     "你心情有些低落, 不太想说话。",
		"angry":   "你心情不好, 不想主动说话。",
	}[st.Emotion]
	if emotionTxt == "" {
		emotionTxt = "你心情平稳。"
	}
	reason := ""
	if st.Reason != "" {
		reason = " 原因: " + st.Reason
	}
	return fmt.Sprintf("你是一个QQ群里的智能助手, 现在你在群里。%s%s\n"+
		"请自然地发一句话参与群聊, 可以是对刚才话题的回应、一个轻松的吐槽或一个提问。\n"+
		"不要太长, 一句话即可, 不要用@, 不要加前缀, 不要重复你最近已经说过的内容。", emotionTxt, reason), nil
}

	var _ = context.Background

// proactiveReply 主动回复: 带最近群聊上下文, 用主模型生成一句搭话发给群
func (s *Service) proactiveReply(ctx context.Context, groupID int64) {
	if s.moodMgr == nil {
		return
	}
	prompt, err := s.moodMgr.ProactivePrompt()
	if err != nil {
		return
	}
	msgs := []Message{{Role: "system", Content: prompt}}
	// 带上群会话最近消息, 让搭话贴合当前话题
	if s.session != nil {
		if ses, serr := s.session.GetCurrent("group", groupID, 0, ""); serr == nil && ses != nil && len(ses.Messages) > 0 {
			start := len(ses.Messages) - 10
			if start < 0 {
				start = 0
			}
			for _, m := range ses.Messages[start:] {
				msgs = append(msgs, Message{Role: m.Role, Content: m.Content})
			}
			msgs = append(msgs, Message{Role: "user", Content: "(以上是最近群聊记录) 请自然地发一句话参与群聊。"})
		}
	}
	// 用当前模型生成
	lvl := s.models.ModelForLevel(levelMember)
	cctx := WithModelOverride(ctx, lvl)
	msg, err := s.client.Complete(cctx, msgs, nil)
	if err != nil {
		log.Printf("[mood] 主动回复生成失败: %v", err)
		return
	}
	text := strings.TrimSpace(msg.TextContent())
	if text == "" || len([]rune(text)) > 100 {
		return
	}
	// 发送到群
	if err := s.bot.Sender.SendGroupMsg(groupID, 0, []core.Segment{core.TextSegment(text)}); err != nil {
		log.Printf("[mood] 主动回复发送失败: %v", err)
	}
	// 把自己的搭话写回群会话, 避免后续看不到自己说过什么而重复话题
	if s.session != nil {
		if ses, serr := s.session.GetCurrent("group", groupID, 0, ""); serr == nil && ses != nil {
			s.session.Append(ses.ID, []session.Message{{Role: "assistant", Content: text, Time: time.Now().Unix()}}, s.cfg.AI.MaxHistory)
		}
	}
	log.Printf("[mood] 主动回复: %s", text)
}
