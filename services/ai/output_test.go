package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gobot/core"
	"gobot/store/session"
)

func TestClientNormalizesModelTextAndPreservesToolCalls(t *testing.T) {
	const marker = "【05-06 07:08·系统时间标注】"
	for _, tc := range []struct {
		name, content, want string
		toolCall, wantError bool
	}{
		{"leaked_timestamp", `"` + marker + `[@42] 正文"`, "[@42] 正文", false, false},
		{"repeated_markers", `"` + marker + marker + `第一段\n\n第二段"`, "第一段\n\n第二段", false, false},
		{"ordinary_datetime", "\"日期 05-06 07:08，代码 `x := 1`。\"", "日期 05-06 07:08，代码 `x := 1`。", false, false},
		{"parts", `[{"type":"text","text":"` + marker + `你好"},{"type":"text","text":"，世界"}]`, "你好，世界", false, false},
		{"empty", `" "`, "", false, true},
		{"metadata_only", `"` + marker + `"`, "", false, true},
		{"null", `null`, "", false, true},
		{"tool_only", `null`, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				message := map[string]interface{}{"role": "assistant", "content": json.RawMessage(tc.content)}
				if tc.toolCall {
					message["tool_calls"] = []interface{}{map[string]interface{}{"id": "call-1", "type": "function", "function": map[string]interface{}{"name": "example", "arguments": `{"text":"` + marker + `"}`}}}
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": message}}})
			}))
			defer server.Close()
			client := NewClient(&core.AIConfig{BaseURL: server.URL, Model: "test-model", Timeout: 3})
			msg, err := client.Complete(context.Background(), []Message{NewTextMessage("user", "hello")}, nil)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "正文") {
					t.Fatal("empty visible output was not rejected", err)
				}
				return
			}
			if err != nil || msg.TextContent() != tc.want {
				t.Fatal("incorrect visible model output", msg, err)
			}
			if tc.toolCall && (len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != `{"text":"`+marker+`"}`) {
				t.Fatal("text cleanup modified structured tool arguments")
			}
		})
	}
}

func TestContextKeepsTimeMetadataOutOfAssistantExamples(t *testing.T) {
	const marker = "【05-06 07:08·系统时间标注】"
	for _, enabled := range []bool{true, false} {
		s := securityFixture(t)
		s.cfg.Features.MsgTimestamp = &enabled
		ses := &session.Session{Messages: []session.Message{
			{Role: "user", Content: "日期 05-06 07:08", Time: 1},
			{Role: "assistant", Content: marker + "旧回复", Time: 2},
			{Role: "assistant", Content: marker, Time: 3},
			{Role: "user", Content: "引用字面标记 " + marker, Time: 4},
		}}
		messages := s.buildContextMessages(ses, "")
		indexPresent, cleanAssistantPresent, literalUserPreserved := false, false, false
		for _, msg := range messages {
			text := msg.TextContent()
			if strings.HasPrefix(text, "[历史消息时间索引") {
				if msg.Role != "system" {
					t.Fatal("time metadata must have the system role")
				}
				indexPresent = true
			}
			if msg.Role == "assistant" {
				if text != "旧回复" {
					t.Fatal("assistant example still contains generated metadata", text)
				}
				cleanAssistantPresent = true
			}
			if msg.Role == "user" && text == ses.Messages[3].Content {
				literalUserPreserved = true
			}
		}
		if indexPresent != enabled || !cleanAssistantPresent || !literalUserPreserved {
			t.Fatal("time switch, assistant history, or literal user input changed incorrectly")
		}
		if ses.Messages[1].Content != marker+"旧回复" {
			t.Fatal("reading context must not mutate persistent history")
		}
	}
}

func TestCleanModelOutputIsSentAndStored(t *testing.T) {
	s := securityFixture(t)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		completeText(w, "【05-06 07:08·系统时间标注】输出正常")
	})
	s.Handle(context.Background(), s.bot, securityEvent(1, 0, "hello"))
	select {
	case reply := <-s.bot.Sender.(*securitySender).sent:
		if reply != "输出正常" {
			t.Fatal("internal metadata reached the sender", reply)
		}
	default:
		t.Fatal("no final reply")
	}
	ses, err := s.session.GetCurrent("private", 0, 1, "")
	if err != nil || len(ses.Messages) != 2 || ses.Messages[1].Content != "输出正常" {
		t.Fatal("unclean output reached stored history", err)
	}
}

func TestDuplicateLegacyOutputIsCleanedWithoutReexecuting(t *testing.T) {
	s := securityFixture(t)
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		t.Error("duplicate request must not call the model")
		completeText(w, "unexpected")
	})
	ses, err := s.session.GetCurrent("private", 0, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := s.session.BeginRun(ses.ID, "private:0:1:321", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.session.FinishRun(run, "completed", "【05-06 07:08·系统时间标注】旧回复", ""); err != nil {
		t.Fatal(err)
	}
	ev := securityEvent(1, 0, "hello")
	ev.MessageID = 321
	s.Handle(context.Background(), s.bot, ev)
	select {
	case reply := <-s.bot.Sender.(*securitySender).sent:
		if reply != "旧回复" {
			t.Fatal("legacy cached output leaked metadata", reply)
		}
	default:
		t.Fatal("missing cached reply")
	}
}
