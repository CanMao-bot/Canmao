package plugin

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"gobot/core"
	"gobot/pluginapi"
	"gobot/services/ai"
	"gobot/store/perm"
)

// Service 将 .so 插件桥接进 core.Service, 事件先经过插件, 未消费再交给 AI
type Service struct {
	plugins []pluginapi.Plugin
	aiSvc   *ai.Service
	bot     *core.Bot
	cfg     *core.Config
	gac     core.GroupAdminClient
	permS   *perm.Store
}

func NewService(plugs []pluginapi.Plugin, aiSvc *ai.Service, bot *core.Bot, cfg *core.Config, gac core.GroupAdminClient, permS *perm.Store) *Service {
	s := &Service{plugins: plugs, aiSvc: aiSvc, bot: bot, cfg: cfg, gac: gac, permS: permS}
	for _, p := range plugs {
		if err := p.Init(s); err != nil {
			log.Printf("[plugin] %s 初始化失败: %v", p.Name(), err)
			continue
		}
		// 可选能力注入
		if cp, ok := p.(pluginapi.CapablePlugin); ok {
			caps := &pluginapi.Capabilities{
				Sender:     s,
				GroupAdmin: s,
				Perm:       s,
			}
			if err := cp.Setup(caps); err != nil {
				log.Printf("[plugin] %s Setup 失败: %v", p.Name(), err)
			}
		}
		for _, t := range p.Tools() {
			tt := pluginToolToAI(p.Name(), t)
			aiSvc.AddTool(tt)
			log.Printf("[plugin] %s 注册工具: %s", p.Name(), t.Name)
		}
		log.Printf("[plugin] 已加载: %s", p.Name())
	}
	return s
}

func (s *Service) Name() string { return "plugin" }

func (s *Service) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	pev := &pluginapi.Event{
		Type:       ev.Type,
		DetailType: ev.DetailType,
		GroupID:    ev.GroupID,
		UserID:     ev.UserID,
		SelfID:     ev.SelfID,
		MessageID:  ev.MessageID,
		RawMessage: ev.RawMessage,
		Message:    ev.Text(),
		IsGroup:    ev.IsGroup(),
	}
	for _, p := range s.plugins {
		reply, handled := p.OnEvent(ctx, pev)
		if handled {
			if reply != "" {
				bot.Reply(ev, reply)
			}
			return true
		}
	}
	return false
}

// pluginapi.Sender 实现
func (s *Service) SendGroupMsg(groupID int64, text string) error {
	return s.bot.Sender.SendGroupMsg(groupID, 0, []core.Segment{core.TextSegment(text)})
}

func (s *Service) SendPrivateMsg(userID int64, text string) error {
	return s.bot.Sender.SendPrivateMsg(userID, []core.Segment{core.TextSegment(text)})
}

// ---- pluginapi.GroupAdmin 桥接 ----

func (s *Service) SetGroupBan(groupID, userID int64, duration int) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupBan(groupID, userID, duration)
}
func (s *Service) SetGroupWholeBan(groupID int64, enable bool) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupWholeBan(groupID, enable)
}
func (s *Service) SetGroupKick(groupID, userID int64, rejectAddRequest bool) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupKick(groupID, userID, rejectAddRequest)
}
func (s *Service) SetGroupAdmin(groupID, userID int64, enable bool) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupAdmin(groupID, userID, enable)
}
func (s *Service) SetGroupCard(groupID, userID int64, card string) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupCard(groupID, userID, card)
}
func (s *Service) SetGroupName(groupID int64, name string) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupName(groupID, name)
}
func (s *Service) SetGroupLeave(groupID int64, isDismiss bool) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SetGroupLeave(groupID, isDismiss)
}
func (s *Service) SendGroupNotice(groupID int64, content string) error {
	if s.gac == nil {
		return fmt.Errorf("群管理能力不可用")
	}
	return s.gac.SendGroupNotice(groupID, content)
}
func (s *Service) GetGroupMemberList(groupID int64) ([]map[string]interface{}, error) {
	if s.gac == nil {
		return nil, fmt.Errorf("群管理能力不可用")
	}
	return s.gac.GetGroupMemberList(groupID)
}
func (s *Service) GetGroupMemberInfo(groupID, userID int64) (map[string]interface{}, error) {
	if s.gac == nil {
		return nil, fmt.Errorf("群管理能力不可用")
	}
	return s.gac.GetGroupMemberInfo(groupID, userID)
}

// ---- pluginapi.Perm 桥接 ----

func (s *Service) IsMaster(userID int64) bool {
	if s.bot == nil || s.bot.Cfg == nil {
		return false
	}
	return s.bot.Cfg.Bot.MasterID == strconv.FormatInt(userID, 10)
}

func (s *Service) IsGroupAdmin(groupID, userID int64) bool {
	if s.permS == nil {
		return false
	}
	role, err := s.permS.GetGroupRole(groupID, userID)
	return err == nil && role == "admin"
}

func pluginToolToAI(pluginName string, t pluginapi.Tool) ai.Tool {
	params := map[string]*ai.ToolParam{}
	var required []string
	for name, p := range t.Parameters {
		tp := &ai.ToolParam{
			Type:        p.Type,
			Description: p.Description,
			Enum:        p.Enum,
		}
		if p.Required {
			required = append(required, name)
		}
		params[name] = tp
	}
	tool := ai.NewTool(safePluginToolName(pluginName)+"_"+safePluginToolName(t.Name), t.Description, params, required,
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			return t.Call(ctx, args)
		})
	// 插件声明风险则采用; 未声明按中风险(需发起人确认)
	switch t.Risk {
	case 1:
		tool.Risk = ai.RiskLow
	case 3:
		tool.Risk = ai.RiskHigh
	case 4:
		tool.Risk = ai.RiskCritical
	default:
		tool.Risk = ai.RiskMedium
	}
	return tool
}

// safePluginToolName 确保工具名符合 OpenAI 规范
func safePluginToolName(name string) string {
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

func idStr(id int64) string { return strconv.FormatInt(id, 10) }
