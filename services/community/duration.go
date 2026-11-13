package community

import (
	"errors"
	"strconv"
	"strings"
)

// Ban durations without a suffix remain minutes for compatibility.
func banSeconds(text string) (int, error) {
	bad := errors.New("禁言时长格式错误：可用 10、10分钟、30秒、2小时、1天；范围1秒至30天")
	text = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text), " ", ""))
	multiplier := int64(60)
	for _, u := range []struct {
		suffix  string
		seconds int64
	}{{"分钟", 60}, {"小时", 3600}, {"秒钟", 1}, {"seconds", 1}, {"minutes", 60}, {"hours", 3600}, {"days", 86400}, {"min", 60}, {"秒", 1}, {"分", 60}, {"时", 3600}, {"天", 86400}, {"s", 1}, {"m", 60}, {"h", 3600}, {"d", 86400}} {
		if strings.HasSuffix(text, u.suffix) {
			text = strings.TrimSuffix(text, u.suffix)
			multiplier = u.seconds
			break
		}
	}
	if text == "" {
		return 0, bad
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, bad
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 1 || n > 30*24*3600/multiplier {
		return 0, bad
	}
	return int(n * multiplier), nil
}
