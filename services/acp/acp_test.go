package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type bufferCloser struct{ bytes.Buffer }

func (*bufferCloser) Close() error { return nil }

func TestServerRequestWithCollidingIDIsAnsweredAndNotDeliveredAsResponse(t *testing.T) {
	c := New(Config{})
	out := &bufferCloser{}
	c.stdin = out
	c.pending[1] = make(chan json.RawMessage, 1)
	c.reader = bufio.NewScanner(strings.NewReader("{\"id\":1,\"method\":\"session/request_permission\",\"params\":{}}\n{\"id\":1,\"result\":{\"sessionId\":\"s\"}}\n"))
	c.readLoop()
	var reply struct {
		ID     int
		Result struct{ Outcome struct{ Outcome string } }
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != 1 || reply.Result.Outcome.Outcome != "cancelled" {
		t.Fatalf("权限请求须明确拒绝: %s", out.String())
	}
	if len(c.pending[1]) != 1 {
		t.Fatal("应仅投递真实响应")
	}
	if err := rpcError(<-c.pending[1]); err != nil {
		t.Fatal(err)
	}
}

func mockClient(t *testing.T, handle func(map[string]json.RawMessage, func(interface{}))) *Client {
	t.Helper()
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	c := New(Config{})
	c.stdin = inputW
	c.reader = bufio.NewScanner(outputR)
	readerDone, serverDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(readerDone); c.readLoop() }()
	go func() {
		defer close(serverDone)
		scanner := bufio.NewScanner(inputR)
		for scanner.Scan() {
			var request map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				continue
			}
			handle(request, func(v interface{}) { data, _ := json.Marshal(v); _, _ = outputW.Write(append(data, '\n')) })
		}
	}()
	t.Cleanup(func() {
		c.Close()
		inputR.Close()
		outputW.Close()
		outputR.Close()
		<-serverDone
		<-readerDone
	})
	return c
}

func TestConcurrentTasksKeepTheirOwnOutputIncludingFinalChunk(t *testing.T) {
	type promptCall struct {
		id            json.RawMessage
		session, text string
	}
	var prompts []promptCall
	seq := 0
	c := mockClient(t, func(req map[string]json.RawMessage, send func(interface{})) {
		var method string
		_ = json.Unmarshal(req["method"], &method)
		if method == "session/new" {
			seq++
			send(map[string]interface{}{"id": req["id"], "result": map[string]interface{}{"sessionId": fmt.Sprintf("s%d", seq)}})
			return
		}
		if method != "session/prompt" {
			return
		}
		var params struct {
			SessionID string `json:"sessionId"`
			Prompt    []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(req["params"], &params)
		prompts = append(prompts, promptCall{req["id"], params.SessionID, params.Prompt[0].Text})
		if len(prompts) != 2 {
			return
		}
		for _, suffix := range []string{"first", "last"} {
			for _, p := range prompts {
				send(map[string]interface{}{"method": "session/update", "params": map[string]interface{}{"sessionId": p.session, "update": map[string]interface{}{"sessionUpdate": "agent_message_chunk", "content": map[string]interface{}{"type": "text", "text": p.text + suffix}}}})
			}
		}
		for _, p := range prompts {
			send(map[string]interface{}{"id": p.id, "result": map[string]interface{}{"stopReason": "end_turn"}})
		}
	})
	type result struct {
		task, reply string
		err         error
	}
	results := make(chan result, 2)
	for _, task := range []string{"alpha", "beta"} {
		go func(task string) {
			reply, err := c.RunTask(context.Background(), "", task, time.Second)
			results <- result{task, reply, err}
		}(task)
	}
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil || r.reply != r.task+"first"+r.task+"last" {
				t.Fatalf("输出串流或尾块丢失: %+v", r)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("并发任务未完成")
		}
	}
}

func TestTaskCancellationAndRPCFailures(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	c := mockClient(t, func(req map[string]json.RawMessage, send func(interface{})) {
		var method string
		_ = json.Unmarshal(req["method"], &method)
		switch method {
		case "session/new":
			send(map[string]interface{}{"id": req["id"], "result": map[string]interface{}{"sessionId": "s"}})
		case "session/cancel":
			cancelled <- struct{}{}
		}
	})
	_, err := c.RunTask(context.Background(), "", "wait", 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应覆盖整个任务的超时: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("未发送取消通知")
	}
	for _, raw := range []string{`{"id":1,"error":{"code":-1,"message":"failed"}}`, `{"id":1}`} {
		if rpcError(json.RawMessage(raw)) == nil {
			t.Fatalf("错误响应不能报告成功: %s", raw)
		}
	}
}

func TestPendingCallUnblocksOnEOF(t *testing.T) {
	c := New(Config{})
	c.stdin = &bufferCloser{}
	c.stop(io.EOF)
	if _, err := c.call(context.Background(), "session/new", nil); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestTaskDeadlineIncludesBlockedPipeWrite(t *testing.T) {
	c := New(Config{})
	r, w := io.Pipe()
	defer r.Close()
	c.stdin = w
	defer c.Close()
	done := make(chan error, 1)
	go func() { _, err := c.RunTask(context.Background(), "", "blocked", 20*time.Millisecond); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("管道写入绕过任务超时")
	}
}
