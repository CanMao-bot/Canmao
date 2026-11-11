package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gobot/core"
)

func TestForwardReceiptRoutesAndSerializesTextNodes(t *testing.T) {
	for _, gid := range []int64{0, 10} {
		requests := make(chan map[string]interface{}, 1)
		o := localOneBot(t, func(conn *websocket.Conn) {
			var req map[string]interface{}
			if conn.ReadJSON(&req) != nil {
				return
			}
			requests <- req
			_ = respondQQ(conn, req["echo"], "ok", 0, map[string]interface{}{"message_id": "81"})
			_, _, _ = conn.ReadMessage()
		})
		nodes := []core.ForwardNode{{Uin: "9", Name: "bot", Nickname: "bot", Content: []core.Segment{core.TextSegment("first"), core.TextSegment("[CQ:at,qq=all]")}}}
		id, err := o.SendForwardWithReceipt(context.Background(), gid, 3, nodes)
		if err != nil || id != 81 {
			t.Fatalf("id=%d err=%v", id, err)
		}
		req := <-requests
		if req["action"] != "send_forward_msg" {
			t.Fatal(req["action"])
		}
		params := req["params"].(map[string]interface{})
		if gid > 0 {
			if params["group_id"] != float64(gid) || params["user_id"] != nil {
				t.Fatal(params)
			}
		} else if params["user_id"] != float64(3) || params["group_id"] != nil {
			t.Fatal(params)
		}
		segments := params["messages"].([]interface{})
		data := segments[0].(map[string]interface{})["data"].(map[string]interface{})
		if data["uin"] != "9" || data["nickname"] != "bot" {
			t.Fatal(data)
		}
		content := data["content"].([]interface{})
		if content[1].(map[string]interface{})["type"] != "text" {
			t.Fatal(content)
		}
	}
}

func TestForwardMissingReceiptFailureAndCancellation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		o := localOneBot(t, func(conn *websocket.Conn) {
			var req map[string]interface{}
			if conn.ReadJSON(&req) != nil {
				return
			}
			status, code := "ok", 0
			if failed {
				status, code = "failed", 1200
			}
			_ = respondQQ(conn, req["echo"], status, code, map[string]interface{}{})
			_, _, _ = conn.ReadMessage()
		})
		if _, err := o.SendForwardWithReceipt(context.Background(), 0, 3, nil); err == nil {
			t.Fatal("发送失败或缺少回执被当作成功")
		}
	}
	started := make(chan struct{})
	o := localOneBot(t, func(conn *websocket.Conn) {
		var req map[string]interface{}
		if conn.ReadJSON(&req) == nil {
			close(started)
		}
		_, _, _ = conn.ReadMessage()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := o.SendForwardWithReceipt(ctx, 0, 3, nil); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未调用接口")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("无法取消合并转发")
	}
}
