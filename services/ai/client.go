package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gobot/core"
)

type ToolParam struct {
	Type        string                `json:"type"`
	Description string                `json:"description,omitempty"`
	Enum        []string              `json:"enum,omitempty"`
	Items       map[string]interface{} `json:"items,omitempty"`
}

type ToolParameters struct {
	Type       string                `json:"type"`
	Properties map[string]*ToolParam `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
}

type Tool struct {
	Type     string        `json:"type"`
	Function ToolFunction  `json:"function"`
	Risk     RiskLevel     `json:"-"`
	Callback func(ctx context.Context, args map[string]interface{}) (string, error) `json:"-"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  ToolParameters `json:"parameters"`
}

// NewTool 创建标准 function 类型工具, params 为属性名->属性定义, required 为必填参数
// 默认风险为中风险(需发起人确认); 需覆盖请设置 Tool.Risk
func NewTool(name, desc string, params map[string]*ToolParam, required []string, cb func(ctx context.Context, args map[string]interface{}) (string, error)) Tool {
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        name,
			Description: desc,
			Parameters: ToolParameters{
				Type:       "object",
				Properties: params,
				Required:   required,
			},
		},
		Risk:     RiskMedium,
		Callback: cb,
	}
}

type Message struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
}

// ContentPart 多模态消息内容片段
type ContentPart struct {
	Type     string         `json:"type"` // text / image_url
	Text     string         `json:"text,omitempty"`
	ImageURL *ImageURLPart  `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL string `json:"url"`
}

// NewTextMessage 纯文本消息
func NewTextMessage(role, content string) Message {
	return Message{Role: role, Content: content}
}

// NewMultimodalMessage 含图片的多模态消息
func NewMultimodalMessage(role, text string, imageURLs []string) Message {
	parts := make([]ContentPart, 0, len(imageURLs)+1)
	if text != "" {
		parts = append(parts, ContentPart{Type: "text", Text: text})
	}
	for _, u := range imageURLs {
		parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImageURLPart{URL: u}})
	}
	return Message{Role: role, Content: parts}
}

// TextContent 提取消息的文本内容(用于会话历史)
func (m Message) TextContent() string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []ContentPart:
		var b strings.Builder
		for _, p := range c {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// EndpointSource 提供当前连接参数(由 ModelRegistry 实现)
type EndpointSource interface {
	Endpoint() (baseURL, apiKey, model string, err error)
}

// EndpointSourceEx 扩展: 额外提供代理
type EndpointSourceEx interface {
	EndpointEx() (baseURL, apiKey, model, proxy string, err error)
}

type Client struct {
	cfg    *core.AIConfig
	source EndpointSource
	http   *http.Client
}

func NewClient(cfg *core.AIConfig) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: time.Duration(cfg.Timeout) * time.Second},
	}
}

// SetEndpointSource 设置动态端点源(模型注册表)
func (c *Client) SetEndpointSource(s EndpointSource) { c.source = s }

// endpoint 返回当前请求的 baseURL/apiKey/model/proxy
func (c *Client) endpoint() (string, string, string, string) {
	if ex, ok := c.source.(EndpointSourceEx); ok {
		if base, key, model, proxy, err := ex.EndpointEx(); err == nil && base != "" {
			return base, key, model, proxy
		}
	}
	if c.source != nil {
		if base, key, model, err := c.source.Endpoint(); err == nil && base != "" {
			return base, key, model, ""
		}
	}
	return c.cfg.BaseURL, c.cfg.APIKey, c.cfg.Model, ""
}

type ctxModelKey struct{}

// WithModelOverride 在 context 中指定本次请求使用的模型(按用户组)
func WithModelOverride(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, ctxModelKey{}, model)
}

func modelFromCtx(ctx context.Context) string {
	if m, ok := ctx.Value(ctxModelKey{}).(string); ok {
		return m
	}
	return ""
}

// Complete 返回模型完整回复消息(可能含 ToolCalls)
func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool) (*Message, error) {
	baseURL, apiKey, model, proxy := c.endpoint()
	if m := modelFromCtx(ctx); m != "" {
		model = m
	}
	req := struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Tools       []Tool    `json:"tools"`
		MaxTokens   int       `json:"max_tokens"`
		Temperature float64   `json:"temperature"`
		Stream      bool      `json:"stream"`
	}{
		Model:       model,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: c.cfg.Temperature,
		Stream:      false,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := c.http
	if proxy != "" {
		client = httpClientFor(proxy, time.Duration(c.cfg.Timeout)*time.Second)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用 LLM API: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM API 返回 %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("解析 LLM 响应: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("LLM 无响应")
	}
	msg := result.Choices[0].Message
	return &msg, nil
}
