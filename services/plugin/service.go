package plugin

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"gobot/core"
	"gobot/pluginapi"
	"gobot/services/ai"
	"gobot/store/perm"
)

// Service 将 .so 插件桥接进 core.Service, 事件先经过插件, 未消费再交给 AI
type Service struct {
	mu      sync.RWMutex // 保护 plugins: 热加载运行期替换, 事件分发运行期读
	plugins []pluginapi.Plugin
	dir     string
	loader  *Manager
	aiSvc   *ai.Service
	bot     *core.Bot
	cfg     *core.Config
	gac     core.GroupAdminClient
	permS   *perm.Store
}

func NewService(plugs []pluginapi.Plugin, aiSvc *ai.Service, bot *core.Bot, cfg *core.Config, gac core.GroupAdminClient, permS *perm.Store) *Service {
	s := &Service{aiSvc: aiSvc, bot: bot, cfg: cfg, gac: gac, permS: permS}
	s.plugins = plugs
	for _, p := range plugs {
		s.attach(p)
	}
	return s
}

// attach 初始化单个插件并注入能力/工具; 工具按插件名前缀替换进 AI 工具表(幂等, 支持热加载重复调用)
func (s *Service) attach(p pluginapi.Plugin) {
	if err := p.Init(s); err != nil {
		log.Printf("[plugin] %s 初始化失败: %v", p.Name(), err)
		return
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
	var tts []ai.Tool
	for _, t := range p.Tools() {
		tt := pluginToolToAI(p.Name(), t)
		tts = append(tts, tt)
		log.Printf("[plugin] %s 注册工具: %s", p.Name(), t.Name)
	}
	prefix := safePluginToolName(p.Name()) + "_"
	if s.aiSvc != nil {
		s.aiSvc.ReplaceToolsByPrefix(prefix, tts)
	}
	log.Printf("[plugin] 已加载: %s", p.Name())
}

// SetLoader 注入 .so 加载器(热加载用), dir 为插件目录
func (s *Service) SetLoader(m *Manager, dir string) { s.loader, s.dir = m, dir }

// ReloadFrom 从插件目录重新加载所有 .so 并原子替换插件列表。
// 注意: Go 的 plugin.Open 对同一路径有进程级缓存且 .so 无法卸载——
// 更新插件必须使用新的文件名(如 echo.v2.so), 旧版本代码会驻留内存至进程重启。
func (s *Service) ReloadFrom() (string, error) {
	if s.loader == nil {
		return "", fmt.Errorf("插件目录未配置")
	}
	plugs, err := s.loader.LoadAll()
	if err != nil {
		// 部分插件失败: 仍以加载成功的列表为准
		log.Printf("[plugin] 热加载部分失败: %v", err)
	}
	return s.swapPlugins(plugs), nil
}

// swapPlugins 原子替换插件列表, 旧插件 Close 收尾, 新插件重新 attach(Init/Setup/工具替换)。
// Close/attach 在锁外执行: 插件回调可能经桥再进本服务, 锁内做会自锁。
func (s *Service) swapPlugins(plugs []pluginapi.Plugin) string {
	s.mu.Lock()
	old := s.plugins
	s.plugins = plugs
	s.mu.Unlock()

	for _, p := range old {
		_ = p.Close()
	}
	for _, p := range plugs {
		s.attach(p)
	}
	return fmt.Sprintf("已加载 %d 个插件", len(plugs))
}

// StartWatcher 监听插件目录变化自动热加载(fsnotify), 事件防抖 1s。
// 更新插件必须换新文件名(见 ReloadFrom 注释); 删除 .so 即下线该插件。
func (s *Service) StartWatcher(ctx context.Context) {
	if s.dir == "" {
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[plugin] 目录监听不可用: %v", err)
		return
	}
	if err := w.Add(s.dir); err != nil {
		log.Printf("[plugin] 监听 %s 失败: %v", s.dir, err)
		w.Close()
		return
	}
	log.Printf("[plugin] 热加载监听已启动: %s", s.dir)
	go func() {
		defer w.Close()
		var debounce *time.Timer
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-w.Events:
				if !ok {
					return
				}
				if !strings.HasSuffix(e.Name, ".so") {
					continue
				}
				// 防抖: 编辑器/构建工具常触发连续事件
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(time.Second, func() {
					log.Printf("[plugin] 检测到 %s 变动, 热加载...", e.Name)
					if msg, err := s.ReloadFrom(); err != nil {
						log.Printf("[plugin] 热加载失败: %v", err)
					} else {
						log.Printf("[plugin] 热加载完成: %s", msg)
					}
				})
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("[plugin] 监听错误: %v", err)
			}
		}
	}()
}

func (s *Service) Name() string { return "plugin" }

// pluginReloadCmdRe 匹配 /plugin reload 管理命令
var pluginReloadCmdRe = regexp.MustCompile(`^/plugin\s+reload\s*$`)

// isManager 主人或全局管理员
func (s *Service) isManager(uid int64) bool {
	if s.bot == nil || s.bot.Cfg == nil {
		return false
	}
	id := strconv.FormatInt(uid, 10)
	if s.bot.Cfg.Bot.MasterID == id {
		return true
	}
	for _, a := range s.bot.Cfg.Bot.AdminIDs {
		if a == id {
			return true
		}
	}
	return false
}

func (s *Service) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if ev.Type != "message" {
		return false
	}
	// 插件热加载命令(主人/管理员), 在插件事件分发前拦截
	if pluginReloadCmdRe.MatchString(strings.TrimSpace(ev.Text())) {
		if !s.isManager(ev.UserID) {
			bot.Reply(ev, "仅主人/管理员可重载插件")
			return true
		}
		msg, err := s.ReloadFrom()
		if err != nil {
			bot.Reply(ev, "❌ 插件重载失败: "+err.Error())
			return true
		}
		bot.Reply(ev, "🔄 "+msg)
		return true
	}

	s.mu.RLock()
	plugins := make([]pluginapi.Plugin, len(s.plugins))
	copy(plugins, s.plugins)
	s.mu.RUnlock()

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
	for _, p := range plugins {
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
