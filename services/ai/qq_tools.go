package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gobot/core"
)

func qqEncode(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func qqPageInt(args map[string]interface{}, key string, fallback, maximum int) int {
	v, exists := args[key]
	if !exists {
		return fallback
	}
	n := int64From(v)
	if n < 0 {
		return 0
	}
	if n > int64(maximum) {
		return maximum
	}
	return int(n)
}

// Leave room for the JSON envelope and escaping under the agent's result cap.
func (s *Service) qqResultBudget() int {
	cap := s.cfg.AI.ToolResultMax
	if cap <= 0 || cap > 3000 {
		cap = 3000
	}
	return cap
}

func (s *Service) qqPageSize() int {
	n := (s.qqResultBudget() - 650) / 2
	if n < 1 {
		n = 1
	}
	return n
}

func (s *Service) qqTextPage(text string, args map[string]interface{}) map[string]interface{} {
	runes := []rune(text)
	start := qqPageInt(args, "offset", 0, len(runes))
	limit := qqPageInt(args, "limit", s.qqPageSize(), s.qqPageSize())
	if limit < 1 {
		limit = s.qqPageSize()
	}
	end := start + limit
	if end > len(runes) {
		end = len(runes)
	}
	page := map[string]interface{}{"json_fragment": string(runes[start:end]), "offset": start, "total_chars": len(runes)}
	if end < len(runes) {
		page["next_offset"] = end
	}
	return page
}

func (s *Service) registerQQTools() {
	list := NewTool("qq_list_actions", "搜索 NapCat 的全部原生 QQ 能力（消息、好友、群管理、文件、账号资料、相册、空间等）。query 可用中文或接口名，空值列出全部；offset/limit 分页。先查询接口，再查看参数并调用。",
		map[string]*ToolParam{"query": {Type: "string"}, "offset": {Type: "integer"}, "limit": {Type: "integer"}}, nil,
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			query, _ := args["query"].(string)
			found := searchQQActions(query)
			start := qqPageInt(args, "offset", 0, len(found))
			limit := qqPageInt(args, "limit", 8, 12)
			if limit < 1 {
				limit = 1
			}
			var rows []interface{}
			end := start
			for end < len(found) && end-start < limit {
				a := found[end]
				row := map[string]interface{}{"action": a.Action, "summary": truncateRunes(a.Summary, 60), "risk": riskForQQAction(a.Action).String()}
				if len([]rune(qqEncode(rows)))+len([]rune(qqEncode(row))) > s.qqResultBudget()-650 && len(rows) > 0 {
					break
				}
				rows = append(rows, row)
				end++
			}
			out := map[string]interface{}{"actions": rows, "total": len(found), "offset": start}
			if end < len(found) {
				out["next_offset"] = end
			}
			return qqEncode(out), nil
		})
	list.Risk = RiskLow
	describe := NewTool("qq_describe_action", "查看 QQ 接口参数。默认列出参数名/必填字段；schema_pointer 选择完整 JSON Schema 的 RFC6901 路径（如 /properties/message/anyOf/0），offset/limit 分页返回 json_fragment。目录仅供参考，实际参数以 NapCat 校验为准。",
		map[string]*ToolParam{"action": {Type: "string"}, "schema_pointer": {Type: "string"}, "offset": {Type: "integer"}, "limit": {Type: "integer"}}, []string{"action"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			action, err := canonicalQQAction(args["action"].(string))
			if err != nil {
				return "", err
			}
			a, ok := qqCatalog()[action]
			if !ok {
				return "", fmt.Errorf("目录未收录此接口；主人可在私聊按极危审批调用，NapCat 会验证支持情况")
			}
			var schema map[string]interface{}
			if err := json.Unmarshal(a.Parameters, &schema); err != nil {
				return "", err
			}
			if pointer, supplied := args["schema_pointer"].(string); supplied {
				selected, err := qqJSONPointer(schema, pointer)
				if err != nil {
					return "", err
				}
				out := s.qqTextPage(qqEncode(selected), args)
				out["action"] = action
				out["schema_pointer"] = pointer
				return qqEncode(out), nil
			}
			props, _ := schema["properties"].(map[string]interface{})
			keys := make([]string, 0, len(props))
			for name := range props {
				keys = append(keys, name)
			}
			sort.Strings(keys)
			start := qqPageInt(args, "offset", 0, len(keys))
			end := start + qqPageInt(args, "limit", 6, 8)
			if end > len(keys) {
				end = len(keys)
			}
			rows := []interface{}{}
			for _, name := range keys[start:end] {
				field, _ := props[name].(map[string]interface{})
				row := map[string]interface{}{"name": name, "schema_pointer": "/properties/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")}
				for _, key := range []string{"type", "description", "default"} {
					if val, ok := field[key]; ok {
						if text, ok := val.(string); ok {
							val = truncateRunes(text, 90)
						}
						row[key] = val
					}
				}
				rows = append(rows, row)
			}
			out := map[string]interface{}{"action": action, "summary": truncateRunes(a.Summary, 90), "risk": riskForQQAction(action).String(), "stream": a.Stream, "required": schema["required"], "fields": rows, "total_fields": len(keys)}
			if end < len(keys) {
				out["next_offset"] = end
			}
			return qqEncode(out), nil
		})
	describe.Risk = RiskLow
	call := NewTool("qq_call_api", "调用任意 NapCat/OneBot QQ 接口。仅主人/配置管理员；参数放入 params 原生 JSON 对象，不要转字符串。先 qq_describe_action。发送及修改需按具体接口审批，账号/凭据/原生文件及未知接口限主人，凭据限私聊。别名统一同步执行，以最终回执判断成功。结果带 result_id，可用 qq_read_result 分页读取；流式数据保存为本机 NDJSON 文件。错误或结果不确定时不要自动重试。",
		map[string]*ToolParam{"action": {Type: "string"}, "params": {Type: "object"}}, []string{"action", "params"}, s.callQQAPI)
	call.Risk = RiskLow // The execution boundary computes a hard floor per action.
	call.Authorize = s.authorizeQQCall
	call.Timeout = 2 * time.Minute
	read := NewTool("qq_read_result", "读取已执行 QQ 接口的完整结果，绝不重复执行操作。仅原发起人、原会话。result_id 来自 qq_call_api；pointer 选 RFC6901 路径（如 /messages/0），offset/limit 按字符分页返回 json_fragment。",
		map[string]*ToolParam{"result_id": {Type: "string"}, "pointer": {Type: "string"}, "offset": {Type: "integer"}, "limit": {Type: "integer"}}, []string{"result_id"}, s.readQQResult)
	read.Risk = RiskLow
	s.tools = append(s.tools, list, describe, call, read)
}

type qqStoredResult struct {
	UserID  int64           `json:"user_id"`
	GroupID int64           `json:"group_id"`
	Private bool            `json:"private"`
	Action  string          `json:"action"`
	Created time.Time       `json:"created"`
	Data    json.RawMessage `json:"data"`
}

func (s *Service) qqResultDir() string {
	root := s.cfg.Data.Dir
	if root == "" {
		root = "data"
	}
	return filepath.Join(root, "qq-results")
}

// Bound runtime storage even when queries return large histories or streams.
// Only our random result names are eligible; never traverse links/directories.
func (s *Service) cleanQQResults(now time.Time) {
	s.qqResultMu.Lock()
	defer s.qqResultMu.Unlock()
	if !s.qqCleanupAt.IsZero() && now.Sub(s.qqCleanupAt) < time.Hour {
		return
	}
	entries, err := os.ReadDir(s.qqResultDir())
	if err != nil {
		return
	}
	s.qqCleanupAt = now
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if ext != ".json" && ext != ".ndjson" {
			continue
		}
		if !qqResultIDPattern.MatchString(strings.TrimSuffix(entry.Name(), ext)) {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && now.Sub(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(filepath.Join(s.qqResultDir(), entry.Name()))
		}
	}
}

func (s *Service) callQQAPI(ctx context.Context, args map[string]interface{}) (string, error) {
	// Keep authorization here too: callers must not bypass it via the callback.
	if err := s.authorizeQQCall(ctx, args); err != nil {
		return "", err
	}
	action, params, err := qqCallArgs(args)
	if err != nil {
		return "", err
	}
	client, ok := s.bot.Sender.(core.QQAPIClient)
	if !ok {
		return "", fmt.Errorf("当前连接不支持原生 QQ API")
	}
	if err := os.MkdirAll(s.qqResultDir(), 0700); err != nil {
		return "", err
	}
	s.cleanQQResults(time.Now())
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(nonce[:])
	var stream *os.File
	streamBytes, frames := 0, 0
	completed := false
	defer func() {
		if stream != nil {
			stream.Close()
			if !completed {
				_ = os.Remove(stream.Name())
			}
		}
	}()
	data, err := client.CallQQAPI(ctx, action, params, func(frame json.RawMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		streamBytes += len(frame) + 1
		if streamBytes > 64*1024*1024 {
			return fmt.Errorf("QQ 流超过 64 MiB，本次接收中止，结果不确定")
		}
		if stream == nil {
			var err error
			stream, err = os.OpenFile(filepath.Join(s.qqResultDir(), id+".ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
		}
		if _, err := stream.Write(append(append([]byte{}, frame...), '\n')); err != nil {
			return err
		}
		frames++
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("QQ 调用已结束但请求取消，结果不确定: %w", err)
	}
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("QQ 返回无效 JSON，结果不确定")
	}
	if len(data) > 32*1024*1024 {
		return "", fmt.Errorf("QQ 结果超过 32 MiB，未能保存；操作可能已完成，不要自动重试")
	}
	ev := core.EventFrom(ctx)
	record := qqStoredResult{UserID: ev.UserID, GroupID: ev.GroupID, Private: ev.IsPrivate(), Action: action, Created: time.Now(), Data: data}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(s.qqResultDir(), id+".json"), encoded, 0600); err != nil {
		return "", fmt.Errorf("QQ 操作已返回成功，但保存结果失败，不要重复执行: %w", err)
	}
	out := s.qqTextPage(string(data), nil)
	out["success"] = true
	out["action"] = action
	out["result_id"] = id
	if stream != nil {
		if err := stream.Close(); err != nil {
			return "", fmt.Errorf("QQ 已返回成功，但流文件保存失败: %w", err)
		}
		path, err := filepath.Abs(stream.Name())
		if err != nil {
			return "", err
		}
		out["stream_file"] = path
		out["stream_frames"] = frames
	}
	completed = true
	return qqEncode(out), nil
}

var qqResultIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Service) readQQResult(ctx context.Context, args map[string]interface{}) (string, error) {
	id, _ := args["result_id"].(string)
	if !qqResultIDPattern.MatchString(id) {
		return "", fmt.Errorf("无效的 result_id")
	}
	data, err := os.ReadFile(filepath.Join(s.qqResultDir(), id+".json"))
	if err != nil {
		return "", err
	}
	var record qqStoredResult
	if err := json.Unmarshal(data, &record); err != nil {
		return "", err
	}
	ev := core.EventFrom(ctx)
	if ev == nil || ev.UserID != record.UserID || ev.GroupID != record.GroupID || ev.IsPrivate() != record.Private {
		return "", errNoPermission("QQ 结果仅原发起人在原会话读取")
	}
	if s.bot.Cfg.Bot.MasterID != fmtID(ev.UserID) && !isAdmin(s.bot, ev.UserID) {
		return "", errNoPermission("QQ 接口权限已撤销")
	}
	if err := s.authorizeQQCall(ctx, map[string]interface{}{"action": record.Action, "params": map[string]interface{}{}}); err != nil {
		return "", err
	}
	if time.Since(record.Created) > 24*time.Hour {
		return "", fmt.Errorf("QQ 结果已超过 24 小时读取期限")
	}
	if qqCredentialAction(record.Action) && (!ev.IsPrivate() || s.bot.Cfg.Bot.MasterID != fmtID(ev.UserID)) {
		return "", errNoPermission("凭据结果仅主人私聊读取")
	}
	if pointer, _ := args["pointer"].(string); pointer != "" {
		var v interface{}
		if err := decodeQQJSON(record.Data, &v); err != nil {
			return "", err
		}
		selected, err := qqJSONPointer(v, pointer)
		if err != nil {
			return "", err
		}
		record.Data = json.RawMessage(qqEncode(selected))
	}
	out := s.qqTextPage(string(record.Data), args)
	out["result_id"] = id
	out["action"] = record.Action
	return qqEncode(out), nil
}
