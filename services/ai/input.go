package ai

import (
	"strings"
	"unicode"

	"gobot/core"
)

// Some OneBot implementations omit raw_message but still supply text segments.
func eventUserContent(ev *core.Event) string {
	if strings.TrimSpace(ev.RawMessage) != "" {
		return ev.RawMessage
	}
	return ev.Text()
}

// Punctuation and emoji carry conversational meaning. Preserve the exact text
// with a descriptive label so gateways do not reject it as an empty request.
func describeSparseInput(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return text
		}
	}
	return "[用户发送了标点或符号消息，原文如下]\n" + text
}

func eventHasImage(ev *core.Event) bool {
	for _, seg := range ev.Message {
		if seg.Type == "image" {
			return true
		}
	}
	return false
}
