package ai

import (
	"regexp"
	"strings"
)

// This exact reserved marker was injected into older conversation histories.
// Strip it before displaying or reusing an assistant reply; ordinary dates stay intact.
var internalTimestamp = regexp.MustCompile(`【(?:[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}|MM-DD HH:MM)·系统时间标注】`)

func cleanAssistantOutput(text string) string {
	return strings.TrimSpace(internalTimestamp.ReplaceAllString(text, ""))
}
