package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"gobot/core"
)

type OneBot struct {
	wsURL     string
	token     string
	conn      *websocket.Conn
	mu        sync.Mutex
	echoChans map[string]chan json.RawMessage
	onEvent   func(ev *core.Event)
	closed    bool
}

func NewOneBot(wsURL, token string) *OneBot {
	return &OneBot{
		wsURL:     wsURL,
		token:     token,
		echoChans: make(map[string]chan json.RawMessage),
	}
}

func (o *OneBot) SetEventHandler(fn func(ev *core.Event)) { o.onEvent = fn }

func (o *OneBot) dial() (*websocket.Conn, error) {
	hdr := map[string][]string{}
	if o.token != "" {
		hdr["Authorization"] = []string{"Bearer " + o.token}
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.Dial(o.wsURL, hdr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", o.wsURL, err)
	}
	return conn, nil
}

func (o *OneBot) Connect() error {
	conn, err := o.dial()
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.conn = conn
	o.mu.Unlock()
	log.Printf("[adapter] 已连接 OneBot WS: %s", o.wsURL)
	go o.readLoop()
	return nil
}

// setConn 替换当前连接(重连时调用), 并作废所有挂起的 API 调用
func (o *OneBot) setConn(conn *websocket.Conn) {
	o.mu.Lock()
	o.conn = conn
	// 作废旧连接上挂起的 API 调用: close 让其立即失败, 而不是等 30s 超时
	for echo, ch := range o.echoChans {
		close(ch)
		delete(o.echoChans, echo)
	}
	o.mu.Unlock()
}

func (o *OneBot) currentConn() *websocket.Conn {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.conn
}

func (o *OneBot) isClosed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closed
}

// reconnect 断线后指数退避重连, 直到成功或 Close; 返回 false 表示已关闭
func (o *OneBot) reconnect() bool {
	backoff := time.Second
	for {
		if o.isClosed() {
			return false
		}
		log.Printf("[adapter] %v 后重连 OneBot WS: %s", backoff, o.wsURL)
		time.Sleep(backoff)
		if o.isClosed() {
			return false
		}
		conn, err := o.dial()
		if err != nil {
			log.Printf("[adapter] 重连失败: %v", err)
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		o.setConn(conn)
		log.Printf("[adapter] 已重连 OneBot WS: %s", o.wsURL)
		return true
	}
}

func (o *OneBot) readLoop() {
	for {
		conn := o.currentConn()
		if conn == nil {
			return
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			if o.isClosed() {
				log.Printf("[adapter] WS 连接已关闭: %v", err)
				return
			}
			log.Printf("[adapter] WS 连接断开: %v, 启动自动重连", err)
			if !o.reconnect() {
				return
			}
			continue
		}
		var probe struct {
			Echo     string `json:"echo"`
			PostType string `json:"post_type"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			continue
		}
		if probe.Echo != "" {
			o.mu.Lock()
			if ch, ok := o.echoChans[probe.Echo]; ok {
				// A slow stream consumer must not block events or other API calls.
				select {
				case ch <- json.RawMessage(data):
				default:
					close(ch)
					delete(o.echoChans, probe.Echo)
				}
			}
			o.mu.Unlock()
		} else if probe.PostType != "" && o.onEvent != nil {
			var ev core.Event
			if err := json.Unmarshal(data, &ev); err == nil {
				o.onEvent(&ev)
			}
		}
	}
}

func (o *OneBot) Close() error {
	o.mu.Lock()
	o.closed = true
	conn := o.conn
	for echo, ch := range o.echoChans {
		close(ch)
		delete(o.echoChans, echo)
	}
	o.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

type CallResult struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
}

func (o *OneBot) Call(action string, params map[string]interface{}) (*CallResult, error) {
	return o.CallContext(context.Background(), action, params)
}

func (o *OneBot) CallContext(ctx context.Context, action string, params map[string]interface{}) (*CallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return o.callContext(ctx, action, params, nil)
}

func (o *OneBot) CallQQAPI(ctx context.Context, action string, params map[string]interface{}, onStream func(json.RawMessage) error) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cr, err := o.callContext(ctx, action, params, onStream)
	if err != nil {
		return nil, err
	}
	return cr.Data, nil
}

func (o *OneBot) callContext(ctx context.Context, action string, params map[string]interface{}, onStream func(json.RawMessage) error) (*CallResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o.mu.Lock()
	conn := o.conn
	if conn == nil || o.closed {
		o.mu.Unlock()
		return nil, errors.New("连接未建立")
	}
	echo := fmt.Sprintf("echo_%d", time.Now().UnixNano())
	ch := make(chan json.RawMessage, 64)
	o.echoChans[echo] = ch
	o.mu.Unlock()

	defer func() {
		o.mu.Lock()
		delete(o.echoChans, echo)
		o.mu.Unlock()
	}()

	msg := map[string]interface{}{
		"action": action,
		"params": params,
		"echo":   echo,
	}
	o.mu.Lock()
	if ctx.Err() != nil || conn != o.conn || o.closed {
		o.mu.Unlock()
		return nil, errors.New("调用已取消或连接已更换，未发送请求")
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	err := conn.SetWriteDeadline(deadline)
	if err == nil {
		err = conn.WriteJSON(msg)
	}
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}

	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				return nil, errors.New("API 连接中断或接收队列溢出，执行结果不确定，请勿自动重试")
			}
			var cr CallResult
			if err := json.Unmarshal(resp, &cr); err != nil {
				return nil, err
			}
			if cr.Retcode != 0 || cr.Status != "ok" {
				message := cr.Message
				if message == "" {
					message = cr.Wording
				}
				return &cr, fmt.Errorf("API %s 返回错误 status=%s retcode=%d: %s", action, cr.Status, cr.Retcode, message)
			}
			var frame struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(cr.Data, &frame)
			if frame.Type == "stream" {
				if onStream == nil {
					return nil, errors.New("此接口返回数据流，需要流式调用")
				}
				if err := onStream(cr.Data); err != nil {
					return nil, err
				}
				continue
			}
			return &cr, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("API 等待中断，执行结果不确定，请勿自动重试: %w", ctx.Err())
		}
	}
}

func segToMap(seg core.Segment) map[string]interface{} {
	return map[string]interface{}{"type": seg.Type, "data": seg.Data}
}

func (o *OneBot) SendGroupMsg(groupID, userID int64, msg []core.Segment) error {
	params := map[string]interface{}{
		"group_id": groupID,
		"message":  msg,
	}
	if userID > 0 {
		params["at_sender"] = true
	}
	_, err := o.Call("send_group_msg", params)
	return err
}

func (o *OneBot) SendPrivateMsg(userID int64, msg []core.Segment) error {
	_, err := o.Call("send_private_msg", map[string]interface{}{
		"user_id": userID,
		"message": msg,
	})
	return err
}

func (o *OneBot) SendMessageWithReceipt(ctx context.Context, groupID, userID int64, msg []core.Segment) (int64, error) {
	action := "send_private_msg"
	params := map[string]interface{}{"user_id": userID, "message": msg}
	if groupID > 0 {
		action = "send_group_msg"
		params = map[string]interface{}{"group_id": groupID, "message": msg}
	}
	cr, err := o.CallContext(ctx, action, params)
	if err != nil {
		return 0, err
	}
	return messageReceiptID(cr.Data)
}

func messageReceiptID(raw json.RawMessage) (int64, error) {
	var data struct {
		MessageID json.Number `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, err
	}
	id, err := data.MessageID.Int64()
	if err != nil || id == 0 {
		return 0, errors.New("发送消息未取得有效 message_id")
	}
	return id, nil
}

// SendForwardMsg 发送合并转发消息
func (o *OneBot) SendForwardMsg(groupID, userID int64, nodes []core.ForwardNode) error {
	_, err := o.Call("send_forward_msg", forwardMessageParams(groupID, userID, nodes))
	return err
}

func (o *OneBot) SendForwardWithReceipt(ctx context.Context, groupID, userID int64, nodes []core.ForwardNode) (int64, error) {
	cr, err := o.CallContext(ctx, "send_forward_msg", forwardMessageParams(groupID, userID, nodes))
	if err != nil {
		return 0, err
	}
	return messageReceiptID(cr.Data)
}

func forwardMessageParams(groupID, userID int64, nodes []core.ForwardNode) map[string]interface{} {
	// 转成 OneBot node 段格式
	segments := make([]map[string]interface{}, 0, len(nodes))
	for _, n := range nodes {
		content := make([]map[string]interface{}, 0, len(n.Content))
		for _, c := range n.Content {
			content = append(content, segToMap(c))
		}
		segments = append(segments, map[string]interface{}{
			"type": "node",
			"data": map[string]interface{}{
				"uin":      n.Uin,
				"name":     n.Name,
				"nickname": n.Nickname,
				"content":  content,
			},
		})
	}
	params := map[string]interface{}{"messages": segments}
	if groupID > 0 {
		params["group_id"] = groupID
	} else {
		params["user_id"] = userID
	}
	return params
}

// core.Sender 接口实现
func (o *OneBot) SendGroupMsgRaw(groupID int64, msg []core.Segment) error {
	_, err := o.Call("send_group_msg", map[string]interface{}{"group_id": groupID, "message": msg})
	return err
}

// GetMsg 按消息 ID 获取消息详情 (get_msg)
func (o *OneBot) GetMsg(messageID int64) (*core.MessageInfo, error) {
	cr, err := o.Call("get_msg", map[string]interface{}{"message_id": messageID})
	if err != nil {
		return nil, err
	}
	var data struct {
		MessageID  int64           `json:"message_id"`
		GroupID    int64           `json:"group_id"`
		Time       int64           `json:"time"`
		RawMessage string          `json:"raw_message"`
		Message    json.RawMessage `json:"message"`
		Sender     struct {
			UserID   int64  `json:"user_id"`
			Nickname string `json:"nickname"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(cr.Data, &data); err != nil {
		return nil, err
	}
	info := &core.MessageInfo{
		MessageID:  data.MessageID,
		GroupID:    data.GroupID,
		UserID:     data.Sender.UserID,
		Nickname:   data.Sender.Nickname,
		Time:       data.Time,
		RawMessage: data.RawMessage,
	}
	// message 可能是消息段数组, 也可能是字符串; 失败时退化用 raw_message 构造 text 段
	if len(data.Message) > 0 {
		if err := json.Unmarshal(data.Message, &info.Message); err != nil {
			info.Message = []core.Segment{core.TextSegment(data.RawMessage)}
		}
	} else if data.RawMessage != "" {
		info.Message = []core.Segment{core.TextSegment(data.RawMessage)}
	}
	return info, nil
}

// ---- 文件/图片 API ----

// GetImage 获取图片信息(url/file/path)
func (o *OneBot) GetImage(file string) (map[string]interface{}, error) {
	cr, err := o.Call("get_image", map[string]interface{}{"file": file})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// GetFile 获取文件信息(file 为文件标识或路径)
func (o *OneBot) GetFile(file string) (map[string]interface{}, error) {
	cr, err := o.Call("get_file", map[string]interface{}{"file": file})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// DownloadFile 下载文件到指定位置, 返回文件信息
func (o *OneBot) DownloadFile(url, threadCount string) (map[string]interface{}, error) {
	params := map[string]interface{}{"url": url}
	if threadCount != "" {
		params["thread_count"] = threadCount
	}
	cr, err := o.Call("download_file", params)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// UploadGroupFile 上传文件到群
func (o *OneBot) UploadGroupFile(groupID int64, file, name string) error {
	_, err := o.Call("upload_group_file", map[string]interface{}{
		"group_id": groupID,
		"file":     file,
		"name":     name,
	})
	return err
}

// UploadPrivateFile 上传文件到私聊
func (o *OneBot) UploadPrivateFile(userID int64, file, name string) error {
	_, err := o.Call("upload_private_file", map[string]interface{}{
		"user_id": userID,
		"file":    file,
		"name":    name,
	})
	return err
}

// GetGroupRootFiles 获取群根目录文件列表
func (o *OneBot) GetGroupRootFiles(groupID int64) (map[string]interface{}, error) {
	cr, err := o.Call("get_group_root_files", map[string]interface{}{"group_id": groupID})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// GetGroupFilesByFolder 获取群指定文件夹文件列表
func (o *OneBot) GetGroupFilesByFolder(groupID, folderID int64) (map[string]interface{}, error) {
	cr, err := o.Call("get_group_files_by_folder", map[string]interface{}{
		"group_id":  groupID,
		"folder_id": folderID,
	})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---- 群管理 API ----

// SetGroupBan 禁言成员 (duration 秒, 0=解除禁言)
func (o *OneBot) SetGroupBan(groupID, userID int64, duration int) error {
	_, err := o.Call("set_group_ban", map[string]interface{}{
		"group_id": groupID, "user_id": userID, "duration": duration,
	})
	return err
}

// SetGroupWholeBan 全体禁言
func (o *OneBot) SetGroupWholeBan(groupID int64, enable bool) error {
	_, err := o.Call("set_group_whole_ban", map[string]interface{}{
		"group_id": groupID, "enable": enable,
	})
	return err
}

// SetGroupKick 踢出成员
func (o *OneBot) SetGroupKick(groupID, userID int64, rejectAddRequest bool) error {
	_, err := o.Call("set_group_kick", map[string]interface{}{
		"group_id": groupID, "user_id": userID, "reject_add_request": rejectAddRequest,
	})
	return err
}

// SetGroupAdmin 设置/取消群管理员
func (o *OneBot) SetGroupAdmin(groupID, userID int64, enable bool) error {
	_, err := o.Call("set_group_admin", map[string]interface{}{
		"group_id": groupID, "user_id": userID, "enable": enable,
	})
	return err
}

// SetGroupCard 设置群名片
func (o *OneBot) SetGroupCard(groupID, userID int64, card string) error {
	_, err := o.Call("set_group_card", map[string]interface{}{
		"group_id": groupID, "user_id": userID, "card": card,
	})
	return err
}

// SetGroupName 设置群名称
func (o *OneBot) SetGroupName(groupID int64, name string) error {
	_, err := o.Call("set_group_name", map[string]interface{}{
		"group_id": groupID, "group_name": name,
	})
	return err
}

// SetGroupLeave 退出群
func (o *OneBot) SetGroupLeave(groupID int64, isDismiss bool) error {
	_, err := o.Call("set_group_leave", map[string]interface{}{
		"group_id": groupID, "is_dismiss": isDismiss,
	})
	return err
}

// SendGroupNotice 发送群公告
func (o *OneBot) SendGroupNotice(groupID int64, content string) error {
	_, err := o.Call("send_group_notice", map[string]interface{}{
		"group_id": groupID, "content": content,
	})
	return err
}

// GetGroupMemberList 获取群成员列表
func (o *OneBot) GetGroupMemberList(groupID int64) ([]map[string]interface{}, error) {
	cr, err := o.Call("get_group_member_list", map[string]interface{}{"group_id": groupID})
	if err != nil {
		return nil, err
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(cr.Data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// GetGroupInfo 获取群信息(群名/人数等)
func (o *OneBot) GetGroupInfo(groupID int64) (map[string]interface{}, error) {
	cr, err := o.Call("get_group_info", map[string]interface{}{"group_id": groupID})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (o *OneBot) GetGroupList() ([]map[string]interface{}, error) {
	cr, err := o.Call("get_group_list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var groups []map[string]interface{}
	if err := json.Unmarshal(cr.Data, &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (o *OneBot) DeleteMsg(messageID int64) error {
	_, err := o.Call("delete_msg", map[string]interface{}{"message_id": messageID})
	return err
}

func (o *OneBot) SetFriendAddRequest(flag string, approve bool, remark string) error {
	_, err := o.Call("set_friend_add_request", map[string]interface{}{"flag": flag, "approve": approve, "remark": remark})
	return err
}

// GetGroupMemberInfo 获取群成员信息
func (o *OneBot) GetGroupMemberInfo(groupID, userID int64) (map[string]interface{}, error) {
	cr, err := o.Call("get_group_member_info", map[string]interface{}{
		"group_id": groupID, "user_id": userID, "no_cache": true,
	})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---- 入群申请 API ----

// SetGroupAddRequest 审批入群申请(sub_type=add)
func (o *OneBot) SetGroupAddRequest(flag string, approve bool, reason string) error {
	_, err := o.Call("set_group_add_request", map[string]interface{}{
		"flag":     flag,
		"sub_type": "add",
		"approve":  approve,
		"reason":   reason,
	})
	return err
}

// GetStrangerInfo 获取陌生人信息(nickname 等)
func (o *OneBot) GetStrangerInfo(userID int64) (map[string]interface{}, error) {
	cr, err := o.Call("get_stranger_info", map[string]interface{}{"user_id": userID})
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cr.Data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
