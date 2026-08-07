package ai

import "testing"

func TestTruncateRunes(t *testing.T) {
	s := "中文内容一二三四五"
	got := truncateRunes(s, 5)
	if len([]rune(got)) > 30 {
		t.Fatalf("截断后应包含提示, got %q", got)
	}
	if len([]rune(got)) == len([]rune(s)) {
		t.Fatal("长文本应被截断")
	}
	// 短文本不截断
	if truncateRunes("短", 10) != "短" {
		t.Fatal("短文本不应截断")
	}
}

func TestEstimateMessageListTokens(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "你好世界"},                    // 4 tokens
		{Role: "assistant", Content: "你好!"},                  // 2
	}
	got := estimateMessageListTokens(msgs)
	if got <= 0 {
		t.Fatalf("估算 = %d", got)
	}
	// 工具调用参数计入
	msgs = append(msgs, Message{Role: "assistant", ToolCalls: []ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "list_files", Arguments: `{"path":"/root"}`},
		},
	}})
	if estimateMessageListTokens(msgs) <= got {
		t.Fatal("工具调用应增加估算")
	}
}
