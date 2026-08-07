package shell

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"gobot/services/ai"
)

// NewShellTool 创建系统命令执行工具
func NewShellTool() ai.Tool {
	t := ai.NewTool("run_command", "在服务器上执行 shell 命令并返回输出。可执行系统运维、文件操作等命令。",
		map[string]*ai.ToolParam{
			"command": {Type: "string", Description: "要执行的 shell 命令"},
			"workdir": {Type: "string", Description: "工作目录(可选)"},
			"timeout": {Type: "integer", Description: "超时秒数(可选, 默认30)"},
		},
		[]string{"command"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			cmdStr, _ := args["command"].(string)
			if cmdStr == "" {
				return "错误: command 不能为空", nil
			}
			workdir, _ := args["workdir"].(string)
			timeout := 30
			if t, ok := args["timeout"].(float64); ok && t > 0 {
				timeout = int(t)
			}
			out, err := runShell(ctx, cmdStr, workdir, timeout)
			if err != nil {
				return out + "\n执行失败: " + err.Error(), nil
			}
			return out, nil
		})
	// shell 命令可直接执行任意系统操作, 需人工审批(主人/管理员确认)
	t.Risk = ai.RiskHigh
	return t
}

func runShell(ctx context.Context, cmdStr, workdir string, timeout int) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "/bin/bash", "-c", cmdStr)
	if workdir != "" {
		cmd.Dir = workdir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 截断输出, 避免 token 爆炸
	maxLen := 8000
	err := cmd.Run()
	out := stdout.String() + stderr.String()
	if len([]rune(out)) > maxLen {
		out = string([]rune(out)[:maxLen]) + "\n...(输出过长已截断)"
	}
	if cctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("命令执行超时(%d秒)", timeout)
	}
	if err != nil {
		return strings.TrimSpace(out), err
	}
	return strings.TrimSpace(out), nil
}
