package pluginapi

import (
	"context"
)

// Tool 插件可暴露的工具, 注册进 AI Agent
// Risk: 0=默认(中风险) 1=低 2=中 3=高 4=极危
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]*Param
	Risk        int
	Call        func(ctx context.Context, args map[string]interface{}) (string, error)
}

type Param struct {
	Type        string
	Description string
	Required    bool
	Enum        []string
}

type Event struct {
	Type       string
	DetailType string
	GroupID    int64
	UserID     int64
	SelfID     int64
	MessageID  int64
	RawMessage string
	Message    string
	IsGroup    bool
}

// Handler 插件的事件处理回调
type Handler func(ctx context.Context, ev *Event) (reply string, handled bool)

// Sender 插件可用的发送能力
type Sender interface {
	SendGroupMsg(groupID int64, text string) error
	SendPrivateMsg(userID int64, text string) error
}

// GroupAdmin 群管理能力(主程序桥接到 OneBot API)
type GroupAdmin interface {
	SetGroupBan(groupID, userID int64, duration int) error
	SetGroupWholeBan(groupID int64, enable bool) error
	SetGroupKick(groupID, userID int64, rejectAddRequest bool) error
	SetGroupAdmin(groupID, userID int64, enable bool) error
	SetGroupCard(groupID, userID int64, card string) error
	SetGroupName(groupID int64, name string) error
	SetGroupLeave(groupID int64, isDismiss bool) error
	SendGroupNotice(groupID int64, content string) error
	GetGroupMemberList(groupID int64) ([]map[string]interface{}, error)
	GetGroupMemberInfo(groupID, userID int64) (map[string]interface{}, error)
}

// Perm 权限查询能力(供插件做授权判断)
type Perm interface {
	IsMaster(userID int64) bool
	IsGroupAdmin(groupID, userID int64) bool
}

// Capabilities 插件在 Init 时可获取的完整能力集合
type Capabilities struct {
	Sender     Sender
	GroupAdmin GroupAdmin
	Perm       Perm
}

// Plugin 插件需实现的接口, .so 通过符号 "Plugin" 导出
type Plugin interface {
	Name() string
	Init(sender Sender) error
	OnEvent(ctx context.Context, ev *Event) (string, bool)
	Tools() []Tool
	Close() error
}

// CapablePlugin 可选接口: 插件可实现以获取群管理/权限能力
type CapablePlugin interface {
	Setup(caps *Capabilities) error
}

// Base 提供可内嵌的默认实现
type Base struct{}

func (b *Base) Init(sender Sender) error { return nil }
func (b *Base) OnEvent(ctx context.Context, ev *Event) (string, bool) {
	return "", false
}
func (b *Base) Tools() []Tool { return nil }
func (b *Base) Close() error  { return nil }
