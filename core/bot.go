package core

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type Sender interface {
	SendGroupMsg(groupID, userID int64, msg []Segment) error
	SendPrivateMsg(userID int64, msg []Segment) error
}

// ReceiptSender returns the real message ID for workflows resolved by quoting
// a bot message. Implementations must not synthesize IDs or guess the recipient.
type ReceiptSender interface {
	SendMessageWithReceipt(ctx context.Context, groupID, userID int64, msg []Segment) (int64, error)
}

func (b *Bot) SendTracked(ctx context.Context, ev *Event, text string) (int64, error) {
	sender, ok := b.Sender.(ReceiptSender)
	if !ok {
		return 0, errors.New("当前连接不支持消息回执，无法创建可引用的申请")
	}
	gid := int64(0)
	if ev.IsGroup() {
		gid = ev.GroupID
	}
	id, err := sender.SendMessageWithReceipt(ctx, gid, ev.UserID, []Segment{TextSegment(text)})
	if err == nil && id == 0 {
		return 0, errors.New("发送申请未取得有效消息回执")
	}
	return id, err
}

// QQAPIClient exposes native OneBot actions. Stream callbacks receive intermediate
// data frames; the returned data is always the final API response.
type QQAPIClient interface {
	CallQQAPI(ctx context.Context, action string, params map[string]interface{}, onStream func(json.RawMessage) error) (json.RawMessage, error)
}

// FileClient 文件/图片交互能力(由 adapter 实现)
type FileClient interface {
	GetImage(file string) (map[string]interface{}, error)
	GetFile(file string) (map[string]interface{}, error)
	DownloadFile(url, threadCount string) (map[string]interface{}, error)
	UploadGroupFile(groupID int64, file, name string) error
	UploadPrivateFile(userID int64, file, name string) error
	GetGroupRootFiles(groupID int64) (map[string]interface{}, error)
	GetGroupFilesByFolder(groupID, folderID int64) (map[string]interface{}, error)
}

// ForwardSender 合并转发能力(由 adapter 实现)
type ForwardSender interface {
	SendForwardMsg(groupID, userID int64, nodes []ForwardNode) error
}

// ForwardReceiptSender supports cancellation and returns the actual message ID.
type ForwardReceiptSender interface {
	SendForwardWithReceipt(ctx context.Context, groupID, userID int64, nodes []ForwardNode) (int64, error)
}

// MessageInfo 消息详情(get_msg)
type MessageInfo struct {
	MessageID  int64
	GroupID    int64
	UserID     int64
	Nickname   string
	Time       int64
	Message    []Segment
	RawMessage string
}

// MessageGetter 按消息 ID 获取消息(由 adapter 实现)
type MessageGetter interface {
	GetMsg(messageID int64) (*MessageInfo, error)
}

// GetMessage 按消息 ID 获取消息详情(Sender 需实现 MessageGetter)
func (b *Bot) GetMessage(messageID int64) (*MessageInfo, error) {
	g, ok := b.Sender.(MessageGetter)
	if !ok {
		return nil, errors.New("当前连接不支持 get_msg")
	}
	return g.GetMsg(messageID)
}

// GroupInfoGetter 获取群信息能力(由 adapter 实现)
type GroupInfoGetter interface {
	GetGroupInfo(groupID int64) (map[string]interface{}, error)
}

// GetGroupName 获取群名称(Sender 需实现 GroupInfoGetter), 失败返回空串
func (b *Bot) GetGroupName(groupID int64) string {
	g, ok := b.Sender.(GroupInfoGetter)
	if !ok {
		return ""
	}
	info, err := g.GetGroupInfo(groupID)
	if err != nil {
		return ""
	}
	name, _ := info["group_name"].(string)
	return name
}

// GroupAdminClient 群管理能力(由 adapter 实现), 供插件桥接
type GroupAdminClient interface {
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

// GroupRequestClient 入群申请处理能力(由 adapter 实现)
type GroupRequestClient interface {
	SetGroupAddRequest(flag string, approve bool, reason string) error
	GetStrangerInfo(userID int64) (map[string]interface{}, error)
}

type Service interface {
	Name() string
	Handle(ctx context.Context, bot *Bot, ev *Event) bool
}

// ControlService 只处理唤醒运行中任务的短消息，不占普通任务的执行名额。
type ControlService interface {
	HandleControl(ctx context.Context, bot *Bot, ev *Event) bool
}

// EventPolicy applies to all services, including the fast approval/ask control path.
// Implementations must not perform network requests on this ingress path.
type EventPolicy interface{ AllowEvent(ev *Event) bool }

type Bot struct {
	Cfg      *Config
	Sender   Sender
	Forward  ForwardSender
	services []Service
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
	// dispatch 背压: 限制并发处理的事件数, 防止消息洪峰(刷屏/被拉群)无限起 goroutine 打爆 LLM API
	dispatchSem  chan struct{}
	ingress      chan *Event
	dispatchOnce sync.Once
	stopped      bool
	routers      sync.WaitGroup
	workers      sync.WaitGroup
	background   sync.WaitGroup
}

// maxDispatchConcurrency 全局并发事件处理上限。
// 审批等待等长阻塞也会占用名额, 容量需留足余量(正常聊天很难同时有几十个在途事件)。
const maxDispatchConcurrency = 64

func NewBot(cfg *Config) *Bot {
	ctx, cancel := context.WithCancel(context.Background())
	return &Bot{Cfg: cfg, ctx: ctx, cancel: cancel, dispatchSem: make(chan struct{}, maxDispatchConcurrency), ingress: make(chan *Event, 1024)}
}

func (b *Bot) SetSender(s Sender) {
	b.Sender = s
	if fs, ok := s.(ForwardSender); ok {
		b.Forward = fs
	}
}

func (b *Bot) RegisterService(s Service) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.services = append(b.services, s)
	log.Printf("[core] 已注册服务: %s", s.Name())
}

func (b *Bot) Run() {}

func (b *Bot) Shutdown() { b.mu.Lock(); b.stopped = true; b.cancel(); b.mu.Unlock() }

// GoBackground registers a bounded service worker in the bot's shutdown lifecycle.
// The caller owns queue/concurrency limits; no new worker starts after Shutdown.
func (b *Bot) GoBackground(fn func(context.Context)) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return false
	}
	b.background.Add(1)
	go func() {
		defer b.background.Done()
		fn(b.ctx)
	}()
	return true
}

func (b *Bot) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { b.routers.Wait(); b.workers.Wait(); b.background.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Bot) Dispatch(ev *Event) {
	if ev == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.stopped {
		return
	}
	b.dispatchOnce.Do(func() {
		for i := 0; i < 4; i++ {
			b.routers.Add(1)
			go func() { defer b.routers.Done(); b.routeEvents() }()
		}
	})
	// 接收路径绝不等待执行名额，也不做网络请求。
	select {
	case <-b.ctx.Done():
	case b.ingress <- ev:
	default:
		log.Printf("[core] 接收队列已满，丢弃事件 id=%d", ev.MessageID)
	}
}

func (b *Bot) serviceSnapshot() []Service {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]Service(nil), b.services...)
}

func (b *Bot) routeEvents() {
	for {
		select {
		case <-b.ctx.Done():
			return
		case ev := <-b.ingress:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[core] 控制消息处理异常: %v", r)
					}
				}()
				ctx := WithEvent(b.ctx, ev)
				if ctx.Err() != nil {
					return
				}
				for _, svc := range b.serviceSnapshot() {
					if policy, ok := svc.(EventPolicy); ok && !policy.AllowEvent(ev) {
						return
					}
				}
				for _, svc := range b.serviceSnapshot() {
					if control, ok := svc.(ControlService); ok && control.HandleControl(ctx, b, ev) {
						return
					}
				}
				select {
				case b.dispatchSem <- struct{}{}:
					b.workers.Add(1)
					go func() {
						defer b.workers.Done()
						defer func() {
							<-b.dispatchSem
							if r := recover(); r != nil {
								log.Printf("[core] 事件处理异常: %v", r)
							}
						}()
						b.dispatch(ev)
					}()
				default:
					log.Printf("[core] 执行任务已满，丢弃普通事件 id=%d", ev.MessageID)
				}
			}()
		}
	}
}

func (b *Bot) dispatch(ev *Event) {
	b.mu.RLock()
	svcs := make([]Service, len(b.services))
	copy(svcs, b.services)
	b.mu.RUnlock()

	ctx := WithEvent(b.ctx, ev)
	for _, svc := range svcs {
		if svc.Handle(ctx, b, ev) {
			return
		}
	}
}

func (b *Bot) Reply(ev *Event, text string) {
	// 长消息用合并转发发送
	if b.Cfg.Bot.LongMessageForward && len([]rune(text)) > b.Cfg.Bot.LongMessageThreshold && b.Forward != nil {
		// 拆分为多条节点, 每条不超过 forwardChunkSize (2500) 字符
		nodes := splitForwardNodes(text, b.Cfg.Bot.Name, ev.UserID)
		var err error
		if ev.IsGroup() {
			err = b.Forward.SendForwardMsg(ev.GroupID, ev.UserID, nodes)
		} else {
			err = b.Forward.SendForwardMsg(0, ev.UserID, nodes)
		}
		if err == nil {
			return
		}
		log.Printf("[core] 合并转发失败, 回退普通发送: %v", err)
	}

	msg := []Segment{TextSegment(text)}
	var err error
	if ev.IsGroup() {
		// 群聊: 把 [@QQ号] / [@全体成员] 标记转成 at 消息段
		if FeatureOn(b.Cfg.Features.AtSend) {
			if segs := parseAtSegments(text); len(segs) > 0 {
				msg = segs
			}
		}
		err = b.Sender.SendGroupMsg(ev.GroupID, ev.UserID, msg)
	} else {
		err = b.Sender.SendPrivateMsg(ev.UserID, msg)
	}
	if err != nil {
		log.Printf("[core] 发送消息失败: %v", err)
	}
}

// atMarkRe 匹配 [@QQ号] / [@全体成员] 标记
var atMarkRe = regexp.MustCompile(`\[@(\d+|全体成员)\]`)

// parseAtSegments 将文本中的 [@QQ号] / [@全体成员] 标记切分为 text 段和 at 段
func parseAtSegments(text string) []Segment {
	locs := atMarkRe.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	segs := make([]Segment, 0, len(locs)*2+1)
	last := 0
	for _, loc := range locs {
		if pre := text[last:loc[0]]; pre != "" {
			segs = append(segs, TextSegment(pre))
		}
		mark := text[loc[2]:loc[3]]
		if mark == "全体成员" {
			segs = append(segs, Segment{Type: "at", Data: map[string]interface{}{"qq": "all"}})
		} else if qq, err := strconv.ParseInt(mark, 10, 64); err == nil {
			segs = append(segs, AtSegment(qq))
		} else {
			segs = append(segs, TextSegment(text[loc[0]:loc[1]]))
		}
		last = loc[1]
	}
	if tail := text[last:]; tail != "" {
		segs = append(segs, TextSegment(tail))
	}
	return segs
}

// forwardChunkSize 合并转发单节点最大字符数 (QQ 单条消息上限约 2500)
const forwardChunkSize = 2500

// splitForwardNodes 将长文本按内容分段成合并转发节点(类似聊天记录),
// 优先按空行段落分, 段落过长再按行/字符切
func splitForwardNodes(text, botName string, userID int64) []ForwardNode {
	paragraphs := splitParagraphs(text)
	var chunks []string
	for _, p := range paragraphs {
		if len([]rune(p)) <= forwardChunkSize {
			chunks = append(chunks, p)
			continue
		}
		// 段落过长: 按行再分
		lines := strings.Split(p, "\n")
		cur := ""
		for _, line := range lines {
			if len([]rune(cur))+len([]rune(line))+1 > forwardChunkSize && cur != "" {
				chunks = append(chunks, cur)
				cur = line
			} else {
				if cur != "" {
					cur += "\n"
				}
				cur += line
			}
		}
		if cur != "" {
			chunks = append(chunks, cur)
		}
	}

	nodes := make([]ForwardNode, 0, len(chunks))
	for _, c := range chunks {
		nodes = append(nodes, ForwardNode{
			Name:     botName,
			Nickname: botName,
			Uin:      "",
			Content:  []Segment{TextSegment(c)},
		})
	}
	return nodes
}

// splitParagraphs 按空行将文本切分为段落, 保留空行结构
func splitParagraphs(text string) []string {
	blocks := strings.Split(text, "\n\n")
	// 合并过小段落(避免产生大量碎片节点)
	var out []string
	for _, b := range blocks {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		out = append(out, b)
	}
	// 若整体不超长且段数不多, 直接作为一个节点
	if len(out) == 1 || len([]rune(text)) <= forwardChunkSize {
		return out
	}
	return out
}

func (b *Bot) IsCommand(ev *Event, name string) bool {
	t := strings.TrimSpace(ev.Text())
	prefix := b.Cfg.Bot.Prefix
	return strings.HasPrefix(t, prefix+name)
}

func (b *Bot) CommandArgs(ev *Event, name string) string {
	t := strings.TrimSpace(ev.Text())
	prefix := b.Cfg.Bot.Prefix
	if !strings.HasPrefix(t, prefix+name) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(t, prefix+name))
}
