package ai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gobot/core"
)

func (s *Service) newQQForwardTool() Tool {
	t := NewTool("qq_send_forward_message", "常驻 QQ 合并转发输出：将 messages 中的多段文字作为聊天记录节点，合成一条消息发送，短内容也可用。每项1到2500字，1到50项，合计最多50000字。节点统一使用机器人身份。默认当前会话无需额外审批；group_id 或 user_id 指定其他群/私聊时限主人和配置管理员，并按目标审批，群须已开启 AI。成功后不要在最终回复重复正文；错误或回执不确定时不要自动重试。",
		map[string]*ToolParam{
			"messages": {Type: "array", Description: "按顺序排列的文字节点，每项一个字符串", Items: map[string]interface{}{"type": "string", "minLength": 1, "maxLength": 2500}},
			"group_id": {Type: "integer", Description: "可选，目标群；与 user_id 互斥，均省略为当前会话"},
			"user_id":  {Type: "integer", Description: "可选，目标私聊 QQ；与 group_id 互斥"},
		}, []string{"messages"}, s.sendQQForward)
	t.Risk = RiskLow // The executor enforces the cross-conversation risk floor.
	t.Timeout = 30 * time.Second
	return t
}

func (s *Service) sendQQForward(ctx context.Context, args map[string]interface{}) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkRunGroupAI(ctx); err != nil {
		return "", err
	}
	if err := s.authorizeQQMessage(ctx, args); err != nil {
		return "", err
	}
	target, err := qqMessageTargetFor(core.EventFrom(ctx), args)
	if err != nil {
		return "", err
	}
	raw, ok := args["messages"].([]interface{})
	if !ok || len(raw) == 0 || len(raw) > 50 {
		return "", fmt.Errorf("messages 须为1到50条文字")
	}
	parts := make([]string, 0, len(raw))
	total := 0
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return "", fmt.Errorf("messages 每项须为文字字符串")
		}
		text = cleanAssistantOutput(text)
		n := len([]rune(text))
		if n == 0 || n > 2500 {
			return "", fmt.Errorf("合并转发每个节点须为1到2500字")
		}
		total += n
		if total > 50000 {
			return "", fmt.Errorf("合并转发合计不能超过50000字")
		}
		parts = append(parts, text)
	}
	// Node identities come from configuration/the real event, never model arguments.
	selfID := s.selfID()
	if selfID <= 0 {
		selfID = core.EventFrom(ctx).SelfID
	}
	if selfID <= 0 {
		return "", fmt.Errorf("无法确定机器人自身身份，请配置 onebot.self_id")
	}
	name := strings.TrimSpace(s.cfg.Bot.Name)
	if name == "" {
		name = "罐头喵"
	}
	nodes := make([]core.ForwardNode, 0, len(parts))
	for _, text := range parts {
		nodes = append(nodes, core.ForwardNode{UserID: selfID, Uin: fmtID(selfID), Name: name, Nickname: name, Content: []core.Segment{core.TextSegment(text)}})
	}
	encoded, _ := json.Marshal(parts)
	digest := sha256.Sum256(encoded)
	state, _ := ctx.Value(qqOutputKey{}).(*qqOutputState)
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.forwards[target][digest] {
			return `{"success":true,"duplicate":true,"note":"本轮已发送相同合并转发，未重复发送"}`, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkRunGroupAI(ctx); err != nil {
		return "", err
	}
	if err := s.authorizeQQMessage(ctx, args); err != nil {
		return "", err
	}
	var id int64
	if sender, ok := s.bot.Sender.(core.ForwardReceiptSender); ok {
		id, err = sender.SendForwardWithReceipt(ctx, target.groupID, target.userID, nodes)
		if err == nil && id == 0 {
			err = fmt.Errorf("未取得有效消息回执")
		}
	} else if s.bot.Forward != nil {
		err = s.bot.Forward.SendForwardMsg(target.groupID, target.userID, nodes)
	} else {
		return "", fmt.Errorf("当前连接不支持合并转发")
	}
	if err != nil {
		return "", fmt.Errorf("合并转发发送失败，结果可能不确定，请勿自动重试: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return "", fmt.Errorf("合并转发发送中断，结果可能已产生，请勿自动重试: %w", err)
	}
	body := strings.Join(parts, "\n\n")
	if state != nil {
		if state.forwards == nil {
			state.forwards = map[qqMessageTarget]map[[32]byte]bool{}
		}
		if state.forwards[target] == nil {
			state.forwards[target] = map[[32]byte]bool{}
		}
		state.forwards[target][digest] = true
		if state.sent == nil {
			state.sent = map[qqMessageTarget]map[string]bool{}
		}
		if state.sent[target] == nil {
			state.sent[target] = map[string]bool{}
		}
		state.sent[target][body] = true
	}
	if target.groupID > 0 {
		s.recordGroupMsg(target.groupID, "assistant", 0, body)
	}
	result, _ := json.Marshal(map[string]interface{}{"success": true, "message_id": id, "node_count": len(nodes)})
	return string(result), nil
}
