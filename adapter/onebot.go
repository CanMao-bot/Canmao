package adapter

import (
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

func (o *OneBot) Connect() error {
	hdr := map[string][]string{}
	if o.token != "" {
		hdr["Authorization"] = []string{"Bearer " + o.token}
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.Dial(o.wsURL, hdr)
	if err != nil {
		return fmt.Errorf("连接 %s 失败: %w", o.wsURL, err)
	}
	o.conn = conn
	log.Printf("[adapter] 已连接 OneBot WS: %s", o.wsURL)
	go o.readLoop()
	return nil
}

func (o *OneBot) readLoop() {
	for {
		_, data, err := o.conn.ReadMessage()
		if err != nil {
			if !o.closed {
				log.Printf("[adapter] WS 连接断开: %v", err)
			}
			return
		}
		var probe struct {
			Echo   string `json:"echo"`
			PostType string `json:"post_type"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			continue
		}
		if probe.Echo != "" {
			o.mu.Lock()
			if ch, ok := o.echoChans[probe.Echo]; ok {
				ch <- json.RawMessage(data)
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
	o.closed = true
	if o.conn != nil {
		return o.conn.Close()
	}
	return nil
}

type CallResult struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
}

func (o *OneBot) Call(action string, params map[string]interface{}) (*CallResult, error) {
	if o.conn == nil {
		return nil, errors.New("连接未建立")
	}
	echo := fmt.Sprintf("echo_%d", time.Now().UnixNano())
	ch := make(chan json.RawMessage, 1)
	o.mu.Lock()
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
	err := o.conn.WriteJSON(msg)
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		var cr CallResult
		if err := json.Unmarshal(resp, &cr); err != nil {
			return nil, err
		}
		if cr.Retcode != 0 {
			return &cr, fmt.Errorf("API 返回错误 retcode=%d", cr.Retcode)
		}
		return &cr, nil
	case <-time.After(30 * time.Second):
		return nil, errors.New("API 调用超时")
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

// SendForwardMsg 发送合并转发消息
func (o *OneBot) SendForwardMsg(groupID, userID int64, nodes []core.ForwardNode) error {
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
	_, err := o.Call("send_forward_msg", params)
	return err
}

// core.Sender 接口实现
func (o *OneBot) SendGroupMsgRaw(groupID int64, msg []core.Segment) error {
	_, err := o.Call("send_group_msg", map[string]interface{}{"group_id": groupID, "message": msg})
	return err
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

// GetGroupMemberInfo 获取群成员信息
func (o *OneBot) GetGroupMemberInfo(groupID, userID int64) (map[string]interface{}, error) {
	cr, err := o.Call("get_group_member_info", map[string]interface{}{
		"group_id": groupID, "user_id": userID,
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
