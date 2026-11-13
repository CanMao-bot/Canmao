package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"gobot/core"
)

func TestSparseInputKeepsUserMeaningAndTextSegmentFallback(t *testing.T) {
	for _, text := range []string{"？", "...", "👍", " ？！ "} {
		s := securityFixture(t)
		requests := make(chan string, 1)
		installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Messages []Message `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			requests <- req.Messages[len(req.Messages)-1].TextContent()
			completeText(w, "ok")
		})
		ev := securityEvent(1, 0, text)
		ev.RawMessage = "" // The adapter may provide message segments only.
		if !s.Handle(context.Background(), s.bot, ev) {
			t.Fatal("未处理符号消息")
		}
		got := <-requests
		if !strings.Contains(got, "用户发送了标点或符号消息") || !strings.HasSuffix(got, text) {
			t.Fatalf("标点含义丢失: %q", got)
		}
	}
	if got := describeSparseInput("1+1?"); got != "1+1?" {
		t.Fatal(got)
	}
	if got := describeSparseInput("你好？"); got != "你好？" {
		t.Fatal(got)
	}
}

func TestEmptyInputPromptsWithoutModel(t *testing.T) {
	s := securityFixture(t)
	var calls atomic.Int32
	installTestModel(t, s, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completeText(w, "unused") })
	if !s.Handle(context.Background(), s.bot, securityEvent(1, 0, "  \n")) {
		t.Fatal("空消息未处理")
	}
	if calls.Load() != 0 {
		t.Fatal("空消息仍请求模型")
	}
	select {
	case msg := <-s.bot.Sender.(*securitySender).sent:
		if !strings.Contains(msg, "文字或图片") {
			t.Fatal(msg)
		}
	default:
		t.Fatal("缺少输入引导")
	}
}

func TestImageOnlyInputSuppliesTextAndPreservesPixels(t *testing.T) {
	s := securityFixture(t)
	const pixels = "data:image/png;base64,example"
	msg, _, err := s.prepareUserMessage(context.Background(), "", []string{pixels})
	if err != nil {
		t.Fatal(err)
	}
	parts, ok := msg.Content.([]ContentPart)
	if !ok || len(parts) != 2 || parts[0].Type != "text" || parts[0].Text == "" || parts[1].ImageURL.URL != pixels {
		t.Fatalf("图片或文字缺失: %+v", msg)
	}
	if eventUserContent(&core.Event{Message: []core.Segment{core.TextSegment("from segments")}}) != "from segments" {
		t.Fatal("没有回退文本消息段")
	}
}
