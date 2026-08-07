package core

import (
	"strings"
	"testing"
)

func TestSplitForwardNodes(t *testing.T) {
	text := strings.Repeat("很长的一段内容测试", 200) // 200 * 8 = 1600 字符
	nodes := splitForwardNodes(text, "gobot", 123)
	if len(nodes) < 1 {
		t.Fatal("应至少拆出1个节点")
	}
	// 每节点不超过 chunkSize
	for _, n := range nodes {
		chunk := ""
		for _, s := range n.Content {
			if s.Type == "text" {
				chunk += s.Data["text"].(string)
			}
		}
		if len([]rune(chunk)) > forwardChunkSize {
			t.Fatalf("节点超长: %d", len([]rune(chunk)))
		}
	}
	// 拼接后内容一致(去除节点间空格)
	var joined string
	for _, n := range nodes {
		for _, s := range n.Content {
			joined += s.Data["text"].(string)
		}
	}
	if strings.ReplaceAll(joined, "\n", "") != strings.ReplaceAll(text, "\n", "") {
		t.Fatal("拆分后拼接内容不一致")
	}
	if nodes[0].Name != "gobot" {
		t.Fatalf("节点 name = %q", nodes[0].Name)
	}
}

func TestSplitForwardByParagraph(t *testing.T) {
	text := "第一段内容比较长的一段话。\n\n第二段内容也比较长。\n\n第三段内容。"
	nodes := splitForwardNodes(text, "gobot", 123)
	// 3 个段落 → 3 个节点
	if len(nodes) != 3 {
		t.Fatalf("按段落应拆为 3 个节点, got %d", len(nodes))
	}
	first := nodes[0].Content[0].Data["text"].(string)
	if !strings.Contains(first, "第一段") {
		t.Fatalf("第一个节点应含第一段, got %q", first)
	}
}

func TestReplyShortMessageNormal(t *testing.T) {
	cfg := &Config{Bot: BotConfig{LongMessageForward: true, LongMessageThreshold: 10}}
	bot := NewBot(cfg)
	sent := ""
	sender := &testSender{sendGroup: func(gid, uid int64, msg []Segment) error {
		sent = msg[0].Data["text"].(string)
		return nil
	}}
	bot.SetSender(sender)
	ev := &Event{Type: "message", DetailType: "group", GroupID: 1, UserID: 2}
	bot.Reply(ev, "短消息")
	if sent != "短消息" {
		t.Fatalf("短消息应普通发送, got %q", sent)
	}
}

type testSender struct {
	sendGroup func(gid, uid int64, msg []Segment) error
	sendPrivate func(uid int64, msg []Segment) error
}

func (t *testSender) SendGroupMsg(gid, uid int64, msg []Segment) error {
	if t.sendGroup != nil {
		return t.sendGroup(gid, uid, msg)
	}
	return nil
}
func (t *testSender) SendPrivateMsg(uid int64, msg []Segment) error {
	if t.sendPrivate != nil {
		return t.sendPrivate(uid, msg)
	}
	return nil
}
