package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/services/memory"
	"gobot/store/allow"
	"gobot/store/perm"
	"gobot/store/provider"
	"gobot/store/sched"
	"gobot/store/session"
)

type Service struct {
	cfg          *core.Config
	client       *Client
	perm         *perm.Store
	session      *session.Store
	allow        *allow.Store
	bot          *core.Bot
	tools        []Tool
	skillContext string
	approvals    *ApprovalManager
	asker        *Asker
	models       *ModelRegistry
	memory       *memory.Manager
	moodMgr      *MoodManager
	personaMgr   *PersonaManager
	sched        *sched.Store
	fileDir      string // 媒体文件保存目录
	groupMu      sync.Mutex
	groupNames   map[int64]cachedGroupName // 群名缓存
	groupMetas   map[int64]cachedGroupMeta // 群 meta(身份/头衔/管理名单)缓存
}

type cachedGroupName struct {
	name string
	at   time.Time
}

type cachedGroupMeta struct {
	text string
	at   time.Time
}

func New(cfg *core.Config, p *perm.Store, s *session.Store, allowStore *allow.Store, bot *core.Bot) *Service {
	svc := &Service{
		cfg:     cfg,
		client:  NewClient(&cfg.AI),
		perm:    p,
		session: s,
		allow:   allowStore,
		bot:     bot,
		models:  NewModelRegistry(&cfg.AI),
	}
	svc.client.SetEndpointSource(svc.models)
	svc.approvals = NewApprovalManager(bot, 120*time.Second)
	svc.approvals.SetAllowStore(allowStore)
	svc.asker = NewAsker(bot, 300*time.Second)
	svc.registerBuiltinTools()
	return svc
}

// SetMoodManager 注入心情管理器
func (s *Service) SetMoodManager(m *MoodManager) {
	s.moodMgr = m
	// 注入 AI 情感判断
	m.SetLLM(func(ctx context.Context, text string) (EmotionResult, error) {
		prompt := "你是一个情感分析器。分析下面这条用户在QQ里发的消息, 判断它对AI说话者的情感倾向。\n\n" +
			"用户消息: " + text + "\n\n" +
			"输出 JSON: {\"delta\": 整数, \"emotion\": \"happy|neutral|sad|angry\", \"reason\": \"简短中文原因\"}\n" +
			"规则: delta 范围 -30~+30。表扬/感谢/赞美→正数; 批评/辱骂→负数; 求助→小正数; 普通闲聊→0。\n" +
			"注意识别讽刺: 表面夸奖实则贬低→负数。只输出 JSON。"
		cctx := WithModelOverride(ctx, s.models.ModelForLevel(levelMaster))
		msg, err := s.client.Complete(cctx, []Message{
			{Role: "system", Content: "你是情感分析器, 只输出 JSON。"},
			{Role: "user", Content: prompt},
		}, nil)
		if err != nil {
			return EmotionResult{}, err
		}
		var res EmotionResult
		if err := json.Unmarshal([]byte(msg.TextContent()), &res); err != nil {
			return EmotionResult{}, err
		}
		return res, nil
	})
}

// SetPersonaManager 注入人设管理器
func (s *Service) SetPersonaManager(m *PersonaManager) {
	s.personaMgr = m
	// 注入 LLM 调用
	m.SetLLM(func(ctx context.Context, prompt string) (string, error) {
		cctx := WithModelOverride(ctx, s.models.ModelForLevel(levelMaster))
		msg, err := s.client.Complete(cctx, []Message{
			{Role: "system", Content: "你是人设优化器, 只输出要求的 JSON。"},
			{Role: "user", Content: prompt},
		}, nil)
		if err != nil {
			return "", err
		}
		return msg.TextContent(), nil
	})
}

// SetSchedStore 注入定时任务存储
func (s *Service) SetSchedStore(st *sched.Store) {
	s.sched = st
	if s.cfg.Sched.ScheduleTool {
		s.registerSchedTools()
	}
}

// SetFileDir 设置媒体文件保存目录
func (s *Service) SetFileDir(dir string) { s.fileDir = dir }

// SetMemoryManager 注入记忆管理器
func (s *Service) SetMemoryManager(m *memory.Manager) { s.memory = m }

// ModelRegistry 访问模型注册表
func (s *Service) ModelRegistry() *ModelRegistry { return s.models }

// SetProviderStore 设置 provider 持久化存储
func (s *Service) SetProviderStore(st *provider.Store) {
	s.models.SetStore(st)
}

// HasDefaultModel 当前是否已配置默认模型(可正常对话)
func (s *Service) HasDefaultModel() bool {
	if !s.models.HasProvider() {
		return false
	}
	p, err := s.models.CurrentProvider()
	if err != nil {
		return false
	}
	return p.Model != ""
}

// providerReady 是否已配置提供商(不管是否选了模型)
func (s *Service) providerReady() bool {
	if !s.models.HasProvider() {
		return false
	}
	_, err := s.models.CurrentProvider()
	return err == nil
}

func (s *Service) Name() string { return "ai-agent" }

func (s *Service) AddTool(t Tool) { s.tools = append(s.tools, t) }

func (s *Service) SetSkillContext(ctx string) { s.skillContext = ctx }

func (s *Service) Tools() []Tool { return s.tools }

func (s *Service) registerBuiltinTools() {
	s.tools = append(s.tools, NewTool("send_group_message", "向指定群发送消息",
		map[string]*ToolParam{
			"group_id": {Type: "integer", Description: "目标群号"},
			"message":  {Type: "string", Description: "要发送的内容"},
		}, []string{"group_id", "message"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			gid := int64(args["group_id"].(float64))
			text := args["message"].(string)
			err := s.bot.Sender.SendGroupMsg(gid, 0, []core.Segment{core.TextSegment(text)})
			return "已发送", err
		},
	))

	// 提问工具: 一次性输出多个问题, 用户一次性回答 (低风险, 无需审批)
	askTool := NewTool("ask_user", "向用户提问以获取完成任务所需的信息。可一次提出多个问题(每个问题包含文本和可选的选项列表), 用户会一次性回答全部问题。当信息不足时优先使用本工具, 不要猜测。",
		map[string]*ToolParam{
			"questions": {Type: "array", Description: "问题列表, 每个元素是 {\"text\": 问题文本, \"options\": 可选选项数组}"},
		}, []string{"questions"},
		s.askCallback,
	)
	askTool.Risk = RiskLow
	s.tools = append(s.tools, askTool)

	// AI 主动记忆工具: AI 可自行决定保存某件事到记忆
	if s.cfg.Mood.RememberTool {
		rememberTool := NewTool("remember", "主动记住一条重要信息(用户偏好、约定、事实等), 保存到长期记忆供以后检索。当对话中出现值得记住的用户信息时使用。",
			map[string]*ToolParam{
				"content": {Type: "string", Description: "要记住的信息内容"},
			}, []string{"content"},
			s.rememberCallback,
		)
		rememberTool.Risk = RiskLow
		s.tools = append(s.tools, rememberTool)
	}

	// 按消息ID查看消息内容(引用/历史消息)
	if core.FeatureOn(s.cfg.Features.Quote) {
		s.registerViewMessageTool()
	}
	if core.FeatureOn(s.cfg.Features.GroupMeta) {
		s.registerMemberInfoTool()
	}
}

// rememberCallback AI 主动记忆
func (s *Service) rememberCallback(ctx context.Context, args map[string]interface{}) (string, error) {
	ev, ok := ctx.Value("event").(*core.Event)
	if !ok || ev == nil {
		return "", errNoEvent
	}
	content, _ := args["content"].(string)
	if content == "" || s.memory == nil {
		return "记忆功能不可用", nil
	}
	scope, gid, uid := s.sessionScope(ev)
	if err := s.memory.Save(ctx, scope, gid, uid, content); err != nil {
		return "记忆保存失败: " + err.Error(), nil
	}
	return "✅ 已记住: " + content, nil
}

func (s *Service) askCallback(ctx context.Context, args map[string]interface{}) (string, error) {
	ev, ok := ctx.Value("event").(*core.Event)
	if !ok || ev == nil {
		return "", errNoEvent
	}
	rawQ, ok := args["questions"].([]interface{})
	if !ok || len(rawQ) == 0 {
		return "错误: questions 参数必须为非空数组", nil
	}
	qs := make([]Question, 0, len(rawQ))
	for i, rq := range rawQ {
		m, ok := rq.(map[string]interface{})
		if !ok {
			continue
		}
		q := Question{
			ID:   fmt.Sprintf("q%d", i+1),
			Text: str(m["text"]),
		}
		if opts, ok := m["options"].([]interface{}); ok {
			for _, o := range opts {
				if s, ok := o.(string); ok {
					q.Options = append(q.Options, s)
				}
			}
		}
		if q.Text == "" {
			continue
		}
		qs = append(qs, q)
	}
	if len(qs) == 0 {
		return "错误: 没有有效的问题", nil
	}
	res, err := s.asker.Ask(ctx, ev, qs)
	if err != nil {
		return "", err
	}
	if !res.Completed {
		return "提问未完成: " + res.Reason, nil
	}
	// 序列化回答供模型使用
	out := "用户回答如下:\n"
	for _, q := range qs {
		ans := res.Answers[q.ID]
		if ans == "" {
			ans = "(未回答)"
		}
		out += fmt.Sprintf("- %s: %s\n", q.Text, ans)
	}
	return out, nil
}

func str(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

type noEventError string

func (e noEventError) Error() string { return string(e) }

const errNoEvent noEventError = "无法获取事件上下文"

func (s *Service) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if ev.Type != "message" {
		return false
	}

	isMaster := bot.Cfg.Bot.MasterID == fmtID(ev.UserID)

	// 群未启用时: 全员静默(不响应任何内容, 含主人), 仅管理命令可执行
	if ev.IsGroup() && !s.perm.BotEnabled(ev.GroupID) {
		// 管理命令仍可用(主人/管理员可重新启用)
		cmd, _ := cmdOf(ev.Text())
		if isMaster || isAdmin(bot, ev.UserID) {
			switch cmd {
			case "/bot", "/ai", "/help", "/ai status":
				// 允许管理命令
			default:
				return false
			}
		} else {
			return false
		}
	}
	// 1. 处理审批回复
	if s.approvals.Resolve(ev) {
		return true
	}
	// 2. 处理提问回答
	if s.asker.Resolve(ev) {
		return true
	}

	// 内置管理命令(优先于引导, 保证 /model /provider 等可用)
	if handled, done := s.handleCommands(ctx, bot, ev, isMaster); handled {
		return done
	}

	// 未配置模型时引导主人配置(区分: 无提供商 / 有提供商未选模型)
	if !s.HasDefaultModel() {
		if isMaster {
			if s.providerReady() {
				// 有提供商但未选模型
				bot.Reply(ev, "🚀 当前提供商已配置, 但还没有选择模型。\n\n"+
					"用 /model list 查看可用模型, 然后 /model <模型ID> 设置默认模型。\n"+
					"也可 /model group <组> <模型ID> 按用户组设置。")
			} else {
				guide := "🚀 需要先配置模型提供商才能使用 AI 功能。\n\n" +
					s.models.RenderProviders() + "\n\n" +
					"例如: /provider add deepseek https://api.deepseek.com/v1 sk-你的key\n" +
					"添加后会自动获取模型列表, 再用 /model <ID> 选择默认模型。"
				bot.Reply(ev, guide)
			}
		}
		return true
	}

	// 判断是否该响应 AI
	respond := s.shouldRespond(bot, ev, isMaster)

	// 心情系统: 群消息即使不触发回复, 也参与情绪感知(轻量规则)与主动回复计数
	if ev.IsGroup() && s.moodMgr != nil && s.cfg.Mood.Enabled {
		if !respond {
			// 未触发回复的消息只做关键词级情绪感知(无 LLM 开销)
			s.moodMgr.ruleBasedDetect(ev.Text())
		}
		// 每N条群消息触发主动回复评估(私聊主动打扰不合适; 本条条会正常回复时不再搭话)
		if !respond && s.cfg.Mood.Proactive && s.perm.GroupEnabled(ev.GroupID) &&
			s.moodMgr.CountAndMaybeProactive(ev.GroupID) {
			s.proactiveReply(ctx, ev.GroupID)
		}
	}

	if !respond {
		return false
	}

	// 心情系统: 对触发回复的消息做 AI 情绪检测(群聊+私聊都生效)
	if s.moodMgr != nil && s.cfg.Mood.Enabled {
		if state := s.moodMgr.DetectAndApply(ev.Text()); state != nil {
			log.Printf("[mood] 检测到情绪: %s (值=%d, 原因=%s)", state.Emotion, state.Value, state.Reason)
		}
	}

	// 获取当前会话 (自动创建)
	scope, gid, uid := s.sessionScope(ev)
	ses, err := s.session.GetCurrent(scope, gid, uid, defaultSessionTitle(ev))
	if err != nil {
		log.Printf("[ai] 获取会话失败: %v", err)
		bot.Reply(ev, "抱歉, 会话异常: "+err.Error())
		return true
	}

	// 自动压缩
	if err := s.maybeAutoCompact(ctx, ses); err != nil {
		log.Printf("[ai] 自动压缩失败: %v", err)
	}

	// 群聊 @ 解析: [CQ:at,qq=xxx] → [@昵称(xxx)], 让模型知道谁被提及
	userContent := ev.RawMessage
	if ev.IsGroup() && core.FeatureOn(s.cfg.Features.AtParse) {
		userContent = s.resolveAtMentions(ev, userContent)
	}

	// 引用消息注入: 用户回复引用了某条消息时, 让 AI 看到被引用内容
	var quoteImages []string
	if core.FeatureOn(s.cfg.Features.Quote) {
		if replyID := ev.ReplyID(); replyID > 0 {
			if quoteText, imgs := s.fetchQuote(replyID); quoteText != "" {
				userContent = quoteText + "\n" + userContent
				quoteImages = imgs
			}
		}
	}

	// 群内合并会话时, 消息加上发送者标识让 AI 区分用户
	if ev.IsGroup() && s.cfg.AI.SessionMode == "merged" {
		userContent = fmt.Sprintf("[用户%d] %s", ev.UserID, userContent)
	}

	// 提取消息中的图片, 下载转 base64 (引用消息的图片排在主消息图片之前)
	imageDataURLs := append(quoteImages, extractImageDataURLs(ev)...)

	// 记忆检索: 将相关记忆注入上下文
	memText := ""
	if s.memory != nil {
		scope, gid, uid := s.sessionScope(ev)
		if mt, err := s.memory.Recall(ctx, scope, gid, uid, userContent, s.cfg.Memory.RecallLimit); err == nil && mt != "" {
			memText = mt
			log.Printf("[ai] 检索到记忆: %d 条", countLines(mt))
		} else if err != nil {
			log.Printf("[ai] 记忆检索失败: %v", err)
		}
	}

	// 图片转述: 用识图模型将图片描述为文字(不影响主对话模型)
	if len(imageDataURLs) > 0 && s.cfg.Memory.VisionModel != "" {
		caption := s.describeImages(ctx, imageDataURLs)
		if caption != "" {
			userContent = fmt.Sprintf("%s\n[图片内容描述]: %s", userContent, caption)
		}
	}

	// 记忆注入已移入 buildContextMessages(system prompt/摘要之后、历史消息之前)
	messages := s.buildContextMessages(ses, memText)
	// 注入当前环境信息(meta): 群名/群号/自己的QQ与群身份/群主管理员名单, 低调一行
	if ev.IsGroup() && core.FeatureOn(s.cfg.Features.EnvInject) {
		meta := ""
		if core.FeatureOn(s.cfg.Features.GroupMeta) {
			meta = s.groupMetaOf(ev.GroupID)
			if meta != "" {
				meta = " " + meta
			}
		}
		envMsg := NewTextMessage("system", fmt.Sprintf("[当前环境] 你是%s(QQ:%d), 正在QQ群「%s」(群号: %d) 中群聊。%s当前时间: %s。",
			s.bot.Cfg.Bot.Name, s.selfID(), s.groupNameOf(ev.GroupID), ev.GroupID, meta, time.Now().Format("2006-01-02 15:04")))
		messages = append([]Message{messages[0], envMsg}, messages[1:]...)
	}
	if len(imageDataURLs) > 0 {
		// 图片已转述为文字, 主模型不再接收 base64(避免 token 开销)
		messages = append(messages, NewTextMessage("user", userContent))
	} else {
		messages = append(messages, NewTextMessage("user", userContent))
	}

	// 按用户组选择模型
	lvl := s.levelOf(bot, ev, isMaster)
	model := s.models.ModelForLevel(lvl)
	ctx = WithModelOverride(ctx, model)

	reply, err := s.runAgent(ctx, messages, ev)
	if err != nil {
		log.Printf("[ai] Agent 错误: %v", err)
		bot.Reply(ev, "抱歉, 处理出错: "+err.Error())
		return true
	}

	s.session.Append(ses.ID, []session.Message{
		{Role: "user", Content: userContent, Time: time.Now().Unix()},
		{Role: "assistant", Content: reply, Time: time.Now().Unix()},
	}, s.cfg.AI.MaxHistory)

	// 记忆保存: 提取重要信息存入记忆库
	if s.memory != nil {
		s.saveMemories(ctx, ev, userContent, reply)
	}

	// 自动设置会话标题(若未设置)
	if ses.Title == "" {
		if title := genTitle(ev); title != "" {
			s.session.Rename(ses.ID, title)
		}
	}

	// 人设自我优化: 每隔N条消息反思对话
	if s.personaMgr != nil && s.cfg.Persona.SelfImprove && ev.IsGroup() {
		recent := fmt.Sprintf("用户(%d): %s\nAI: %s", ev.UserID, userContent, reply)
		s.personaMgr.MaybeSelfImprove(ctx, ev.GroupID, recent)
	}

	bot.Reply(ev, reply)
	return true
}

func defaultSessionTitle(ev *core.Event) string {
	t := strings.TrimSpace(ev.Text())
	if t == "" {
		return ""
	}
	if len([]rune(t)) > 20 {
		t = string([]rune(t)[:20]) + "..."
	}
	return t
}

func genTitle(ev *core.Event) string {
	t := strings.TrimSpace(ev.Text())
	if t == "" {
		return ""
	}
	if len([]rune(t)) > 20 {
		t = string([]rune(t)[:20]) + "..."
	}
	return t
}

func (s *Service) systemPrompt() string {
	base := s.cfg.AI.SystemPrompt
	// 人设注入
	if s.personaMgr != nil && s.cfg.Persona.Enabled {
		base = s.personaMgr.InjectPrompt(base)
	}
	// 当前心情注入
	if s.moodMgr != nil && s.cfg.Mood.Enabled {
		if st, err := s.moodMgr.Current(); err == nil {
			emotion := map[string]string{
				"happy":   "开心",
				"neutral": "平静",
				"sad":     "低落",
				"angry":   "生气",
			}[st.Emotion]
			if emotion == "" {
				emotion = "平静"
			}
			moodTxt := fmt.Sprintf("\n\n=== 你当前的心情 ===\n心情值: %d/100 (%s)", st.Value, emotion)
			if st.Reason != "" {
				moodTxt += "\n原因: " + st.Reason
			}
			moodTxt += "\n请自然地体现这份心情, 但不要过度表演。"
			base += moodTxt
		}
	}
	// 群内合并会话时提示 AI 存在多位用户
	if s.cfg.AI.SessionMode == "merged" && strings.Contains(base, "QQ 群") {
		base += "\n\n注意: 本群多人共享同一个对话, 消息格式为 [用户QQ号] 内容, 请根据上下文判断回复对象。"
	}
	// 群聊消息格式指引: @ 与引用
	base += "\n\n注意: 群聊消息中 [@昵称(QQ号)] 表示有人被提及, [引用消息] 开头的内容是用户回复时引用的消息; 你在回复中写 [@QQ号] 可 @ 对应成员、[@全体成员] 可 @ 全体成员, 仅在确有必要时使用。"
	if s.skillContext == "" {
		return base
	}
	return base + "\n\n=== 可用技能 (Skills) ===\n" + s.skillContext + "\n当用户请求与某个技能相关时, 优先使用对应技能。"
}

func (s *Service) runAgent(ctx context.Context, messages []Message, ev *core.Event) (string, error) {
	// 注入事件到上下文, 供工具使用
	ctx = context.WithValue(ctx, "event", ev)
	cur := messages
	maxIter := s.cfg.AI.MaxIterations
	if maxIter <= 0 {
		maxIter = 8
	}
	// 上下文 token 预算: 模型上下文窗口的 70% 用于工具循环(预留回复空间)
	budget := s.models.ContextWindow() * 7 / 10
	if budget <= 0 {
		budget = 20000
	}

	toolCallsSeen := map[string]int{} // 去重: 工具名+参数哈希 -> 次数

	for i := 0; i < maxIter; i++ {
		// 上下文预算检查
		if est := estimateMessageListTokens(cur); est > budget {
			return "", fmt.Errorf("对话上下文超限(%d tokens), 请压缩会话后重试", est)
		}

		msg, err := s.client.Complete(ctx, cur, s.tools)
		if err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			return msg.TextContent(), nil
		}

		cur = append(cur, *msg)
		executed := false
		for _, tc := range msg.ToolCalls {
			// 工具调用去重: 同一工具同一参数连续调用超过3次, 中断防死循环
			key := tc.Function.Name + "|" + tc.Function.Arguments
			toolCallsSeen[key]++
			if toolCallsSeen[key] > 10 {
				cur = append(cur, Message{Role: "tool", ToolCallID: tc.ID, Content: "错误: 该工具调用重复次数过多, 请停止并直接回答用户"})
				continue
			}
			result := s.executeTool(ctx, tc)
			// 工具结果截断, 防止撑爆上下文
			result = truncateRunes(result, s.cfg.AI.ToolResultMax)
			cur = append(cur, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
			executed = true
		}
		// 若所有工具调用都被去重跳过, 说明陷入死循环
		if !executed {
			break
		}
	}
	return "", fmt.Errorf("Agent 达到最大工具轮数(%d), 已停止", maxIter)
}

// riskForTool 工具风险等级: 默认中风险(需发起人确认), 可被工具覆盖
func riskForTool(t Tool) RiskLevel {
	if t.Risk > RiskLow {
		return t.Risk
	}
	return RiskMedium
}

// truncateRunes 按字符截断文本
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("\n...(已截断, 共%d字符, 仅显示前%d)", len(r), max)
}

func (s *Service) executeTool(ctx context.Context, tc ToolCall) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		args = map[string]interface{}{}
	}
	ev, _ := ctx.Value("event").(*core.Event)

	for _, t := range s.tools {
		if t.Function.Name == tc.Function.Name {
			if t.Callback == nil {
				return "工具已注册但无回调"
			}
			// 安全审核: 按风险等级请求人工审批
			if ev != nil {
				risk := riskForTool(t)
				// 低风险直接执行
				if risk > RiskLow {
					// 已永久允许则跳过审批
					if !s.approvals.IsAllowed(ev, tc.Function.Name) {
						res, err := s.approvals.Request(ctx, ev, tc.Function.Name, args, risk)
						if err != nil {
							return "审批出错: " + err.Error()
						}
						if !res.Approved {
							return "操作被拒绝: " + res.Reason
						}
					}
				}
			}
			res, err := t.Callback(ctx, args)
			if err != nil {
				return "工具执行失败: " + err.Error()
			}
			return res
		}
	}
	return "未知工具: " + tc.Function.Name
}

func (s *Service) shouldRespond(bot *core.Bot, ev *core.Event, isMaster bool) bool {
	if ev.IsGroup() {
		// 群 AI 开关: 关闭后全员(含主人)不响应 AI 对话, 仅管理命令可用
		if !s.perm.GroupEnabled(ev.GroupID) {
			return false
		}
		ok, err := s.perm.CanUseAI(ev.GroupID, ev.UserID, isMaster)
		if err != nil || !ok {
			return false
		}
		// mention_only 模式: 群内仅 @bot 才响应
		if bot.Cfg.AI.MentionOnly {
			selfID, _ := strconv.ParseInt(bot.Cfg.OneBot.SelfID, 10, 64)
			return ev.IsMentioned(selfID)
		}
		// @bot 时必响应
		selfID, _ := strconv.ParseInt(bot.Cfg.OneBot.SelfID, 10, 64)
		if ev.IsMentioned(selfID) {
			return true
		}
		// 概率触发: 普通群消息按 reply_probability 概率响应
		return s.probabilityGate()
	}
	// 私聊: 除黑名单外均可
	role, _ := s.perm.GetUserRole(ev.UserID)
	return role != perm.RoleBanned
}

// probabilityGate 按配置概率决定是否触发
func (s *Service) probabilityGate() bool {
	p := 1.0
	if s.cfg.AI.ReplyProbability != nil {
		p = *s.cfg.AI.ReplyProbability
	}
	if p <= 0 {
		return false
	}
	if p >= 1 {
		return true
	}
	return rand.Float64() < p
}

func fmtID(id int64) string { return strings.TrimSpace(int64ToStr(id)) }

// groupNameOf 获取群名称(带 10 分钟缓存, 失败返回空串)
func (s *Service) groupNameOf(groupID int64) string {
	s.groupMu.Lock()
	if s.groupNames == nil {
		s.groupNames = map[int64]cachedGroupName{}
	}
	if c, ok := s.groupNames[groupID]; ok && time.Since(c.at) < 10*time.Minute {
		name := c.name
		s.groupMu.Unlock()
		return name
	}
	s.groupMu.Unlock()

	name := s.bot.GetGroupName(groupID)

	s.groupMu.Lock()
	s.groupNames[groupID] = cachedGroupName{name: name, at: time.Now()}
	s.groupMu.Unlock()
	return name
}

// extractImageDataURLs 从消息段中提取图片, 下载并转为 base64 data URI (供多模态模型使用)
func extractImageDataURLs(ev *core.Event) []string {
	var urls []string
	for _, seg := range ev.Message {
		if seg.Type != "image" {
			continue
		}
		u, _ := seg.Data["url"].(string)
		if u == "" {
			continue
		}
		// 下载并转 base64
		if dataURI, err := downloadImageAsDataURI(u); err == nil {
			urls = append(urls, dataURI)
		} else {
			log.Printf("[ai] 图片下载失败: %v", err)
		}
	}
	return urls
}

// downloadImageAsDataURI 下载图片并转 base64 data URI
func downloadImageAsDataURI(rawURL string) (string, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// 从 Content-Type 推断 MIME
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func int64ToStr(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
