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

// Config ACP client 配置
type Config struct {
	Binary string // opencode 二进制路径, 默认 "opencode"
	Cwd    string // 工作目录
}

// Client ACP 客户端(stdio JSON-RPC)
type Client struct {
	cfg    Config
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Scanner
	// 消息分发
	mu       sync.Mutex
	seq      int
	pending  map[int]chan json.RawMessage
	notifyCh chan json.RawMessage
}

func New(cfg Config) *Client {
	if cfg.Binary == "" {
		cfg.Binary = "opencode"
	}
	return &Client{
		cfg:      cfg,
		pending:  map[int]chan json.RawMessage{},
		notifyCh: make(chan json.RawMessage, 256),
	}
}

// Start 启动 opencode acp 子进程并初始化
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
		return err
	}
	// stderr 用于日志, 忽略或转发
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 opencode acp: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.reader = bufio.NewScanner(stdout)
	c.reader.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	go c.readLoop()

	// 初始化
	if _, err := c.call(ctx, "initialize", map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs":       map[string]interface{}{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]interface{}{"name": "gobot", "version": "1.0.0"},
	}); err != nil {
		return fmt.Errorf("ACP 初始化失败: %w", err)
	}
	return nil
}

// Close 关闭会话与进程
func (c *Client) Close() {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
}

// readLoop 读取子进程 stdout 的 NDJSON 消息
func (c *Client) readLoop() {
	for c.reader.Scan() {
		line := c.reader.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		// 响应(有 id) 或 请求(有 method+id) 或 通知(有 method 无 id)
		if idRaw, ok := msg["id"]; ok {
			var id int
			if err := json.Unmarshal(idRaw, &id); err != nil {
				continue
			}
			c.mu.Lock()
			if ch, ok := c.pending[id]; ok {
				ch <- json.RawMessage(line)
			}
			c.mu.Unlock()
		} else if methodRaw, ok := msg["method"]; ok {
			// 服务端主动请求(需响应)或通知
			var method string
			json.Unmarshal(methodRaw, &method)
			if isRequest(msg) {
				// 服务端请求需应答
				go c.handleServerRequest(method, json.RawMessage(line))
			} else {
				select {
				case c.notifyCh <- json.RawMessage(line):
				default:
				}
			}
		}
	}
}

// isRequest 判断服务端消息是否为需要响应的请求(有 id 且有 method)
func isRequest(msg map[string]json.RawMessage) bool {
	_, hasID := msg["id"]
	_, hasMethod := msg["method"]
	return hasID && hasMethod
}

// handleServerRequest 应答服务端请求(权限等)
func (c *Client) handleServerRequest(method string, raw json.RawMessage) {
	// 解析 id
	var m struct {
		ID int `json:"id"`
	}
	json.Unmarshal(raw, &m)

	var result interface{}
	switch method {
	case "session/request_permission":
		// 默认拒绝权限请求
		result = map[string]interface{}{"outcome": "reject"}
	default:
		result = map[string]interface{}{}
	}
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      m.ID,
		"result":  result,
	}
	c.send(resp)
}

// NextNotification 读取下一条通知(带超时)
func (c *Client) NextNotification(timeout time.Duration) (json.RawMessage, error) {
	select {
	case n := <-c.notifyCh:
		return n, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("等待通知超时")
	}
}

// call 发送 JSON-RPC 请求并等待响应
func (c *Client) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	c.seq++
	id := c.seq
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.send(req); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) send(msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// RunTask 执行一次完整任务: 创建会话 → 发消息 → 收集回复 → 关闭
// 返回 agent 的最终文本回复
func (c *Client) RunTask(ctx context.Context, cwd, prompt string, timeout time.Duration) (string, error) {
	// 创建会话
	resp, err := c.call(ctx, "session/new", map[string]interface{}{
		"cwd":        cwd,
		"mcpServers": []interface{}{},
	})
	if err != nil {
		return "", err
	}
	var newResp struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &newResp); err != nil {
		return "", fmt.Errorf("解析 session/new 响应: %w", err)
	}
	sessionID := newResp.Result.SessionID
	if sessionID == "" {
		return "", fmt.Errorf("session/new 未返回 sessionId: %s", string(resp))
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.call(cctx, "session/close", map[string]interface{}{"sessionId": sessionID})
	}()

	// 发送 prompt
	promptResp := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.seq++
	pid := c.seq
	ch := make(chan json.RawMessage, 1)
	c.pending[pid] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, pid)
		c.mu.Unlock()
	}()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      pid,
		"method":  "session/prompt",
		"params": map[string]interface{}{
			"sessionId": sessionID,
			"prompt":    []map[string]interface{}{{"type": "text", "text": prompt}},
		},
	}
	if err := c.send(req); err != nil {
		return "", err
	}

	// 收集文本回复: 处理通知直到收到 prompt 响应
	var reply strings.Builder
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	// 同时监听通知流和 prompt 响应
	for {
		select {
		case <-deadline.C:
			return strings.TrimSpace(reply.String()), fmt.Errorf("opencode 任务超时(%v)", timeout)
		case <-ctx.Done():
			return strings.TrimSpace(reply.String()), ctx.Err()
		case resp := <-ch:
			// prompt 完成
			var r struct {
				Result struct {
					StopReason string `json:"stopReason"`
				} `json:"result"`
			}
			_ = json.Unmarshal(resp, &r)
			_ = promptResp
			return strings.TrimSpace(reply.String()), nil
		case n := <-c.notifyCh:
			text := extractMessageText(n)
			if text != "" {
				reply.WriteString(text)
			}
		}
	}
}

// extractMessageText 从 session/update 通知提取文本增量
func extractMessageText(raw json.RawMessage) string {
	var n struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				MessageID     string `json:"messageId"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return ""
	}
	if n.Method != "session/update" {
		return ""
	}
	if n.Params.Update.SessionUpdate == "agent_message_chunk" &&
		n.Params.Update.Content.Type == "text" {
		return n.Params.Update.Content.Text
	}
	return ""
}
