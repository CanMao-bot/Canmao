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
