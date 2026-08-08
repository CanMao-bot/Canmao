package core

import (
	"fmt"
)

type Event struct {
	Type       string   `json:"post_type"`      // message / notice / request
	DetailType string   `json:"message_type"`   // group / private
	NoticeType string   `json:"notice_type"`    // group_increase ...
	RequestType string  `json:"request_type"`   // friend / group
	SubType    string   `json:"sub_type"`
	SelfID     int64    `json:"self_id"`
	UserID     int64    `json:"user_id"`
	GroupID    int64    `json:"group_id"`
	MessageID  int64    `json:"message_id"`
	Message    []Segment `json:"message"`
	RawMessage string   `json:"raw_message"`
	OperatorID int64    `json:"operator_id"`
	Comment    string   `json:"comment"`        // request 事件的验证信息/入群理由
	Flag       string   `json:"flag"`           // request 事件的审批标识(set_group_add_request 用)
	File       map[string]interface{} `json:"file"` // notice group_upload 的群文件信息
}

type Segment struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

type Reply struct {
	Message []Segment `json:"message"`
}

func (e *Event) IsGroup() bool  { return e.DetailType == "group" }
func (e *Event) IsPrivate() bool { return e.DetailType == "private" }

// IsMentioned 判断消息是否 @ 了指定 QQ(或 @全体)
func (e *Event) IsMentioned(selfID int64) bool {
	self := fmt.Sprintf("%d", selfID)
	for _, seg := range e.Message {
		if seg.Type != "at" {
			continue
		}
		qq, ok := seg.Data["qq"].(string)
		if !ok {
			if f, ok := seg.Data["qq"].(float64); ok {
				qq = fmt.Sprintf("%d", int64(f))
			}
		}
		if qq == self || qq == "all" {
			return true
		}
	}
	return false
}

// IsMentionedByUser 判断消息是否包含 @某用户
func (e *Event) IsMentionedByUser(userID int64) bool {
	uid := fmt.Sprintf("%d", userID)
	for _, seg := range e.Message {
		if seg.Type != "at" {
			continue
		}
		qq, ok := seg.Data["qq"].(string)
		if !ok {
			if f, ok := seg.Data["qq"].(float64); ok {
				qq = fmt.Sprintf("%d", int64(f))
			}
		}
		if qq == uid {
			return true
		}
	}
	return false
}

// ReplyID 提取引用回复(reply)消息段的被引用消息 ID, 无则 0
func (e *Event) ReplyID() int64 {
	for _, seg := range e.Message {
		if seg.Type != "reply" {
			continue
		}
		switch v := seg.Data["id"].(type) {
		case string:
			var id int64
			fmt.Sscanf(v, "%d", &id)
			return id
		case float64:
			return int64(v)
		}
	}
	return 0
}

// AtQQs 返回消息中所有 at 段的 QQ 列表("all" 跳过)
func (e *Event) AtQQs() []int64 {
	var qqs []int64
	for _, seg := range e.Message {
		if seg.Type != "at" {
			continue
		}
		switch v := seg.Data["qq"].(type) {
		case string:
			if v == "all" {
				continue
			}
			var id int64
			if _, err := fmt.Sscanf(v, "%d", &id); err == nil {
				qqs = append(qqs, id)
			}
		case float64:
			qqs = append(qqs, int64(v))
		}
	}
	return qqs
}

func (e *Event) Text() string {
	var s string
	for _, seg := range e.Message {
		if seg.Type == "text" {
			if t, ok := seg.Data["text"].(string); ok {
				s += t
			}
		}
	}
	return s
}

func TextSegment(text string) Segment {
	return Segment{Type: "text", Data: map[string]interface{}{"text": text}}
}

func ImageSegment(file string) Segment {
	return Segment{Type: "image", Data: map[string]interface{}{"file": file}}
}

// RecordSegment 语音消息段
func RecordSegment(file string) Segment {
	return Segment{Type: "record", Data: map[string]interface{}{"file": file}}
}

func AtSegment(uid int64) Segment {
	return Segment{Type: "at", Data: map[string]interface{}{"qq": uid}}
}

// ForwardNode 合并转发节点
type ForwardNode struct {
	UserID   int64     `json:"user_id,omitempty"`
	Uin      string    `json:"uin,omitempty"`
	Name     string    `json:"name,omitempty"`
	Nickname string    `json:"nickname,omitempty"`
	Content  []Segment `json:"content"`
}
