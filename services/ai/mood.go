package ai

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/mood"
	"gobot/store/session"
)

// MoodManager 全局心情管理: 情绪检测 + 主动插话判断
type MoodManager struct {
	store *mood.Store
	bot   *core.Bot
	mu    sync.Mutex
	// 群消息计数 (groupID -> 计数)
	msgCount map[int64]int
	// 主动回复阈值 (每N条消息评估一次, 旧行为兼容)
	interval int
	// AI 情感判断函数(由 Service 注入)
	llm func(ctx context.Context, text string) (EmotionResult, error)
	// AI 插话判断函数(由 Service 注入, 含群上下文)
	interjectLLM func(ctx context.Context, text, moodTxt string, recent []Message) (InterjectResult, error)
	// 冷却: 每组下次允许插话的时间
	nextInterject map[int64]time.Time
	// 冷却区间
	cooldownMin, cooldownMax time.Duration
	// 情感关怀冷却(低于普通冷却, 低落时更快再次安慰)
	careCooldown time.Duration
	// 心情驱动的自主管理操作: 记录"惹怒 bot"的用户(groupID -> userID)
	aggressors map[int64]int64
	// 自主惩罚冷却 (groupID -> 上次惩罚时间)
	punishCooldown map[int64]time.Time
	// 自主惩罚间隔(默认 30 分钟, 防止反复惩罚)
	punishInterval time.Duration
	// 自主惩罚开关
	punishEnabled bool
	// 心情阈值: 低于该值(0-100)视为心情极差, 触发自主惩罚
	punishMoodThreshold int
}

func NewMoodManager(st *mood.Store, bot *core.Bot, interval int) *MoodManager {
	if interval <= 0 {
		interval = 10
	}
	return &MoodManager{
		store:          st,
		bot:            bot,
		msgCount:       map[int64]int{},
		interval:       interval,
		nextInterject:  map[int64]time.Time{},
		cooldownMin:    5 * time.Minute,
		cooldownMax:    15 * time.Minute,
		careCooldown:   2 * time.Minute,
		aggressors:     map[int64]int64{},
		punishCooldown: map[int64]time.Time{},
		punishInterval: 30 * time.Minute,
		punishEnabled:  true,
		punishMoodThreshold: 30,
	}
}

// SetPunish 配置心情驱动的自主管理操作
func (m *MoodManager) SetPunish(enabled bool, moodThreshold int, interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.punishEnabled = enabled
	if moodThreshold > 0 {
		m.punishMoodThreshold = moodThreshold
	}
	if interval > 0 {
		m.punishInterval = interval
	}
}

// NoteAggressor 记录惹怒 bot 的用户(负面情绪来源)
func (m *MoodManager) NoteAggressor(groupID, userID int64) {
	if userID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.aggressors == nil {
		m.aggressors = map[int64]int64{}
	}
	m.aggressors[groupID] = userID
}

// ClearAggressor 清除某群的惹怒记录
func (m *MoodManager) ClearAggressor(groupID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.aggressors, groupID)
}

// MaybePunishAggressor 心情极差时, 对惹怒 bot 的用户执行自主管理操作(禁言/改昵称)。
// 这是 AI 自主行为, 不经过人工审批。返回 (是否执行了惩罚, 动作描述)。
func (m *MoodManager) MaybePunishAggressor(groupID int64) (bool, string) {
	if !m.punishEnabled {
		return false, ""
	}
	m.mu.Lock()
	aggressor, has := m.aggressors[groupID]
	if !has {
		m.mu.Unlock()
		return false, ""
	}
	if last, ok := m.punishCooldown[groupID]; ok && time.Since(last) < m.punishInterval {
		m.mu.Unlock()
		return false, ""
	}
	m.mu.Unlock()

	st, err := m.store.Load()
	if err != nil {
		return false, ""
	}
	// 仅心情极差(低于阈值)或明确生气时才惩罚; 且不惩罚自己
	if st.Value > m.punishMoodThreshold && st.Emotion != "angry" {
		return false, ""
	}
	selfID, _ := strconv.ParseInt(m.bot.Cfg.OneBot.SelfID, 10, 64)
	if aggressor == selfID {
		return false, ""
	}
	// 主人/管理员不惩罚: 即使惹 bot 生气也不能禁言/改昵称他们
	if m.bot.Cfg.Bot.MasterID == strconv.FormatInt(aggressor, 10) {
		return false, "主人不可惩罚"
	}
	for _, a := range m.bot.Cfg.Bot.AdminIDs {
		if a == strconv.FormatInt(aggressor, 10) {
			return false, "管理员不可惩罚"
		}
	}
	// 进入惩罚: 占位冷却, 防止并发/快速重复
	m.mu.Lock()
	m.punishCooldown[groupID] = time.Now()
	m.mu.Unlock()

	gac, ok := m.bot.Sender.(core.GroupAdminClient)
	if !ok {
		return false, "当前连接不支持群管理操作"
	}

	// 随机选择惩罚动作: 禁言(60-300秒) 或 改昵称
	if rand.Intn(2) == 0 {
		duration := 60 + rand.Intn(240)
		if err := gac.SetGroupBan(groupID, aggressor, duration); err != nil {
			m.mu.Lock()
			delete(m.punishCooldown, groupID)
			m.mu.Unlock()
			return false, "禁言失败: " + err.Error()
		}
		return true, fmt.Sprintf("心情极差, 自主禁言了惹恼我的用户 %d (%d秒)", aggressor, duration)
	}
	card := fmt.Sprintf("😤%d", aggressor%10000)
	if err := gac.SetGroupCard(groupID, aggressor, card); err != nil {
		m.mu.Lock()
		delete(m.punishCooldown, groupID)
		m.mu.Unlock()
		return false, "改昵称失败: " + err.Error()
	}
	return true, fmt.Sprintf("心情极差, 自主把惹恼我的用户 %d 的昵称改成了 %s", aggressor, card)
}

// SetCooldown 设置插话冷却区间(普通搭话)与情感关怀冷却
func (m *MoodManager) SetCooldown(min, max, care time.Duration) {
	if min <= 0 {
		min = 5 * time.Minute
	}
	if max < min {
		max = min
	}
	if care <= 0 {
		care = 2 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldownMin, m.cooldownMax, m.careCooldown = min, max, care
}

// SetInterjectLLM 注入插话判断函数
func (m *MoodManager) SetInterjectLLM(fn func(ctx context.Context, text, moodTxt string, recent []Message) (InterjectResult, error)) {
	m.interjectLLM = fn
}

// CanInterject 检查该群是否已过插话冷却期
func (m *MoodManager) CanInterject(groupID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, ok := m.nextInterject[groupID]
	if !ok {
		return true
	}
	return time.Now().After(next)
}

// MarkInterjected 记录插话并设置下次允许时间
func (m *MoodManager) MarkInterjected(groupID int64, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nextInterject == nil {
		m.nextInterject = map[int64]time.Time{}
	}
	// 情感关怀可更快再次安慰; 普通搭话用随机冷却区间, 减少触发规律性
	if mode == "care" {
		m.nextInterject[groupID] = time.Now().Add(m.careCooldown)
	} else {
		span := int64(m.cooldownMax - m.cooldownMin)
		extra := time.Duration(0)
		if span > 0 {
			extra = time.Duration(rand.Int63n(span))
		}
		m.nextInterject[groupID] = time.Now().Add(m.cooldownMin + extra)
	}
}

// JudgeInterject 判断当前消息是否值得主动插话
// LLM 不可用时回退旧行为: 按心情概率决定是否搭话
func (m *MoodManager) JudgeInterject(ctx context.Context, text, moodTxt string, recent []Message) (InterjectResult, error) {
	if m.interjectLLM != nil {
		return m.interjectLLM(ctx, text, moodTxt, recent)
	}
	if m.shouldProactive() {
		return InterjectResult{Should: true, Mode: "chat"}, nil
	}
	return InterjectResult{Should: false, Mode: "silent"}, nil
}

// EmotionResult AI 情感分析结果
type EmotionResult struct {
	Delta  int    `json:"delta"`  // 心情变化 -30 ~ +30
	Emotion string `json:"emotion"` // happy / neutral / sad / angry
	Reason string `json:"reason"`  // 变化原因(中文)
}

// InterjectResult 插话判断结果
type InterjectResult struct {
	Should bool   `json:"should"`  // 是否值得主动插话
	Mode   string `json:"mode"`    // chat=正常搭话 / care=情感关怀 / silent=静默
	Reason string `json:"reason"`  // 简短中文原因
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

// CountAndMaybeProactive 群消息计数, 每N条到达一次插话检查点
// 返回 true 表示到达检查点(调用方应基于当前已刷新的群上下文判断是否插话)
func (m *MoodManager) CountAndMaybeProactive(groupID int64) bool {
	m.mu.Lock()
	m.msgCount[groupID]++
	count := m.msgCount[groupID]
	if count >= m.interval {
		m.msgCount[groupID] = 0
		m.mu.Unlock()
		return true
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
	return fmt.Sprintf("你是一个QQ群里的智能助手, 现在你在群里, 正在和大家聊天。%s%s\n"+
		"请你像真人一样自然地说一句话参与群聊: 可以对刚聊到的话题接话、吐槽、提问或分享一个小想法, 也可以顺着话题延伸。\n"+
		"要求: 语言要自然、像朋友聊天, 不要官方、不要客气、不要刻意卖萌; 别说空话套话; 一句话即可(不要超过两句话); 不要@别人; 不要加任何前缀。\n"+
		"最重要: 先看看上面别人和自己刚才说过什么, 接住最新的话题, 不要重复自己或别人说过的话, 不要说和当前话题无关的旧话题。", emotionTxt, reason), nil
}

// CarePrompt 情感关怀提示: 用户情绪低落时主动安慰
func (m *MoodManager) CarePrompt() (string, error) {
	st, err := m.store.Load()
	if err != nil {
		return "", err
	}
	reason := ""
	if st.Reason != "" {
		reason = " 当前心情原因: " + st.Reason
	}
	return fmt.Sprintf("你是一个QQ群里的智能助手, 你察觉到群里某位用户情绪低落或沮丧。%s\n"+
		"请你像真正的朋友一样, 用一句真诚、温暖、不肉麻的话主动安慰TA, 表达关心和理解。\n"+
		"要求: 语气自然、像朋友聊天, 不要说教、不喊口号、不复制鸡汤; 一两句话即可; 不要@别人; 不要加任何前缀。", reason), nil
}

	var _ = context.Background

// recordGroupMsg 记录最近群消息到环形缓冲
func (s *Service) recordGroupMsg(groupID int64, role string, sender int64, content string) {
	content = strings.TrimSpace(content)
	if content == "" || strings.HasPrefix(content, "/") || strings.Contains(content, "[CQ:") {
		return
	}
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recentMsgs == nil {
		s.recentMsgs = map[int64][]recentMsg{}
	}
	buf := s.recentMsgs[groupID]
	// 避免连续重复(同角色同内容紧挨着)导致循环刷屏
	if n := len(buf); n > 0 {
		last := buf[n-1]
		if last.role == role && last.content == content {
			return
		}
	}
	buf = append(buf, recentMsg{role: role, sender: sender, content: content, at: time.Now()})
	if len(buf) > recentMsgLimit {
		buf = buf[len(buf)-recentMsgLimit:]
	}
	s.recentMsgs[groupID] = buf
}

// recentGroupContext 组装最近群消息为 LLM 消息列表(最新一条在最前标注, 整体按时间正序)
func (s *Service) recentGroupContext(groupID int64, max int) []Message {
	s.recentMu.Lock()
	buf := s.recentMsgs[groupID]
	s.recentMu.Unlock()
	if len(buf) == 0 {
		return nil
	}
	if max <= 0 || max > len(buf) {
		max = len(buf)
	}
	buf = buf[len(buf)-max:]
	out := make([]Message, 0, len(buf)+1)
	parts := make([]string, 0, len(buf))
	for _, m := range buf {
		who := "用户"
		if m.role == "assistant" {
			who = "你(罐头喵)"
		} else if m.sender != 0 {
			who = fmt.Sprintf("用户%d", m.sender)
		}
		parts = append(parts, fmt.Sprintf("%s: %s", who, m.content))
	}
	out = append(out, NewTextMessage("user", "(下面是群里最近的对话记录, 按时间先后排列:)\n"+strings.Join(parts, "\n")))
	return out
}

// maybeProactiveInterject 对单条群消息做 LLM 插话判断, 命中则搭话(情感关怀优先)
func (s *Service) maybeProactiveInterject(ctx context.Context, groupID int64, text string) {
	if s.moodMgr == nil {
		return
	}
	moodTxt, err := s.moodMgr.ProactivePrompt()
	if err != nil {
		moodTxt = ""
	}
	// 从环形缓冲取最近群消息(已达检查点, 取最近 15 条完整上下文)作为判断上下文
	var recent []Message
	if ctxMsgs := s.recentGroupContext(groupID, 15); len(ctxMsgs) > 0 {
		recent = ctxMsgs
	}
	res, err := s.moodMgr.JudgeInterject(ctx, text, moodTxt, recent)
	if err != nil {
		log.Printf("[mood] 插话判断失败: %v", err)
		return
	}
	if !res.Should {
		log.Printf("[mood] 本条消息不值得插话 (mode=%s): %s", res.Mode, res.Reason)
		return
	}
	// 先占冷却位再发送, 避免并发/快速连续触发刷屏
	s.moodMgr.MarkInterjected(groupID, res.Mode)
	s.proactiveReply(ctx, groupID, res.Mode)
	log.Printf("[mood] 主动插话 (mode=%s): %s", res.Mode, res.Reason)
}

// proactiveReply 主动回复: 带最近群聊上下文(环形缓冲), 用主模型生成一句搭话发给群
func (s *Service) proactiveReply(ctx context.Context, groupID int64, mode string) {
	if s.moodMgr == nil {
		return
	}
	var prompt string
	var err error
	if mode == "care" {
		prompt, err = s.moodMgr.CarePrompt()
	} else {
		prompt, err = s.moodMgr.ProactivePrompt()
	}
	if err != nil {
		return
	}
	msgs := []Message{{Role: "system", Content: prompt}}
	// 从环形缓冲取最近群消息(实时, 含自己刚才说过的话), 让搭话贴合当前话题且不自说自话重复
	if ctxMsgs := s.recentGroupContext(groupID, 12); len(ctxMsgs) > 0 {
		msgs = append(msgs, ctxMsgs...)
		if mode == "care" {
			msgs = append(msgs, Message{Role: "user", Content: "请结合上面最近的对话, 用一句真诚温暖的话安慰那位情绪低落的用户, 别讲大道理, 别@任何人。"})
		} else {
			msgs = append(msgs, Message{Role: "user", Content: "请结合上面最近的对话, 自然发一句话参与群聊, 说点新的、贴题的, 不要重复自己刚说过的话。"})
		}
	} else {
		if mode == "care" {
			msgs = append(msgs, Message{Role: "user", Content: "群里暂时没有聊天记录, 但有位用户情绪低落, 请用一句真诚温暖的话安慰TA, 别讲大道理。"})
		} else {
			msgs = append(msgs, Message{Role: "user", Content: "群里暂时没有聊天记录, 你可以自然地起个头或随便聊两句。"})
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
		return
	}
	// 记录自己的搭话到环形缓冲 + 群会话, 避免后续不知道自己说过什么
	s.recordGroupMsg(groupID, "assistant", 0, text)
	if s.session != nil {
		if ses, serr := s.session.GetCurrent("group", groupID, 0, ""); serr == nil && ses != nil {
			s.session.Append(ses.ID, []session.Message{{Role: "assistant", Content: text, Time: time.Now().Unix()}}, s.cfg.AI.MaxHistory)
		}
	}
	log.Printf("[mood] 主动回复: %s", text)
}
