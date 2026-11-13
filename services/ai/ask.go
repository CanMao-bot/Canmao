package ai

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gobot/core"
)

// Question 单个问题
type Question struct {
	ID          string   `json:"id"`
	Text        string   `json:"text"`
	Options     []string `json:"options,omitempty"`
	Answer      string
	MultiChoice bool
}

// AskRequest 一次提问会话
type AskRequest struct {
	key       string
	Questions []Question
	UserID    int64
	GroupID   int64
	Channel   chan AskResult
	ExpireAt  time.Time
}

type AskResult struct {
	Answers   map[string]string
	Completed bool
	Reason    string
}

// Asker 一次性输出所有问题, 等待用户一次性回答
type Asker struct {
	mu       sync.Mutex
	pendings map[string]*AskRequest
	bot      *core.Bot
	timeout  time.Duration
}

func NewAsker(bot *core.Bot, timeout time.Duration) *Asker {
	return &Asker{
		pendings: make(map[string]*AskRequest),
		bot:      bot,
		timeout:  timeout,
	}
}

func askKey(ev *core.Event) string {
	if ev.IsGroup() {
		return "g" + int64ToStr(ev.GroupID) + "_u" + int64ToStr(ev.UserID)
	}
	return "p" + int64ToStr(ev.UserID)
}

// Ask 发起一次提问会话, 阻塞等待用户回答
func (a *Asker) Ask(ctx context.Context, ev *core.Event, questions []Question) (*AskResult, error) {
	key := askKey(ev)

	a.mu.Lock()
	if _, exists := a.pendings[key]; exists {
		a.mu.Unlock()
		return &AskResult{Completed: false, Reason: "已有进行中的提问会话, 请先回答完再继续"}, nil
	}
	req := &AskRequest{
		key:       key,
		Questions: questions,
		UserID:    ev.UserID,
		GroupID:   ev.GroupID,
		Channel:   make(chan AskResult, 1),
		ExpireAt:  time.Now().Add(a.timeout),
	}
	msg := a.renderQuestions(req)
	a.pendings[key] = req
	a.mu.Unlock()
	defer a.removeRequest(req)

	// 一次性输出所有问题
	if ev.IsGroup() {
		a.bot.Sender.SendGroupMsg(ev.GroupID, 0, []core.Segment{core.TextSegment(msg)})
	} else {
		a.bot.Sender.SendPrivateMsg(ev.UserID, []core.Segment{core.TextSegment(msg)})
	}

	select {
	case res := <-req.Channel:
		return &res, nil
	case <-time.After(a.timeout):
		return &AskResult{Completed: false, Reason: "等待回答超时"}, nil
	case <-ctx.Done():
		return &AskResult{Completed: false, Reason: "请求被取消"}, nil
	}
}

// Resolve 处理用户对提问的回答; 返回 true 表示消费了该消息
func (a *Asker) Resolve(ev *core.Event) bool {
	key := askKey(ev)
	a.mu.Lock()
	var reply string
	defer func() {
		a.mu.Unlock()
		if reply != "" {
			a.bot.Reply(ev, reply)
		}
	}()
	req, ok := a.pendings[key]
	if !ok {
		return false
	}
	if time.Now().After(req.ExpireAt) {
		delete(a.pendings, key)
		return false
	}

	text := strings.TrimSpace(ev.Text())

	// 检查是否有未完成的问题
	unanswered := a.unansweredIDs(req)
	if len(unanswered) == 0 {
		a.finish(req)
		return true
	}

	// 若用户回答 "取消/算了" 则放弃
	if isReject(text) {
		delete(a.pendings, key)
		reply = "已取消提问"
		req.Channel <- AskResult{Completed: false, Reason: "用户取消"}
		return true
	}

	// 一次性回答: 按顺序分配答案给未答问题
	// 支持两种格式: 逐行回答, 或 1.xxx 2.xxx 编号回答
	lines := splitLines(text)
	ids := unanswered

	// 尝试编号格式: "1. xxx"
	numbered := map[int]string{}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		for idx, q := range req.Questions {
			if q.Answer != "" {
				continue
			}
			prefix := fmt.Sprintf("%d.", idx+1)
			if strings.HasPrefix(t, prefix) {
				numbered[idx] = strings.TrimSpace(strings.TrimPrefix(t, prefix))
			}
		}
	}

	if len(numbered) > 0 {
		for idx, q := range req.Questions {
			if ans, ok := numbered[idx]; ok {
				a.assign(req, q.ID, ans)
			}
		}
	} else {
		// 逐行按顺序分配
		ai := 0
		for _, id := range ids {
			if ai >= len(lines) {
				break
			}
			t := strings.TrimSpace(lines[ai])
			if t == "" {
				ai++
				continue
			}
			a.assign(req, id, t)
			ai++
		}
	}

	// 检查是否全部答完
	remaining := a.unansweredIDs(req)
	if len(remaining) == 0 {
		a.finish(req)
		return true
	}

	// 还有未答的, 提示补齐
	reply = "还有问题未回答, 请补齐: " + strings.Join(remaining, ", ")
	return true
}

func (a *Asker) assign(req *AskRequest, id, answer string) {
	for i := range req.Questions {
		if req.Questions[i].ID == id && req.Questions[i].Answer == "" {
			req.Questions[i].Answer = answer
			return
		}
	}
}

func (a *Asker) unansweredIDs(req *AskRequest) []string {
	var ids []string
	for _, q := range req.Questions {
		if q.Answer == "" {
			ids = append(ids, q.ID)
		}
	}
	return ids
}

func (a *Asker) finish(req *AskRequest) {
	answers := map[string]string{}
	for _, q := range req.Questions {
		answers[q.ID] = q.Answer
	}
	delete(a.pendings, req.key)
	req.Channel <- AskResult{Answers: answers, Completed: true}
}

func (a *Asker) removeRequest(req *AskRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendings[req.key] == req {
		delete(a.pendings, req.key)
	}
}

func (a *Asker) renderQuestions(req *AskRequest) string {
	var b strings.Builder
	b.WriteString("❓ 我需要向你确认几个问题, 请一次性回答(可编号或逐行):\n\n")
	for i, q := range req.Questions {
		b.WriteString(fmt.Sprintf("%d. %s", i+1, q.Text))
		if len(q.Options) > 0 {
			b.WriteString(" (选项: " + strings.Join(q.Options, "/") + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n回复格式示例:\n")
	for i := range req.Questions {
		if i == 0 {
			b.WriteString(fmt.Sprintf("%d. 你的回答", i+1))
		} else {
			b.WriteString(fmt.Sprintf("\n%d. 你的回答", i+1))
		}
	}
	b.WriteString("\n\n回复「取消」放弃提问。")
	return b.String()
}

func splitLines(s string) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	var out []string
	for _, l := range lines {
		out = append(out, l)
	}
	return out
}

var _ = context.Background
