package ai

import (
	"context"
	"strings"
	"time"

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
		// 工具调用参数也算入
		for _, tc := range m.ToolCalls {
			content += tc.Function.Name + tc.Function.Arguments
		}
		total += estimateTokens(m.Role+content) + 4
	}
	return total
}

// compactThreshold 触发压缩的 token 阈值
// 依据当前模型的上下文窗口(动态): 保留 system prompt + 工具定义 + 新消息等空间,
// 历史消息窗口达到上下文窗口的 40% 时触发压缩
func (s *Service) compactThreshold() int {
	// 若显式配置 compact_token 则优先
	if s.cfg.AI.CompactToken > 0 {
		return s.cfg.AI.CompactToken
	}
	ctxWin := 0
	// 已配置 provider 时用模型动态上下文
	if s.models != nil && s.models.HasProvider() {
		ctxWin = s.models.ContextWindow()
	}
	if ctxWin <= 0 {
		ctxWin = s.cfg.AI.ContextWindow
	}
	if ctxWin <= 0 {
		ctxWin = 64000
	}
	// 历史消息预算: 取上下文窗口的 40%, 留有足够余量
	return ctxWin * 40 / 100
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
	if len(ses.Messages) < 8 {
		return nil
	}
	if estimateMessagesTokens(ses.Messages) < s.compactThreshold() {
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
	if summary != "" {
		if err := s.session.SetSummary(ses.ID, summary); err != nil {
			return err
		}
	}
	if keep != nil {
		if err := s.session.ReplaceMessages(ses.ID, keep); err != nil {
			return err
		}
	} else {
		if err := s.session.ReplaceMessages(ses.ID, []session.Message{}); err != nil {
			return err
		}
	}
	return nil
}

// summarize 调用 LLM 把对话压缩成摘要
func (s *Service) summarize(ctx context.Context, msgs []session.Message, prevSummary string) (string, error) {
	var b strings.Builder
	b.WriteString("请将以下对话内容压缩成简洁的要点摘要, 保留关键信息(人物意图、已决定事项、重要数据、上下文线索)。用中文输出, 不超过 500 字。\n\n")
	if prevSummary != "" {
		b.WriteString("[已有摘要]\n" + prevSummary + "\n\n")
	}
	b.WriteString("[本轮对话]\n")
	for _, m := range msgs {
		role := m.Role
		switch m.Role {
		case "user":
			role = "用户"
		case "assistant":
			role = "助手"
		case "system":
			role = "系统"
		}
		b.WriteString(role + ": " + m.Content + "\n")
	}

	messages := []Message{
		{Role: "system", Content: "你是对话摘要专家, 负责把冗长对话压缩为精炼摘要。"},
		{Role: "user", Content: b.String()},
	}
	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	msg, err := s.client.Complete(ctx2, messages, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(msg.TextContent()), nil
}

// buildContextMessages 组装发给 LLM 的完整消息: 摘要作为前置上下文 + 窗口消息
func (s *Service) buildContextMessages(ses *session.Session) []Message {
	msgs := []Message{{Role: "system", Content: s.systemPrompt()}}
	if ses.Summary != "" {
		msgs = append(msgs, Message{Role: "system", Content: "[前情摘要]\n" + ses.Summary})
	}
	for _, m := range ses.Messages {
		msgs = append(msgs, Message{Role: m.Role, Content: m.Content})
	}
	return msgs
}
