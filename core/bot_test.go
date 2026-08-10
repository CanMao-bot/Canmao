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

func TestReplyID(t *testing.T) {
	// 无 reply 段
	ev := &Event{Message: []Segment{TextSegment("hi")}}
	if ev.ReplyID() != 0 {
		t.Fatalf("无 reply 段应返回 0, got %d", ev.ReplyID())
	}
	// id 为 string
	ev = &Event{Message: []Segment{
		{Type: "reply", Data: map[string]interface{}{"id": "12345"}},
		TextSegment("回复"),
	}}
	if ev.ReplyID() != 12345 {
		t.Fatalf("string id 应解析为 12345, got %d", ev.ReplyID())
	}
	// id 为 float64
	ev = &Event{Message: []Segment{
		{Type: "reply", Data: map[string]interface{}{"id": float64(6789)}},
	}}
	if ev.ReplyID() != 6789 {
		t.Fatalf("float64 id 应解析为 6789, got %d", ev.ReplyID())
	}
}

func TestAtQQs(t *testing.T) {
	ev := &Event{Message: []Segment{
		{Type: "at", Data: map[string]interface{}{"qq": "10001"}},
		TextSegment(" "),
		{Type: "at", Data: map[string]interface{}{"qq": "all"}},
		{Type: "at", Data: map[string]interface{}{"qq": float64(20002)}},
	}}
	qqs := ev.AtQQs()
	if len(qqs) != 2 || qqs[0] != 10001 || qqs[1] != 20002 {
		t.Fatalf("AtQQs 应返回 [10001 20002], got %v", qqs)
	}
	// 无 at 段
	ev2 := &Event{Message: []Segment{TextSegment("hi")}}
	if q := ev2.AtQQs(); len(q) != 0 {
		t.Fatalf("无 at 段应为空, got %v", q)
	}
}

func TestParseAtSegments(t *testing.T) {
	// 无标记返回 nil
	if segs := parseAtSegments("普通文本"); segs != nil {
		t.Fatalf("无标记应返回 nil, got %v", segs)
	}

	segs := parseAtSegments("你好 [@12345] 在吗 [@全体成员] 收到")
	// text, at(12345), text, at(all), text
	if len(segs) != 5 {
		t.Fatalf("应切出 5 段, got %d: %v", len(segs), segs)
	}
	if segs[0].Type != "text" || segs[0].Data["text"] != "你好 " {
		t.Fatalf("第1段应为 text, got %v", segs[0])
	}
	if segs[1].Type != "at" || segs[1].Data["qq"] != int64(12345) {
		t.Fatalf("第2段应为 at(12345), got %v", segs[1])
	}
	if segs[3].Type != "at" || segs[3].Data["qq"] != "all" {
		t.Fatalf("第4段应为 at(all), got %v", segs[3])
	}
	if segs[4].Type != "text" || segs[4].Data["text"] != " 收到" {
		t.Fatalf("第5段应为 text, got %v", segs[4])
	}
}

func TestReplyGroupAtMark(t *testing.T) {
	cfg := &Config{Bot: BotConfig{LongMessageForward: false, LongMessageThreshold: 200}}
	bot := NewBot(cfg)
	var got []Segment
	sender := &testSender{sendGroup: func(gid, uid int64, msg []Segment) error {
		got = msg
		return nil
	}}
	bot.SetSender(sender)
	ev := &Event{Type: "message", DetailType: "group", GroupID: 1, UserID: 2}
	bot.Reply(ev, "好的 [@10001] 马上处理")
	if len(got) != 3 || got[1].Type != "at" || got[1].Data["qq"] != int64(10001) {
		t.Fatalf("群聊回复应切出 at 段, got %v", got)
	}
}

func TestReplyPrivateNoAtParse(t *testing.T) {
	cfg := &Config{Bot: BotConfig{LongMessageForward: false, LongMessageThreshold: 200}}
	bot := NewBot(cfg)
	var got []Segment
	sender := &testSender{sendPrivate: func(uid int64, msg []Segment) error {
		got = msg
		return nil
	}}
	bot.SetSender(sender)
	ev := &Event{Type: "message", DetailType: "private", UserID: 2}
	bot.Reply(ev, "好的 [@10001]")
	if len(got) != 1 || got[0].Type != "text" || got[0].Data["text"] != "好的 [@10001]" {
		t.Fatalf("私聊应保持纯文本, got %v", got)
	}
}

func TestFeatureOn(t *testing.T) {
	// nil 视为开启
	if !FeatureOn(nil) {
		t.Fatal("nil 应视为开启")
	}
	on, off := true, false
	if !FeatureOn(&on) {
		t.Fatal("true 应为开启")
	}
	if FeatureOn(&off) {
		t.Fatal("false 应为关闭")
	}
}

func TestReplyGroupAtSendDisabled(t *testing.T) {
	// at_send 关闭时, 群聊回复保持纯文本单段
	off := false
	cfg := &Config{
		Bot:      BotConfig{LongMessageForward: false, LongMessageThreshold: 200},
		Features: FeaturesConfig{AtSend: &off},
	}
	bot := NewBot(cfg)
	var got []Segment
	sender := &testSender{sendGroup: func(gid, uid int64, msg []Segment) error {
		got = msg
		return nil
	}}
	bot.SetSender(sender)
	ev := &Event{Type: "message", DetailType: "group", GroupID: 1, UserID: 2}
	bot.Reply(ev, "好的 [@10001] 马上处理")
	if len(got) != 1 || got[0].Type != "text" || got[0].Data["text"] != "好的 [@10001] 马上处理" {
		t.Fatalf("at_send 关闭应保持纯文本, got %v", got)
	}
}
