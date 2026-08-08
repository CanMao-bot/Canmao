package ai

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gobot/core"
)

// cqAtRe 匹配消息文本中的 [CQ:at,qq=xxx]
var cqAtRe = regexp.MustCompile(`\[CQ:at,qq=(\d+|all)\]`)

// quoteTextMaxLen 注入上下文的引用文本最大长度
const quoteTextMaxLen = 500

// registerViewMessageTool 注册按消息ID查看消息内容的工具
func (s *Service) registerViewMessageTool() {
	t := NewTool("view_message", "按消息ID查看一条消息(引用/历史消息)的详细内容, 返回发送者和文本(图片记为[图片])。当用户提到某条消息、需要查看引用或历史消息内容时使用。",
		map[string]*ToolParam{
			"message_id": {Type: "integer", Description: "消息ID"},
		}, []string{"message_id"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			id, _ := args["message_id"].(float64)
			if id <= 0 {
				return "错误: 请提供有效的 message_id", nil
			}
			info, err := s.bot.GetMessage(int64(id))
			if err != nil {
				return "获取消息失败: " + err.Error(), nil
			}
			name := info.Nickname
			if name == "" {
				name = int64ToStr(info.UserID)
			}
			when := time.Unix(info.Time, 0).Format("2006-01-02 15:04:05")
			return fmt.Sprintf("%s(%d) 于 %s: %s", name, info.UserID, when, segmentsText(info.Message)), nil
		})
	t.Risk = RiskLow
	s.tools = append(s.tools, t)
}

// segmentsText 提取消息段可读文本(text 拼接, 图片记为 [图片], 其它段忽略)
func segmentsText(segs []core.Segment) string {
	var b strings.Builder
	for _, seg := range segs {
		switch seg.Type {
		case "text":
			if t, ok := seg.Data["text"].(string); ok {
				b.WriteString(t)
			}
		case "image":
			b.WriteString("[图片]")
		}
	}
	return b.String()
}

// fetchQuote 拉取被引用消息, 返回展示文本和图片 data URI(配置识图模型时下载引用中的图片)
func (s *Service) fetchQuote(replyID int64) (string, []string) {
	info, err := s.bot.GetMessage(replyID)
	if err != nil {
		log.Printf("[ai] 获取引用消息失败: %v", err)
		return "", nil
	}
	text := segmentsText(info.Message)
	if r := []rune(text); len(r) > quoteTextMaxLen {
		text = string(r[:quoteTextMaxLen]) + "..."
	}
	name := info.Nickname
	if name == "" {
		name = int64ToStr(info.UserID)
	}
	// 引用消息含图片时一并下载, 交给识图模型转述
	var imgs []string
	if s.cfg.Memory.VisionModel != "" {
		for _, seg := range info.Message {
			if seg.Type != "image" {
				continue
			}
			u, _ := seg.Data["url"].(string)
			if u == "" {
				continue
			}
			if du, err := downloadImageAsDataURI(u); err == nil {
				imgs = append(imgs, du)
			} else {
				log.Printf("[ai] 引用图片下载失败: %v", err)
			}
		}
	}
	return fmt.Sprintf("[引用消息] %s(%d): %s", name, info.UserID, text), imgs
}

// resolveAtMentions 把群消息中的 [CQ:at,qq=xxx] 解析为可读的 [@昵称(xxx)]
func (s *Service) resolveAtMentions(ev *core.Event, text string) string {
	if !cqAtRe.MatchString(text) {
		return text
	}
	return cqAtRe.ReplaceAllStringFunc(text, func(m string) string {
		qq := cqAtRe.FindStringSubmatch(m)[1]
		if qq == "all" {
			return "[@全体成员]"
		}
		uid, _ := strconv.ParseInt(qq, 10, 64)
		return fmt.Sprintf("[@%s(%d)]", s.memberName(ev.GroupID, uid), uid)
	})
}

// memberName 获取群成员显示名(群名片优先, 其次昵称, 失败用 QQ 号)
func (s *Service) memberName(groupID, userID int64) string {
	fallback := strconv.FormatInt(userID, 10)
	gac, ok := s.bot.Sender.(core.GroupAdminClient)
	if !ok {
		return fallback
	}
	info, err := gac.GetGroupMemberInfo(groupID, userID)
	if err != nil {
		return fallback
	}
	if card, _ := info["card"].(string); card != "" {
		return card
	}
	if nick, _ := info["nickname"].(string); nick != "" {
		return nick
	}
	return fallback
}
