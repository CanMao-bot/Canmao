package ai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/perm"
)

type qqTestSender struct {
	*securitySender
	mu      sync.Mutex
	actions []string
	params  map[string]interface{}
	data    json.RawMessage
	err     error
	stream  []json.RawMessage
}

func (q *qqTestSender) CallQQAPI(ctx context.Context, action string, params map[string]interface{}, stream func(json.RawMessage) error) (json.RawMessage, error) {
	q.mu.Lock()
	q.actions = append(q.actions, action)
	q.params = params
	q.mu.Unlock()
	for _, frame := range q.stream {
		if err := stream(frame); err != nil {
			return nil, err
		}
	}
	return q.data, q.err
}

func qqFixture(t *testing.T) (*Service, *qqTestSender) {
	s := securityFixture(t)
	s.cfg.Data.Dir = t.TempDir()
	s.cfg.AI.ToolResultMax = 3000
	q := &qqTestSender{securitySender: s.bot.Sender.(*securitySender), data: json.RawMessage(`{"value":"ok"}`)}
	s.bot.SetSender(q)
	return s, q
}

func qqTC(action string, params map[string]interface{}) ToolCall {
	var tc ToolCall
	tc.ID = "qq-test"
	tc.Function.Name = "qq_call_api"
	tc.Function.Arguments = qqEncode(map[string]interface{}{"action": action, "params": params})
	return tc
}

func qqAwaitApproval(t *testing.T, s *Service, q *qqTestSender) *ApprovalRequest {
	t.Helper()
	select {
	case <-q.sent:
	case <-time.After(time.Second):
		t.Fatal("missing approval")
	}
	s.approvals.mu.Lock()
	defer s.approvals.mu.Unlock()
	for _, p := range s.approvals.pendings {
		copy := *p
		return &copy
	}
	t.Fatal("no pending approval")
	return nil
}

func TestQQCatalogDiscoveryAndDescriptions(t *testing.T) {
	s, _ := qqFixture(t)
	if len(qqCatalog()) < 200 {
		t.Fatal("incomplete catalog")
	}
	for _, action := range []string{"send_group_msg", "set_group_admin", "delete_friend", "upload_file_stream", "get_qzone_public_feeds", "get_group_album_media_list"} {
		if _, ok := qqCatalog()[action]; !ok {
			t.Fatal("missing", action)
		}
	}
	if got := searchQQActions("群 禁言"); len(got) < 2 {
		t.Fatal("Chinese search failed", got)
	}
	ctx := core.WithEvent(context.Background(), securityEvent(1, 0, ""))
	var describe Tool
	for _, tool := range s.Tools() {
		if tool.Function.Name == "qq_describe_action" {
			describe = tool
		}
	}
	for action := range qqCatalog() {
		result, err := describe.Callback(ctx, map[string]interface{}{"action": action})
		if err != nil || !json.Valid([]byte(result)) || len([]rune(result)) > 3000 {
			t.Fatalf("invalid/big description for %s (%d): %v", action, len([]rune(result)), err)
		}
	}
	result, err := describe.Callback(ctx, map[string]interface{}{"action": "send_group_msg", "schema_pointer": "/properties/message", "limit": float64(100)})
	if err != nil || !strings.Contains(result, "next_offset") {
		t.Fatal(result, err)
	}
}

func TestQQApprovalFloorAliasesAndIsolation(t *testing.T) {
	s, q := qqFixture(t)
	ev := securityEvent(1, 0, "")
	ctx := core.WithEvent(context.Background(), ev)
	if err := s.allow.Grant("private", 0, 1, "qq_call_api", 1, int(RiskCritical)); err != nil {
		t.Fatal(err)
	}
	params := map[string]interface{}{"group_id": "7", "user_id": "3", "duration": float64(60)}
	done := make(chan string, 1)
	go func() { done <- s.executeTool(ctx, qqTC("set_group_ban_async_rate_limited", params)) }()
	req := qqAwaitApproval(t, s, q)
	if req.Risk != RiskHigh || req.ToolName != "qq_call_api/set_group_ban" {
		t.Fatal(req)
	}
	q.mu.Lock()
	calls := len(q.actions)
	q.mu.Unlock()
	if calls != 0 {
		t.Fatal("called before approval")
	}
	s.approvals.Resolve(securityEvent(1, 0, "以后都允许 "+req.ID))
	result := <-done
	if !strings.Contains(result, `"success":true`) {
		t.Fatal(result)
	}
	if q.actions[0] != "set_group_ban" || int64From(q.params["duration"]) != 60 {
		t.Fatal(q.actions, q.params)
	}
	if !s.approvals.IsAllowedFor(ev, "qq_call_api/set_group_ban", RiskHigh) {
		t.Fatal("action grant missing")
	}
	if s.approvals.IsAllowedFor(ev, "qq_call_api/set_group_kick", RiskHigh) {
		t.Fatal("grant escaped action scope")
	}
	for _, action := range []string{"get_cookies_async", "send_packet", "get_future_secret", ".handle_quick_operation", "set_group_admin", "set_restart"} {
		if riskForQQAction(action) != RiskCritical {
			t.Fatal("critical action downgraded", action)
		}
	}
	for _, action := range []string{"set_group_ban", "set_group_kick_members", "set_group_card", "set_group_name"} {
		if riskForQQAction(action) < RiskHigh {
			t.Fatal("group action downgraded", action)
		}
	}
}

func TestQQGroupDisableCancelsApprovedOperation(t *testing.T) {
	s, q := qqFixture(t)
	if err := s.perm.SetGroupBot(7, true); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := s.perm.ConfigureGroupAI(7, 1, 1, perm.GroupAIPatch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	ev := securityEvent(1, 7, "")
	ctx := context.WithValue(core.WithEvent(context.Background(), ev), groupAIGuardKey{}, true)
	done := make(chan string, 1)
	go func() {
		done <- s.executeTool(ctx, qqTC("set_group_name", map[string]interface{}{"group_id": "7", "group_name": "test"}))
	}()
	req := qqAwaitApproval(t, s, q)
	off := false
	if err := s.perm.ConfigureGroupAI(7, 1, 1, perm.GroupAIPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	s.approvals.Resolve(securityEvent(1, 7, "同意 "+req.ID))
	if result := <-done; !strings.Contains(result, "操作已取消") {
		t.Fatal(result)
	}
	if len(q.actions) != 0 {
		t.Fatal("operation ran after group AI was disabled")
	}
}

func TestQQPreservesNativeNumbersAndTypes(t *testing.T) {
	s, q := qqFixture(t)
	q.data = json.RawMessage(`{"id":9007199254740993,"flags":[false,null]}`)
	ctx := core.WithEvent(context.Background(), securityEvent(1, 0, ""))
	tc := qqTC("get_msg", map[string]interface{}{})
	tc.Function.Arguments = `{"action":"get_msg","params":{"message_id":9007199254740993,"options":[false,null,{"text":"原文"}]}}`
	result := s.executeTool(ctx, tc)
	if q.params["message_id"] != json.Number("9007199254740993") {
		t.Fatal("64-bit ID rounded", q.params)
	}
	options := q.params["options"].([]interface{})
	if options[0] != false || options[1] != nil {
		t.Fatal("native JSON types lost", options)
	}
	var receipt map[string]interface{}
	if err := json.Unmarshal([]byte(result), &receipt); err != nil {
		t.Fatal(result)
	}
	page, err := s.readQQResult(ctx, map[string]interface{}{"result_id": receipt["result_id"], "pointer": "/id"})
	if err != nil || !strings.Contains(page, "9007199254740993") {
		t.Fatal("result pointer rounded ID", page, err)
	}
}

func TestQQNativeAuthorizationCannotBeBypassed(t *testing.T) {
	s, q := qqFixture(t)
	s.cfg.OneBot.SelfID = "9"
	for _, tc := range []struct {
		user, group int64
		action      string
		params      map[string]interface{}
	}{
		{3, 0, "get_friend_list", map[string]interface{}{}},
		{3, 0, "get_group_member_list", map[string]interface{}{"group_id": "7"}},
		{2, 0, "get_credentials", map[string]interface{}{}},
		{1, 7, "get_cookies_rate_limited", map[string]interface{}{}},
		{1, 7, "get_future_secret", map[string]interface{}{}},
		{2, 0, "upload_group_file", map[string]interface{}{"group_id": "7", "file": "../config.yaml"}},
		{2, 0, "send_group_msg", map[string]interface{}{"group_id": "7", "message": []interface{}{map[string]interface{}{"type": "image", "data": map[string]interface{}{"file": "../config.yaml"}}}}},
		{2, 0, "send_group_msg", map[string]interface{}{"group_id": "7", "message": "[CQ:image,file=secret]"}},
		{1, 0, "set_group_kick_members", map[string]interface{}{"group_id": "7", "user_id": []interface{}{"3", "2"}}},
		{1, 0, "set_group_ban", map[string]interface{}{"group_id": "7", "user_id": "1"}},
		{1, 0, "set_group_kick", map[string]interface{}{"group_id": "7", "user_id": "9"}},
	} {
		ctx := core.WithEvent(context.Background(), securityEvent(tc.user, tc.group, ""))
		result := s.executeTool(ctx, qqTC(tc.action, tc.params))
		if !strings.Contains(result, "无权执行") {
			t.Fatal(tc.action, result)
		}
	}
	if len(q.actions) != 0 || s.approvals.Debug() != 0 {
		t.Fatal("denied action executed or requested approval")
	}
	// Even replacing tool metadata cannot remove the execution boundary checks.
	fake := NewTool("qq_call_api", "fake", map[string]*ToolParam{"action": {Type: "string"}, "params": {Type: "object"}}, []string{"action", "params"}, func(context.Context, map[string]interface{}) (string, error) {
		t.Fatal("unauthorized override ran")
		return "", nil
	})
	fake.Risk = RiskLow
	s.ReplaceToolsByPrefix("qq_call_api", []Tool{fake})
	ctx := core.WithEvent(context.Background(), securityEvent(3, 0, ""))
	if result := s.executeTool(ctx, qqTC("get_friend_list", map[string]interface{}{})); !strings.Contains(result, "无权执行") {
		t.Fatal(result)
	}
}

func TestQQRechecksAuthorityAfterApproval(t *testing.T) {
	s, q := qqFixture(t)
	ctx := core.WithEvent(context.Background(), securityEvent(2, 0, ""))
	done := make(chan string, 1)
	go func() {
		done <- s.executeTool(ctx, qqTC("set_group_name", map[string]interface{}{"group_id": "7", "group_name": "test"}))
	}()
	req := qqAwaitApproval(t, s, q)
	// Change authority before waking the executor (no concurrent config access).
	s.cfg.Bot.AdminIDs = nil
	s.approvals.Resolve(securityEvent(1, 0, "同意 "+req.ID))
	if result := <-done; !strings.Contains(result, "操作已取消") {
		t.Fatal(result)
	}
	if len(q.actions) != 0 {
		t.Fatal("revoked administrator executed")
	}
}

func TestQQResultsArePagedWithoutRepeatingOperations(t *testing.T) {
	s, q := qqFixture(t)
	q.data = json.RawMessage(qqEncode(map[string]interface{}{"messages": []string{strings.Repeat("中文\"\\", 3000)}}))
	ctx := core.WithEvent(context.Background(), securityEvent(1, 0, ""))
	result := s.executeTool(ctx, qqTC("get_group_msg_history", map[string]interface{}{"group_id": "7"}))
	var first map[string]interface{}
	if json.Unmarshal([]byte(result), &first) != nil || len([]rune(result)) > 3000 {
		t.Fatal("invalid result page", result)
	}
	id := first["result_id"].(string)
	var reconstructed strings.Builder
	offset := 0
	for {
		page, err := s.readQQResult(ctx, map[string]interface{}{"result_id": id, "offset": float64(offset)})
		if err != nil || len([]rune(page)) > 3000 {
			t.Fatal("bad page", err)
		}
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(page), &doc); err != nil {
			t.Fatal(err)
		}
		reconstructed.WriteString(doc["json_fragment"].(string))
		if next, ok := doc["next_offset"].(float64); ok {
			offset = int(next)
		} else {
			break
		}
	}
	if reconstructed.String() != string(q.data) || len(q.actions) != 1 {
		t.Fatal("result lost content or operation repeated")
	}
	if _, err := s.readQQResult(core.WithEvent(context.Background(), securityEvent(2, 0, "")), map[string]interface{}{"result_id": id}); err == nil {
		t.Fatal("another requester read result")
	}
	if _, err := s.readQQResult(core.WithEvent(context.Background(), securityEvent(1, 7, "")), map[string]interface{}{"result_id": id}); err == nil {
		t.Fatal("cross-scope read")
	}
	page, err := s.readQQResult(ctx, map[string]interface{}{"result_id": id, "pointer": "/messages/0", "limit": float64(5)})
	if err != nil || !json.Valid([]byte(page)) {
		t.Fatal(page, err)
	}
	if _, err := s.readQQResult(ctx, map[string]interface{}{"result_id": "../../config.yaml"}); err == nil {
		t.Fatal("result path traversal")
	}
}

func TestQQStreamAndFailureReceipts(t *testing.T) {
	s, q := qqFixture(t)
	q.stream = []json.RawMessage{json.RawMessage(`{"type":"stream","data":"one"}`), json.RawMessage(`{"type":"stream","data":"two"}`)}
	ctx := core.WithEvent(context.Background(), securityEvent(1, 0, ""))
	result, err := s.callQQAPI(ctx, map[string]interface{}{"action": "download_file_stream", "params": map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]interface{}
	if json.Unmarshal([]byte(result), &receipt) != nil {
		t.Fatal(result)
	}
	if receipt["stream_frames"] != float64(2) {
		t.Fatal(receipt)
	}
	data, err := os.ReadFile(receipt["stream_file"].(string))
	if err != nil || strings.Count(string(data), "\n") != 2 {
		t.Fatal(string(data), err)
	}
	q.err = errors.New("NapCat rejected operation")
	if result, err := s.callQQAPI(ctx, map[string]interface{}{"action": "get_status", "params": map[string]interface{}{}}); err == nil || strings.Contains(result, `"success":true`) {
		t.Fatal(result, err)
	}
}
