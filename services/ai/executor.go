package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gobot/core"
	"gobot/store/session"
)

type runContextKey struct{}
type toolSetKey struct{}

func (s *Service) requestTools(ctx context.Context) []Tool {
	if tools, ok := ctx.Value(toolSetKey{}).([]Tool); ok {
		return s.withResidentTools(tools)
	}
	return s.toolsSnapshot()
}

func (s *Service) runTrace(id int64) string {
	steps, err := s.session.Steps(id)
	if err != nil {
		log.Printf("[ai] 读取执行记录: %v", err)
		return "[执行记录读取失败，请勿盲目重试]"
	}
	if len(steps) == 0 {
		return ""
	}
	trace := "[本轮工具执行记录]\n"
	for _, step := range steps {
		trace += fmt.Sprintf("%s [%s]: %s\n", step.Name, step.Status, truncateRunes(step.Result, 500))
	}
	return truncateRunes(trace, 4000)
}

// 风险下限在执行边界硬编码，不能被插件元数据或提示词降低。
func riskForTool(t Tool) RiskLevel {
	risk := t.Risk
	if risk == RiskUnspecified {
		risk = RiskMedium
	}
	if risk < RiskLow || risk > RiskCritical {
		risk = RiskCritical
	}
	switch t.Function.Name {
	case "group_ban", "group_kick", "group_set_card", "group_set_name", "run_command", "opencode_task":
		if risk < RiskHigh {
			risk = RiskHigh
		}
	case "write_file":
		risk = RiskCritical
	}
	return risk
}

func riskForToolCall(t Tool, args map[string]interface{}) (RiskLevel, string) {
	risk, key := riskForTool(t), t.Function.Name
	if key == "qq_call_api" {
		action, _, err := qqCallArgs(args)
		if err != nil {
			return RiskCritical, "qq_call_api/invalid"
		}
		if floor := riskForQQAction(action); risk < floor {
			risk = floor
		}
		key += "/" + action
	}
	return risk, key
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(url string) (interface{}, error) {
	return nil, fmt.Errorf("工具 Schema 不允许外部引用: %s", url)
}

func validateArguments(t Tool, raw string) (map[string]interface{}, error) {
	var args map[string]interface{}
	decode := json.Unmarshal
	if t.Function.Name == "qq_call_api" || isQQOutputTool(t.Function.Name) {
		decode = decodeQQJSON
	}
	if err := decode([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("参数必须是 JSON 对象: %w", err)
	}
	if args == nil {
		return nil, fmt.Errorf("参数必须是 JSON 对象，不能为 null")
	}
	data, err := json.Marshal(t.Function.Parameters)
	if err != nil {
		return nil, err
	}
	var schema interface{}
	if err = json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(localSchemaLoader{})
	const uri = "urn:canmao:tool-arguments"
	if err = c.AddResource(uri, schema); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(uri)
	if err != nil {
		return nil, fmt.Errorf("工具参数 Schema 无效: %w", err)
	}
	if err = compiled.Validate(args); err != nil {
		return nil, err
	}
	return args, nil
}

func (s *Service) authorizeTool(ctx context.Context, t Tool, args map[string]interface{}) error {
	if err := s.checkRunGroupAI(ctx); err != nil {
		return err
	}
	ev := core.EventFrom(ctx)
	if ev == nil || ev.UserID <= 0 {
		return errNoEvent
	}
	privileged := s.bot.Cfg.Bot.MasterID == fmtID(ev.UserID) || isAdmin(s.bot, ev.UserID)
	if gid := int64From(args["group_id"]); gid > 0 && (!ev.IsGroup() || gid != ev.GroupID) && !privileged {
		return errNoPermission("不能跨会话访问其他群")
	}
	if t.Function.Name == "qq_call_api" {
		if err := s.authorizeQQCall(ctx, args); err != nil {
			return err
		}
	}
	if isQQOutputTool(t.Function.Name) {
		if err := s.authorizeQQMessage(ctx, args); err != nil {
			return err
		}
	}
	if t.Authorize != nil {
		return t.Authorize(ctx, args)
	}
	return nil
}

func (s *Service) executeTool(ctx context.Context, tc ToolCall) (result string) {
	status := "failed"
	var step *session.Step
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("工具执行异常，已隔离: %v", r)
			status = "failed"
			log.Printf("[ai] 工具 %s panic: %v", tc.Function.Name, r)
		}
		if step != nil {
			if err := s.session.UpdateStep(step.ID, status, result); err != nil {
				log.Printf("[ai] 保存工具结果失败: %v", err)
				result += "\n执行记录保存失败，请勿自动重试该操作"
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		return "请求已取消: " + err.Error()
	}
	var selected *Tool
	for _, t := range s.requestTools(ctx) {
		if t.Function.Name == tc.Function.Name {
			copy := t
			selected = &copy
			break
		}
	}
	if selected == nil {
		return "未知工具: " + tc.Function.Name
	}
	t := *selected
	if t.Callback == nil {
		return "工具已注册但无回调"
	}
	args, err := validateArguments(t, tc.Function.Arguments)
	if err != nil {
		return "工具参数无效: " + err.Error()
	}
	if err = s.authorizeTool(ctx, t, args); err != nil {
		return "无权执行工具: " + err.Error()
	}
	if run, ok := ctx.Value(runContextKey{}).(*session.Run); ok {
		if tc.ID == "" {
			return "工具调用缺少 ID"
		}
		var existing bool
		step, existing, err = s.session.BeginStep(run.ID, tc.ID, tc.Function.Name, tc.Function.Arguments)
		if err != nil {
			return "记录工具调用失败: " + err.Error()
		}
		if existing {
			old := step
			step = nil
			if old.Status == "succeeded" || old.Status == "failed" || old.Status == "rejected" {
				return old.Result
			}
			return "该调用已开始但结果尚不确定，不能重复执行"
		}
	}
	update := func(state string) error {
		if step == nil {
			return nil
		}
		return s.session.UpdateStep(step.ID, state, "")
	}
	ev := core.EventFrom(ctx)
	risk, approvalKey := riskForToolCall(t, args)
	if isQQOutputTool(t.Function.Name) {
		target, _ := qqMessageTargetFor(ev, args) // Already validated by authorizeTool.
		if !target.isCurrent(ev) {
			if risk < RiskMedium {
				risk = RiskMedium
			}
			if target.groupID > 0 {
				approvalKey += "/group/" + fmtID(target.groupID)
			} else {
				approvalKey += "/private/" + fmtID(target.userID)
			}
		}
	}
	if risk > RiskLow && !s.approvals.IsAllowedFor(ev, approvalKey, risk) {
		if err = update("awaiting_approval"); err != nil {
			return "审批状态保存失败: " + err.Error()
		}
		res, err := s.approvals.Request(ctx, ev, approvalKey, args, risk)
		if err != nil {
			return "审批出错: " + err.Error()
		}
		if !res.Approved {
			status = "rejected"
			return "操作被拒绝: " + res.Reason
		}
	}
	if err = ctx.Err(); err != nil {
		return "请求已取消: " + err.Error()
	}
	if err = s.authorizeTool(ctx, t, args); err != nil {
		status = "rejected"
		return "操作已取消: " + err.Error()
	}
	if err = update("executing"); err != nil {
		return "执行状态保存失败: " + err.Error()
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	defer func() {
		log.Printf("[ai] tool_timing name=%s elapsed_ms=%d status=%s", t.Function.Name, time.Since(started).Milliseconds(), status)
	}()
	result, err = t.Callback(toolCtx, args)
	if err != nil {
		if result != "" {
			return "工具执行失败: " + err.Error() + "\n已返回的部分结果:\n" + result
		}
		return "工具执行失败: " + err.Error()
	}
	if toolCtx.Err() != nil {
		return "工具执行中断，结果可能已产生，请勿自动重试: " + toolCtx.Err().Error()
	}
	status = "succeeded"
	return result
}
