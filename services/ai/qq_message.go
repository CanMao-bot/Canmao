package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gobot/core"
)

type qqMessageTarget struct{ groupID, userID int64 }

func (t qqMessageTarget) isCurrent(ev *core.Event) bool {
	return ev != nil && ((ev.IsGroup() && t.groupID == ev.GroupID) ||
		(ev.IsPrivate() && t.groupID == 0 && t.userID == ev.UserID))
}

func qqMessageTargetFor(ev *core.Event, args map[string]interface{}) (qqMessageTarget, error) {
	if ev == nil || ev.UserID <= 0 {
		return qqMessageTarget{}, errNoEvent
	}
	gid, uid := int64From(args["group_id"]), int64From(args["user_id"])
	_, hasGroup := args["group_id"]
	_, hasUser := args["user_id"]
	if (hasGroup && hasUser) || (hasGroup && gid <= 0) || (hasUser && uid <= 0) {
		return qqMessageTarget{}, fmt.Errorf("group_id 与 user_id 只能提供一个有效正整数，均省略则发送到当前会话")
	}
	if !hasGroup && !hasUser {
		uid = ev.UserID
		if ev.IsGroup() {
			gid = ev.GroupID
			uid = 0
		}
	}
	return qqMessageTarget{gid, uid}, nil
}

func (s *Service) authorizeQQMessage(ctx context.Context, args map[string]interface{}) error {
	ev := core.EventFrom(ctx)
	target, err := qqMessageTargetFor(ev, args)
	if err != nil {
		return err
	}
	if !target.isCurrent(ev) && fmtID(ev.UserID) != s.cfg.Bot.MasterID && !isAdmin(s.bot, ev.UserID) {
		return errNoPermission("仅主人或配置管理员可向其他会话发消息")
	}
	// A notification is not an alternate way to re-enable a disabled group's AI.
	if target.groupID > 0 && !s.groupAIEnabled(target.groupID) {
		return fmt.Errorf("目标群 AI 未获主人开启或已关闭")
	}
	return nil
}

// Resident definitions cannot be shadowed or removed by request filtering or
// plugin replacement. The same definitions are used for inference and execution.
func (s *Service) withResidentTools(tools []Tool) []Tool {
	result := append([]Tool(nil), s.residentTools...)
	for _, t := range tools {
		resident := false
		for _, r := range s.residentTools {
			if t.Function.Name == r.Function.Name {
				resident = true
				break
			}
		}
		if !resident {
			result = append(result, t)
		}
	}
	return result
}

func (s *Service) newQQMessageTool() Tool {
	t := NewTool("qq_send_message", "常驻 QQ 输出：立即发送公开进度、阶段结果或普通消息，无需搜索接口，可在任务每轮调用。默认当前会话，无需额外审批；group_id 指定群、user_id 指定私聊，两者只能选一个，跨会话限主人/配置管理员并需审批，群须已开启 AI。耗时工具前先通知，避免重复最终答复。只发送公开文字，不发送内部思考或凭据。",
		map[string]*ToolParam{
			"message":  {Type: "string", Description: "公开消息正文，1到4000字"},
			"group_id": {Type: "integer", Description: "可选，目标群；与 user_id 互斥，省略目标为当前会话"},
			"user_id":  {Type: "integer", Description: "可选，目标私聊 QQ；与 group_id 互斥"},
		}, []string{"message"}, func(ctx context.Context, args map[string]interface{}) (string, error) {
			target, err := qqMessageTargetFor(core.EventFrom(ctx), args)
			if err != nil {
				return "", err
			}
			text, _ := args["message"].(string)
			return s.sendQQMessage(ctx, target, text)
		})
	t.Risk = RiskLow // Cross-conversation medium floor is enforced in executeTool.
	t.Timeout = 15 * time.Second
	return t
}

type qqOutputKey struct{}
type qqOutputState struct {
	mu   sync.Mutex
	sent map[qqMessageTarget]map[string]bool
}

func qqAlreadySent(ctx context.Context, ev *core.Event, text string) bool {
	state, _ := ctx.Value(qqOutputKey{}).(*qqOutputState)
	if state == nil {
		return false
	}
	target, err := qqMessageTargetFor(ev, nil)
	if err != nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.sent[target][cleanAssistantOutput(text)]
}

func (s *Service) sendQQMessage(ctx context.Context, target qqMessageTarget, text string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkRunGroupAI(ctx); err != nil {
		return "", err
	}
	text = cleanAssistantOutput(text)
	if n := len([]rune(text)); n == 0 || n > 4000 {
		return "", fmt.Errorf("消息正文须为1到4000字")
	}
	if target.groupID > 0 && !s.groupAIEnabled(target.groupID) {
		return "", fmt.Errorf("目标群 AI 已关闭")
	}
	state, _ := ctx.Value(qqOutputKey{}).(*qqOutputState)
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.sent[target][text] {
			return `{"success":true,"duplicate":true,"note":"本轮已发送，未重复发送"}`, nil
		}
	}
	segs := []core.Segment{core.TextSegment(text)} // Plain text, never raw CQ execution.
	var id int64
	var err error
	if sender, ok := s.bot.Sender.(core.ReceiptSender); ok {
		id, err = sender.SendMessageWithReceipt(ctx, target.groupID, target.userID, segs)
		if err == nil && id == 0 {
			err = fmt.Errorf("没有取得有效消息回执")
		}
	} else if target.groupID > 0 {
		err = s.bot.Sender.SendGroupMsg(target.groupID, 0, segs)
	} else {
		err = s.bot.Sender.SendPrivateMsg(target.userID, segs)
	}
	if err != nil {
		return "", fmt.Errorf("QQ 发送失败，结果可能不确定，请勿自动重试: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("QQ 发送中断，结果可能已产生，请勿自动重试: %w", err)
	}
	if state != nil {
		if state.sent[target] == nil {
			state.sent[target] = map[string]bool{}
		}
		state.sent[target][text] = true
	}
	if target.groupID > 0 {
		s.recordGroupMsg(target.groupID, "assistant", 0, text)
	}
	result, _ := json.Marshal(map[string]interface{}{"success": true, "message_id": id})
	return string(result), nil
}

// Only public assistant content is delivered; reasoning fields and arguments are
// never read. Explicit current-chat sends take precedence to avoid double posts.
func (s *Service) publishToolProgress(ctx context.Context, msg *Message) {
	text := strings.TrimSpace(msg.TextContent())
	if text == "" {
		return
	}
	ev := core.EventFrom(ctx)
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name != "qq_send_message" && tc.Function.Name != "send_multiple_messages" {
			continue
		}
		if tc.Function.Name == "send_multiple_messages" {
			return
		}
		var args map[string]interface{}
		if json.Unmarshal([]byte(tc.Function.Arguments), &args) == nil {
			if target, err := qqMessageTargetFor(ev, args); err == nil && target.isCurrent(ev) {
				return
			}
		}
	}
	target, err := qqMessageTargetFor(ev, nil)
	if err != nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := s.sendQQMessage(sendCtx, target, text); err != nil {
		log.Printf("[ai] progress_delivery_failed")
	}
}
