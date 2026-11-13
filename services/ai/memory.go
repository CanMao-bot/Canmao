package ai

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gobot/core"
	memoryservice "gobot/services/memory"
)

// Conversation sharing never removes the identity of the person who supplied
// a fact. Private memories are never silently carried into a group.
func memoryScope(ev *core.Event) (string, int64, int64) {
	if ev.IsGroup() {
		return "group", ev.GroupID, ev.UserID
	}
	return "private", 0, ev.UserID
}

type memoryFact struct{ Key, Content string }

var memorySentenceSplit = regexp.MustCompile(`[，,。！!；;\n]+`)

func extractMemoryFacts(text string) []memoryFact {
	text = strings.TrimSpace(stripCQ(text))
	if strings.ContainsAny(text, "?？\"“”「」") || strings.HasPrefix(text, "/") {
		return nil
	}
	for _, marker := range []string{"如果", "假如", "假设", "例如", "比如", "引用", "转述", "他说", "她说", "你说", "跳过审批", "最高权限"} {
		if strings.Contains(text, marker) {
			return nil
		}
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "请记住"), "记住")
	var out []memoryFact
	seen := map[string]bool{}
	for _, sentence := range memorySentenceSplit.Split(text, -1) {
		sentence = strings.TrimSpace(sentence)
		for _, pattern := range []struct{ prefix, key string }{{"我的名字是", "profile:name"}, {"我叫", "profile:name"}, {"我目前住在", "profile:residence"}, {"我住在", "profile:residence"}, {"我不喜欢", "preference:"}, {"我讨厌", "preference:"}, {"我喜欢", "preference:"}, {"我爱", "preference:"}, {"我想学", "goal:study:"}} {
			if !strings.HasPrefix(sentence, pattern.prefix) {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(sentence, pattern.prefix))
			if value == "" || len([]rune(value)) > 60 || memoryservice.ValidContent(sentence) != nil {
				break
			}
			key := pattern.key
			if strings.HasSuffix(key, ":") {
				key += strings.ToLower(value)
			}
			if !seen[key] {
				out = append(out, memoryFact{key, sentence})
				seen[key] = true
			}
			break
		}
		if len(out) >= 4 {
			break
		}
	}
	return out
}

func (s *Service) registerMemoryTools() {
	list := NewTool("list_memories", "查看当前发言者在本会话的长期记忆（最多20条），用于核对、更新或遗忘；不会读取别人的私聊或其他群。", nil, nil, func(ctx context.Context, args map[string]interface{}) (string, error) {
		if s.memory == nil {
			return "", fmt.Errorf("记忆功能尚未启用")
		}
		ev := core.EventFrom(ctx)
		if ev == nil {
			return "", errNoEvent
		}
		scope, gid, uid := memoryScope(ev)
		entries, err := s.memory.List(ctx, scope, gid, uid, 20)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, e := range entries {
			content := e.Content
			if memoryservice.ValidContent(content) != nil {
				content = "[敏感或无效旧内容已隐藏，可按 ID 遗忘]"
			}
			line := fmt.Sprintf("%d [%s] %s\n", e.ID, e.FactKey, content)
			if len([]rune(b.String()+line)) > 2200 {
				break
			}
			b.WriteString(line)
		}
		if b.Len() == 0 {
			return "当前会话没有该用户的长期记忆", nil
		}
		return b.String(), nil
	})
	list.Risk = RiskLow
	forget := NewTool("forget_memory", "用户明确要求遗忘时，删除当前用户在当前会话的一条长期记忆。先 list_memories 核对 ID；不能删除他人的记忆。", map[string]*ToolParam{"id": {Type: "integer", Description: "list_memories 返回的记忆 ID"}}, []string{"id"}, func(ctx context.Context, args map[string]interface{}) (string, error) {
		if s.memory == nil {
			return "", fmt.Errorf("记忆功能尚未启用")
		}
		ev := core.EventFrom(ctx)
		if ev == nil {
			return "", errNoEvent
		}
		scope, gid, uid := memoryScope(ev)
		ok, err := s.memory.Forget(ctx, scope, gid, uid, int64From(args["id"]))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("当前用户与会话没有这条记忆")
		}
		return "已遗忘这条长期记忆；对话历史中的原消息仍按会话规则保留", nil
	})
	forget.Risk = RiskLow
	s.tools = append(s.tools, list, forget)
}
