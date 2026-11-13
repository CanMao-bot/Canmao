package ai

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Native APIs use 64-bit identifiers; float64 decoding silently rounds them.
func decodeQQJSON(data []byte, target interface{}) error {
	if !json.Valid(data) {
		return fmt.Errorf("无效的 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

//go:embed qq_actions.json
var qqCatalogJSON []byte

type qqAction struct {
	Action      string          `json:"action"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Tags        []string        `json:"tags"`
	Parameters  json.RawMessage `json:"parameters"`
	Stream      bool            `json:"stream"`
}

var qqCatalog = sync.OnceValue(func() map[string]qqAction {
	var doc struct {
		Actions []qqAction `json:"actions"`
	}
	if err := json.Unmarshal(qqCatalogJSON, &doc); err != nil {
		panic(err)
	}
	m := make(map[string]qqAction, len(doc.Actions))
	for _, a := range doc.Actions {
		m[a.Action] = a
	}
	return m
})

var qqActionPattern = regexp.MustCompile(`^[A-Za-z_.][A-Za-z0-9_.]{0,95}$`)

// NapCat aliases have exactly the same authority as the synchronous action.
func canonicalQQAction(action string) (string, error) {
	if !qqActionPattern.MatchString(action) {
		return "", fmt.Errorf("无效的 QQ 接口名")
	}
	for {
		next := strings.TrimSuffix(strings.TrimSuffix(action, "_async"), "_rate_limited")
		if next == action {
			break
		}
		action = next
	}
	if !qqActionPattern.MatchString(action) {
		return "", fmt.Errorf("无效的 QQ 接口名")
	}
	return action, nil
}

func searchQQActions(query string) []qqAction {
	words := strings.Fields(strings.ToLower(query))
	var result []qqAction
	for _, a := range qqCatalog() {
		haystack := strings.ToLower(a.Action + " " + a.Summary + " " + a.Description + " " + strings.Join(a.Tags, " "))
		match := true
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				match = false
				break
			}
		}
		if match {
			result = append(result, a)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Action < result[j].Action })
	return result
}

// jsonPointer resolves RFC 6901 paths without interpreting user input as a path
// on the local filesystem. Empty pointer selects the entire JSON value.
func qqJSONPointer(v interface{}, pointer string) (interface{}, error) {
	if pointer == "" {
		return v, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON pointer 必须以 / 开头")
	}
	for _, part := range strings.Split(pointer[1:], "/") {
		// Reject malformed escapes rather than silently selecting a different key.
		for i := 0; i < len(part); i++ {
			if part[i] == '~' {
				if i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1') {
					return nil, fmt.Errorf("无效的 JSON pointer 转义")
				}
				i++
			}
		}
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch node := v.(type) {
		case map[string]interface{}:
			var ok bool
			v, ok = node[part]
			if !ok {
				return nil, fmt.Errorf("JSON pointer 字段不存在: %s", part)
			}
		case []interface{}:
			var index int
			if _, err := fmt.Sscanf(part, "%d", &index); err != nil || fmt.Sprint(index) != part || index < 0 || index >= len(node) {
				return nil, fmt.Errorf("JSON pointer 数组下标无效")
			}
			v = node[index]
		default:
			return nil, fmt.Errorf("JSON pointer 不能继续访问标量")
		}
	}
	return v, nil
}
