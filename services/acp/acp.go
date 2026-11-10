package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Config struct{ Binary, Cwd string }

type taskStream struct {
	mu   sync.Mutex
	text strings.Builder
	err  error
}

type Client struct {
	cfg       Config
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	reader    *bufio.Scanner
	mu        sync.Mutex
	writeGate chan struct{}
	seq       int
	pending   map[int]chan json.RawMessage
	streams   map[string]*taskStream
	notifyCh  chan json.RawMessage
	done      chan struct{}
	stopOnce  sync.Once
	readErr   error
}

func New(cfg Config) *Client {
	if cfg.Binary == "" {
		cfg.Binary = "opencode"
	}
	return &Client{cfg: cfg, pending: make(map[int]chan json.RawMessage), streams: make(map[string]*taskStream), notifyCh: make(chan json.RawMessage, 256), done: make(chan struct{}), writeGate: make(chan struct{}, 1)}
}

func (c *Client) Start(ctx context.Context) error {
	args := []string{"acp"}
	if c.cfg.Cwd != "" {
		args = append(args, "--cwd", c.cfg.Cwd)
	}
	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("启动 opencode acp: %w", err)
	}
	c.cmd, c.stdin = cmd, stdin
	c.reader = bufio.NewScanner(stdout)
	c.reader.Buffer(make([]byte, 64<<10), 16<<20)
	go func() { c.readLoop(); _ = cmd.Wait() }()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = c.call(initCtx, "initialize", map[string]interface{}{
		"protocolVersion": 1, "clientCapabilities": map[string]interface{}{"fs": map[string]interface{}{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo": map[string]interface{}{"name": "gobot", "version": "1.0.0"},
	})
	if err != nil {
		c.Close()
		return fmt.Errorf("ACP 初始化失败: %w", err)
	}
	return nil
}

func (c *Client) stop(err error) {
	c.stopOnce.Do(func() { c.mu.Lock(); c.readErr = err; c.mu.Unlock(); close(c.done) })
}

func (c *Client) Close() {
	c.stop(io.EOF)
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func (c *Client) readLoop() {
	defer func() {
		err := c.reader.Err()
		if err == nil {
			err = io.EOF
		}
		c.stop(err)
	}()
	for c.reader.Scan() {
		line := append(json.RawMessage(nil), c.reader.Bytes()...)
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		// method 优先：服务端请求的 ID 与客户端 pending ID 属于不同方向。
		if raw, ok := msg["method"]; ok {
			var method string
			if json.Unmarshal(raw, &method) != nil {
				continue
			}
			if isRequest(msg) {
				c.handleServerRequest(method, line)
			} else {
				c.routeNotification(line)
			}
			continue
		}
		var id int
		if json.Unmarshal(msg["id"], &id) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- line:
			default:
			}
		}
	}
}

func isRequest(msg map[string]json.RawMessage) bool {
	_, id := msg["id"]
	_, method := msg["method"]
	return id && method
}

func (c *Client) handleServerRequest(method string, raw json.RawMessage) {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return
	}
	resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
	if method == "session/request_permission" {
		// 不代替用户选择 allow 选项，按 ACP 的 outcome 判别字段明确取消。
		resp["result"] = map[string]interface{}{"outcome": map[string]interface{}{"outcome": "cancelled"}}
	} else {
		resp["error"] = map[string]interface{}{"code": -32601, "message": "Unsupported client method"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.send(ctx, resp)
}

func (c *Client) routeNotification(raw json.RawMessage) {
	var n struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return
	}
	c.mu.Lock()
	stream := c.streams[n.Params.SessionID]
	c.mu.Unlock()
	if stream != nil {
		text := extractMessageText(raw)
		stream.mu.Lock()
		if stream.text.Len()+len(text) > 8<<20 {
			stream.err = fmt.Errorf("opencode 输出超过8MB")
		} else if stream.err == nil {
			stream.text.WriteString(text)
		}
		stream.mu.Unlock()
	}
	select {
	case c.notifyCh <- raw:
	default:
	} // 只用于可选监视器；任务输出从不经此队列竞争。
}

func (c *Client) NextNotification(timeout time.Duration) (json.RawMessage, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case n := <-c.notifyCh:
		return n, nil
	case <-timer.C:
		return nil, fmt.Errorf("等待通知超时")
	case <-c.done:
		return nil, c.closedError()
	}
}

func (c *Client) closedError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return io.EOF
}

func rpcError(raw json.RawMessage) error {
	var response struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("ACP RPC %d: %s", response.Error.Code, response.Error.Message)
	}
	if len(response.Result) == 0 {
		return fmt.Errorf("ACP 响应缺少 result")
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.seq++
	id := c.seq
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(ctx, map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp, rpcError(resp)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		select {
		case resp := <-ch:
			return resp, rpcError(resp)
		default:
			return nil, c.closedError()
		}
	}
}

func (c *Client) send(ctx context.Context, msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	select {
	case c.writeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedError()
	}
	defer func() { <-c.writeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return c.closedError()
	default:
	}
	if c.stdin == nil {
		return fmt.Errorf("ACP 尚未连接")
	}
	written := make(chan error, 1)
	go func() { _, writeErr := c.stdin.Write(append(data, '\n')); written <- writeErr }()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		// 半写入会破坏 JSON-RPC 帧，关闭失去响应的传输，避免后台继续执行。
		c.stop(ctx.Err())
		_ = c.stdin.Close()
		return ctx.Err()
	case <-c.done:
		_ = c.stdin.Close()
		return c.closedError()
	}
}

func (c *Client) RunTask(ctx context.Context, cwd, prompt string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.call(ctx, "session/new", map[string]interface{}{"cwd": cwd, "mcpServers": []interface{}{}})
	if err != nil {
		return "", err
	}
	var created struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err = json.Unmarshal(resp, &created); err != nil {
		return "", err
	}
	id := created.Result.SessionID
	if id == "" {
		return "", fmt.Errorf("session/new 未返回 sessionId")
	}
	stream := &taskStream{}
	c.mu.Lock()
	if _, exists := c.streams[id]; exists {
		c.mu.Unlock()
		return "", fmt.Errorf("ACP 返回了正在使用的 sessionId")
	}
	c.streams[id] = stream
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.streams, id); c.mu.Unlock() }()
	resp, err = c.call(ctx, "session/prompt", map[string]interface{}{"sessionId": id, "prompt": []map[string]interface{}{{"type": "text", "text": prompt}}})
	if ctx.Err() != nil {
		// session/cancel 是通知，不等待不存在的 session/close RPC。
		cancelCtx, stop := context.WithTimeout(context.Background(), time.Second)
		_ = c.send(cancelCtx, map[string]interface{}{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]interface{}{"sessionId": id}})
		stop()
	}
	stream.mu.Lock()
	reply, streamErr := strings.TrimSpace(stream.text.String()), stream.err
	stream.mu.Unlock()
	if err != nil {
		return reply, err
	}
	if streamErr != nil {
		return reply, streamErr
	}
	var done struct {
		Result struct {
			StopReason string `json:"stopReason"`
		} `json:"result"`
	}
	if err = json.Unmarshal(resp, &done); err != nil {
		return reply, err
	}
	if done.Result.StopReason != "end_turn" {
		return reply, fmt.Errorf("opencode 未正常完成: %s", done.Result.StopReason)
	}
	return reply, nil
}

func extractMessageText(raw json.RawMessage) string {
	var n struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string                      `json:"sessionUpdate"`
				Content       struct{ Type, Text string } `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return ""
	}
	if n.Method == "session/update" && n.Params.Update.SessionUpdate == "agent_message_chunk" && n.Params.Update.Content.Type == "text" {
		return n.Params.Update.Content.Text
	}
	return ""
}
