package ai

import (
	"context"
	"fmt"
	"log"
	"time"

	"gobot/services/acp"
)

// acpClient 由 main 注入
var acpClient *acp.Client

// SetACPClient 注入 ACP 客户端并注册工具
func (s *Service) SetACPClient(c *acp.Client) {
	acpClient = c
	if c == nil {
		return
	}
	t := NewTool("opencode_task", "调用 opencode(编程智能体)执行代码任务。当需要编写/修改代码、运行测试、排查代码问题、重构项目时使用。会创建一个隔离的 opencode 会话执行任务并返回结果。",
		map[string]*ToolParam{
			"task":  {Type: "string", Description: "要 opencode 执行的任务描述(明确、具体)"},
			"cwd":   {Type: "string", Description: "工作目录(可选, 默认配置目录)"},
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
			reply, err := acpClient.RunTask(cctx, cwd, task, timeout)
			if err != nil {
				if reply != "" {
					return "opencode 部分完成: " + reply + "\n(错误: " + err.Error() + ")", nil
				}
				return "opencode 任务失败: " + err.Error(), nil
			}
			if reply == "" {
				return "opencode 任务完成(无文本输出)", nil
			}
			return reply, nil
		})
	t.Risk = RiskHigh
	s.tools = append(s.tools, t)
	log.Printf("[acp] opencode_task 工具已注册")
}

var _ = fmt.Sprintf
