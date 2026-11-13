package ai

import (
	"context"
	"fmt"
	"log"
	"time"

	"gobot/services/acp"
)

// SetACPClient 注入 ACP 客户端并注册工具
func (s *Service) SetACPClient(c *acp.Client) {
	if c == nil {
		return
	}
	t := NewTool("opencode_task", "调用 opencode(编程智能体)执行代码任务。当需要编写/修改代码、运行测试、排查代码问题、重构项目时使用。会创建一个隔离的 opencode 会话执行任务并返回结果。",
		map[string]*ToolParam{
			"task": {Type: "string", Description: "要 opencode 执行的任务描述(明确、具体)"},
			"cwd":  {Type: "string", Description: "工作目录(可选, 默认配置目录)"},
		},
		[]string{"task"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			task, _ := args["task"].(string)
			if task == "" {
				return "错误: task 不能为空", nil
			}
			cwd, _ := args["cwd"].(string)
			if cwd == "" {
				cwd = s.cfg.ACP.Cwd
			}
			timeout := time.Duration(s.cfg.ACP.Timeout) * time.Second
			if timeout <= 0 {
				timeout = 300 * time.Second
			}
			cctx, cancel := context.WithTimeout(ctx, timeout+30*time.Second)
			defer cancel()

			log.Printf("[acp] opencode 任务: %s (cwd=%s)", truncate(task, 60), cwd)
			reply, err := c.RunTask(cctx, cwd, task, timeout)
			if err != nil {
				return reply, fmt.Errorf("opencode 任务未正常完成: %w", err)
			}
			if reply == "" {
				return "opencode 任务完成(无文本输出)", nil
			}
			return reply, nil
		})
	t.Risk = RiskHigh
	t.Timeout = time.Duration(s.cfg.ACP.Timeout)*time.Second + 30*time.Second
	if t.Timeout <= 30*time.Second {
		t.Timeout = 330 * time.Second
	}
	s.tools = append(s.tools, t)
	log.Printf("[acp] opencode_task 工具已注册")
}
