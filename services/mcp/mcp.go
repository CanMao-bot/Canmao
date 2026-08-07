package mcp

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"gobot/core"
	"gobot/services/ai"
)

type Server struct {
	cfg    core.MCPServer
	client *mcpclient.Client
	tools  []ai.Tool
}

type Manager struct {
	servers []*Server
}

func New(cfg *core.MCPConfig) *Manager {
	m := &Manager{}
	for _, sc := range cfg.Servers {
		if !sc.Enabled {
			continue
		}
		srv := &Server{cfg: sc}
		m.servers = append(m.servers, srv)
	}
	return m
}

func (m *Manager) ConnectAll(ctx context.Context) error {
	for _, s := range m.servers {
		if err := s.connect(ctx); err != nil {
			return fmt.Errorf("连接 MCP server %s: %w", s.cfg.Name, err)
		}
	}
	return nil
}

func (m *Manager) Close() {
	for _, s := range m.servers {
		if s.client != nil {
			s.client.Close()
		}
	}
}

func (s *Server) connect(ctx context.Context) error {
	log.Printf("[mcp] 连接 MCP server: %s (%s)", s.cfg.Name, s.cfg.Transport)
	var err error
	switch s.cfg.Transport {
	case "stdio":
		cmd := resolveNpx(s.cfg.Command)
		s.client, err = client.NewStdioMCPClient(cmd, s.cfg.Env, s.cfg.Args...)
	default:
		return fmt.Errorf("不支持的传输方式: %s", s.cfg.Transport)
	}
	if err != nil {
		return fmt.Errorf("启动 MCP 客户端: %w", err)
	}

	if err := s.client.Start(ctx); err != nil {
		return fmt.Errorf("启动 MCP 传输: %w", err)
	}
	if _, err := s.client.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		return fmt.Errorf("初始化 MCP: %w", err)
	}
	tools, err := s.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("列出 MCP 工具: %w", err)
	}
	for _, t := range tools.Tools {
		s.tools = append(s.tools, s.toolToAI(t))
	}
	log.Printf("[mcp] server %s 加载 %d 个工具", s.cfg.Name, len(s.tools))
	return nil
}

func (s *Server) toolToAI(t mcp.Tool) ai.Tool {
	params, required := schemaToParams(t.InputSchema)
	tool := ai.NewTool(safeToolName(s.cfg.Name)+"_"+safeToolName(t.Name), t.Description, params, required,
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			req := mcp.CallToolRequest{}
			req.Params.Name = t.Name
			req.Params.Arguments = args
			res, err := s.client.CallTool(ctx, req)
			if err != nil {
				return "", err
			}
			var parts []string
			for _, c := range res.Content {
				if tc, ok := c.(mcp.TextContent); ok {
					parts = append(parts, tc.Text)
				} else {
					parts = append(parts, fmt.Sprintf("%v", c))
				}
			}
			if res.IsError {
				return "", fmt.Errorf("MCP 工具 %s 执行出错: %s", t.Name, joinParts(parts))
			}
			return joinParts(parts), nil
		})
	// MCP 工具涉及外部系统操作, 默认高风险(需主人/管理员确认)
	tool.Risk = ai.RiskHigh
	return tool
}

func (m *Manager) Tools() []ai.Tool {
	var all []ai.Tool
	for _, s := range m.servers {
		all = append(all, s.tools...)
	}
	return all
}

func joinParts(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

func resolveNpx(cmd string) string {
	// npx 不在 PATH 时尝试常见安装路径
	return cmd
}

func schemaToParams(schema interface{}) (map[string]*ai.ToolParam, []string) {
	params := map[string]*ai.ToolParam{}
	required := []string{}

	props := map[string]interface{}{}

	switch v := schema.(type) {
	case mcp.ToolInputSchema:
		props = v.Properties
		required = v.Required
	case map[string]interface{}:
		if p, ok := v["properties"].(map[string]interface{}); ok {
			props = p
		}
		if r, ok := v["required"].([]interface{}); ok {
			for _, x := range r {
				if s, ok := x.(string); ok {
					required = append(required, s)
				}
			}
		}
	}

	for name, raw := range props {
		p, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		tp := &ai.ToolParam{
			Type:        str(p["type"]),
			Description: str(p["description"]),
		}
		if items, ok := p["items"].(map[string]interface{}); ok {
			tp.Items = map[string]interface{}{"type": str(items["type"])}
		}
		if enum, ok := p["enum"].([]interface{}); ok {
			for _, e := range enum {
				if s, ok := e.(string); ok {
					tp.Enum = append(tp.Enum, s)
				}
			}
		}
		params[name] = tp
	}
	return params, required
}

func str(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// safeToolName 确保工具名符合 OpenAI 规范: ^[a-zA-Z0-9_-]+$
func safeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
