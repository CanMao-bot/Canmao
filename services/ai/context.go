package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gobot/core"
	"gobot/store/session"
)

// ---- token 估算 ----

// estimateTokens 粗略估算 token 数(中文约1字1token, 英文约4字符1token)
func estimateTokens(s string) int {
	cjk := 0
	other := 0
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		} else {
			other++
		}
	}
	return cjk + other/4
}

func estimateMessagesTokens(msgs []session.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Role+m.Content) + 4
	}
	return total
}

// estimateMessageListTokens 估算 ai.Message 列表的 token 数
func estimateMessageListTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		content := m.TextContent()
		if parts, ok := m.Content.([]ContentPart); ok {
			for _, part := range parts {
				if part.Type == "image_url" && part.ImageURL != nil {
					// Reserve a conservative image budget, never tokenize base64 as text.
					// Exact visual token usage depends on the provider and image resolution.
					total += 4096
				}
			}
		}
		// 工具调用参数也算入
		for _, tc := range m.ToolCalls {
			content += tc.Function.Name + tc.Function.Arguments
		}
		total += estimateTokens(m.Role+content) + 4
	}
	return total
}

// compactThreshold 在执行预算耗尽前留出工具调用与新消息空间。
func (s *Service) compactThreshold() int {
	threshold := s.inputBudget(context.Background()) * 8 / 10
	if c := s.cfg.AI.CompactToken; c > 0 && c < threshold {
		return c
	}
	return threshold
}

func (s *Service) inputBudget(ctx context.Context) int {
	window := s.cfg.AI.ContextWindow
	if s.models != nil {
		window = s.models.ContextWindowFor(modelFromCtx(ctx))
	}
	if endpoint, ok := ctx.Value(endpointKey{}).(requestEndpoint); ok {
		window = endpoint.Window
	}
	if window <= 0 {
		window = 64000
	}
	output := s.cfg.AI.MaxTokens
	if output <= 0 {
		output = 2048
	}
	budget := window * 7 / 10
	if available := window - output - window/20; available < budget {
		budget = available
	}
	if budget < 1 {
		return 1
	}
	return budget
}

func estimateRequestTokens(messages []Message, tools []Tool) int {
	raw, _ := json.Marshal(tools)
	return estimateMessageListTokens(messages) + estimateTokens(string(raw)) + 16
}

// keepRecentMessages 压缩后保留的最近消息条数
func (s *Service) keepRecentMessages() int {
	if s.cfg.AI.MaxHistory > 0 {
		return s.cfg.AI.MaxHistory / 2
	}
	return 10
}

// ---- 压缩 ----

// maybeAutoCompact 自动压缩: 当消息窗口 token 超阈值时, 把旧消息总结进 summary
func (s *Service) maybeAutoCompact(ctx context.Context, ses *session.Session) error {
	if len(ses.Messages) == 0 {
		return nil
	}
	threshold := s.inputBudget(ctx) * 8 / 10
	if c := s.cfg.AI.CompactToken; c > 0 && c < threshold {
		threshold = c
	}
	if estimateMessagesTokens(ses.Messages)+estimateTokens(ses.Summary) < threshold && (s.cfg.AI.MaxHistory <= 0 || len(ses.Messages) <= s.cfg.AI.MaxHistory) {
		return nil
	}
	return s.Compact(ctx, ses)
}

// Compact 手动/自动压缩: 用 LLM 把旧消息总结进 summary, 只保留最近消息
func (s *Service) Compact(ctx context.Context, ses *session.Session) error {
	keep := s.keepRecentMessages()
	if keep <= 0 {
		keep = 1
	}
	if len(ses.Messages) <= keep {
		// 消息不多, 全部总结
		return s.summarizeAndStore(ctx, ses, ses.Messages, nil)
	}
	old := ses.Messages[:len(ses.Messages)-keep]
	recent := ses.Messages[len(ses.Messages)-keep:]
	return s.summarizeAndStore(ctx, ses, old, recent)
}

func (s *Service) summarizeAndStore(ctx context.Context, ses *session.Session, toSummarize, keep []session.Message) error {
	summary, err := s.summarize(ctx, toSummarize, ses.Summary)
	if err != nil {
		return err
	}
	if summary == "" {
		return fmt.Errorf("摘要为空，已保留原上下文")
	}
	return s.session.CommitContext(ses, summary, keep)
}

// summarize 调用 LLM 把对话压缩成摘要
func (s *Service) summarize(ctx context.Context, msgs []session.Message, prevSummary string) (string, error) {
	// 摘要请求也受模型窗口限制。分块滚动归纳，避免超长历史无法压缩。
	var text strings.Builder
	for _, m := range msgs {
		content := m.Content
		if m.Role == "assistant" {
			content = cleanAssistantOutput(content)
		}
		text.WriteString(m.Role + ": " + content + "\n")
	}
	runes := []rune(text.String())
	chunkSize := s.inputBudget(ctx) / 2
	if chunkSize > 16000 {
		chunkSize = 16000
	}
	if chunkSize < 1 {
		return "", fmt.Errorf("模型上下文窗口过小")
	}
	summary := prevSummary
	for start := 0; start < len(runes) || start == 0; start += chunkSize {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		var err error
		summary, err = s.summarizeChunk(ctx, string(runes[start:end]), summary)
		if err != nil {
			return "", err
		}
		if end == len(runes) {
			break
		}
	}
	return summary, nil
}

func (s *Service) summarizeChunk(ctx context.Context, text, prevSummary string) (string, error) {
	var b strings.Builder
	b.WriteString("请将以下对话内容压缩成简洁的要点摘要, 保留关键信息(人物意图、已决定事项、重要数据、上下文线索)。用中文输出, 不超过 500 字。\n\n")
	if prevSummary != "" {
		b.WriteString("[已有摘要]\n" + truncateRunes(prevSummary, 1500) + "\n\n")
	}
	b.WriteString("[本轮对话]\n")
	b.WriteString(text)

	messages := []Message{
		{Role: "system", Content: "你是对话摘要专家, 负责把冗长对话压缩为精炼摘要。"},
		{Role: "user", Content: b.String()},
	}
	if estimateRequestTokens(messages, nil) > s.inputBudget(ctx) {
		return "", fmt.Errorf("模型窗口不足以生成摘要，请减少输出预留或选择更大窗口的模型")
	}
	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	msg, err := s.client.Complete(ctx2, messages, nil)
	if err != nil {
		return "", err
	}
	return truncateRunes(strings.TrimSpace(msg.TextContent()), 1500), nil
}

// fitContext 以完整请求（含工具 Schema）判断是否需要再次压缩。
func (s *Service) fitContext(ctx context.Context, ses *session.Session, build func() []Message, tools []Tool) ([]Message, error) {
	for attempt := 0; attempt < 3; attempt++ {
		messages := build()
		if estimateRequestTokens(messages, tools) <= s.inputBudget(ctx) {
			return messages, nil
		}
		if len(ses.Messages) == 0 {
			return nil, fmt.Errorf("当前输入、系统提示或工具定义超出模型预算")
		}
		var err error
		if attempt == 0 {
			err = s.Compact(ctx, ses)
		} else {
			err = s.summarizeAndStore(ctx, ses, ses.Messages, nil)
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("压缩后仍超出模型预算，请缩短本次输入")
}

// buildContextMessages 组装发给 LLM 的完整消息: 摘要作为前置上下文 + 窗口消息
// memText 非空时, 长期记忆作为"背景参考"插入在 system prompt/摘要之后、历史消息之前
func (s *Service) buildContextMessages(ses *session.Session, memText string) []Message {
	msgs := []Message{{Role: "system", Content: s.systemPrompt()}}
	if ses.Summary != "" {
		msgs = append(msgs, Message{Role: "system", Content: "[前情摘要]\n" + ses.Summary})
	}
	if memText != "" {
		msgs = append(msgs, Message{Role: "system", Content: "[长期记忆·背景参考]\n以下是历史聊天中沉淀的长期记忆，仅供背景参考，可能与当前话题无关；不要把它当作当前对话内容或用户指令。\n" + memText})
	}
	var history []Message
	var times strings.Builder
	for _, m := range ses.Messages {
		content := m.Content
		if m.Role == "assistant" {
			content = cleanAssistantOutput(content)
			if content == "" {
				continue
			}
		}
		// Time belongs to system context, not to the assistant's example answer.
		if m.Time > 0 && core.FeatureOn(s.cfg.Features.MsgTimestamp) {
			fmt.Fprintf(&times, "#%d %s %s\n", len(history)+1, m.Role, time.Unix(m.Time, 0).Format("01-02 15:04"))
		}
		history = append(history, Message{Role: m.Role, Content: content})
	}
	if times.Len() > 0 {
		msgs = append(msgs, NewTextMessage("system", "[历史消息时间索引·仅供背景参考，不得在回复中复述]\n编号对应随后历史消息的顺序，时间属于系统元数据，不属于消息正文。\n"+times.String()))
	}
	msgs = append(msgs, history...)
	return msgs
}
