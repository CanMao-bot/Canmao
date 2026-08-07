package ai

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gobot/core"
	"gobot/store/sched"
)

// registerSchedTools 注册定时任务工具
func (s *Service) registerSchedTools() {
	t := NewTool("schedule_task", "设置一个定时任务, 到时间自动发送消息提醒。支持一次性或重复(每隔N秒/分钟/小时)。例如: 10分钟后提醒我喝水, 每天上午9点发早安。",
		map[string]*ToolParam{
			"content":   {Type: "string", Description: "定时要发送的内容"},
			"delay_sec": {Type: "integer", Description: "多少秒后触发(一次性任务, 与 repeat_sec 二选一)"},
			"repeat_sec": {Type: "integer", Description: "重复间隔秒数(重复任务, 与 delay_sec 二选一)"},
			"target":    {Type: "string", Description: "目标: group=发到当前群 / private=发到当前私聊(可选, 默认当前会话)"},
		},
		[]string{"content"},
		s.scheduleCallback,
	)
	t.Risk = RiskLow
	s.tools = append(s.tools, t)

	// 查看/取消定时任务
	listT := NewTool("list_tasks", "查看当前会话的定时任务列表。",
		map[string]*ToolParam{}, nil, s.listTasksCallback)
	listT.Risk = RiskLow
	s.tools = append(s.tools, listT)

	cancelT := NewTool("cancel_task", "取消一个定时任务。",
		map[string]*ToolParam{
			"task_id": {Type: "integer", Description: "任务ID(用 list_tasks 查看)"},
		},
		[]string{"task_id"},
		s.cancelTaskCallback)
	cancelT.Risk = RiskLow
	s.tools = append(s.tools, cancelT)
}

func (s *Service) scheduleCallback(ctx context.Context, args map[string]interface{}) (string, error) {
	ev, ok := ctx.Value("event").(*core.Event)
	if !ok || ev == nil || s.sched == nil {
		return "定时功能不可用", nil
	}
	content, _ := args["content"].(string)
	if content == "" {
		return "错误: content 不能为空", nil
	}
	delay, _ := args["delay_sec"].(float64)
	repeat, _ := args["repeat_sec"].(float64)

	scope, gid, uid := s.sessionScope(ev)
	now := time.Now().Unix()
	t := &sched.Task{
		Type:      "once",
		Content:   content,
		Scope:     scope,
		GroupID:   gid,
		UserID:    uid,
		TargetAt:  now + int64(delay),
		Enabled:   true,
		CreatedAt: now,
	}
	if repeat > 0 {
		t.Type = "repeat"
		t.Interval = int64(repeat)
		t.TargetAt = now + int64(repeat)
	}
	if t.TargetAt <= now {
		t.TargetAt = now + 10 // 至少10秒后
	}
	id, err := s.sched.Add(t)
	if err != nil {
		return "创建定时任务失败: " + err.Error(), nil
	}
	when := sched.FormatTime(t.TargetAt)
	if t.Type == "repeat" {
		return fmt.Sprintf("✅ 已设置重复任务#%d (每%d秒), 首次 %s, 内容: %s", id, t.Interval, when, content), nil
	}
	return fmt.Sprintf("✅ 已设置定时任务#%d, %s 触发, 内容: %s", id, when, content), nil
}

func (s *Service) listTasksCallback(ctx context.Context, args map[string]interface{}) (string, error) {
	ev, ok := ctx.Value("event").(*core.Event)
	if !ok || ev == nil || s.sched == nil {
		return "定时功能不可用", nil
	}
	scope, gid, uid := s.sessionScope(ev)
	list, err := s.sched.List(scope, gid, uid)
	if err != nil {
		return "查询失败: " + err.Error(), nil
	}
	if len(list) == 0 {
		return "当前没有定时任务", nil
	}
	var b strings.Builder
	b.WriteString("⏰ 定时任务:\n")
	for _, t := range list {
		status := "已停用"
		if t.Enabled {
			status = "启用"
		}
		kind := "一次性"
		if t.Type == "repeat" {
			kind = fmt.Sprintf("重复(每%d秒)", t.Interval)
		}
		b.WriteString(fmt.Sprintf("#%d [%s] %s | %s | %s\n   内容: %s\n", t.ID, status, kind, sched.FormatTime(t.TargetAt), scopeLabel(t.Scope, t.GroupID), truncate(t.Content, 40)))
	}
	return b.String(), nil
}

func (s *Service) cancelTaskCallback(ctx context.Context, args map[string]interface{}) (string, error) {
	if s.sched == nil {
		return "定时功能不可用", nil
	}
	id, _ := args["task_id"].(float64)
	if id <= 0 {
		return "错误: 请提供 task_id", nil
	}
	if err := s.sched.Disable(int64(id)); err != nil {
		return "取消失败: " + err.Error(), nil
	}
	return "✅ 已取消定时任务 #" + strconv.FormatInt(int64(id), 10), nil
}

func scopeLabel(scope string, gid int64) string {
	if scope == "group" {
		return "群" + strconv.FormatInt(gid, 10)
	}
	return "私聊"
}

// handleSchedCmd 手动管理定时任务: /sched list /sched cancel <id>
func (s *Service) handleSchedCmd(ctx context.Context, bot *core.Bot, ev *core.Event, arg string, isMaster bool) bool {
	if s.sched == nil {
		bot.Reply(ev, "定时功能未启用")
		return true
	}
	sub, subArg := cmdOf(arg)
	switch sub {
	case "", "list":
		scope, gid, uid := s.sessionScope(ev)
		list, err := s.sched.List(scope, gid, uid)
		if err != nil {
			bot.Reply(ev, "查询失败: "+err.Error())
			return true
		}
		if len(list) == 0 {
			bot.Reply(ev, "当前没有定时任务")
			return true
		}
		var b strings.Builder
		b.WriteString("⏰ 定时任务:\n")
		for _, t := range list {
			status := "已停用"
			if t.Enabled {
				status = "启用"
			}
			kind := "一次性"
			if t.Type == "repeat" {
				kind = fmt.Sprintf("重复(每%d秒)", t.Interval)
			}
			b.WriteString(fmt.Sprintf("#%d [%s] %s | %s\n   内容: %s\n", t.ID, status, kind, sched.FormatTime(t.TargetAt), truncate(t.Content, 40)))
		}
		b.WriteString("\n/cancel <任务ID> 取消")
		bot.Reply(ev, b.String())
		return true
	case "cancel", "del":
		id, err := strconv.ParseInt(strings.TrimSpace(subArg), 10, 64)
		if err != nil {
			bot.Reply(ev, "用法: /sched cancel <任务ID>")
			return true
		}
		if err := s.sched.Disable(id); err != nil {
			bot.Reply(ev, "取消失败: "+err.Error())
			return true
		}
		bot.Reply(ev, "✅ 已取消定时任务 #"+strconv.FormatInt(id, 10))
		return true
	default:
		bot.Reply(ev, "用法: /sched list 查看 /sched cancel <任务ID> 取消")
		return true
	}
}
