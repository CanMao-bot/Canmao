package file

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/services/ai"
	"gobot/store/perm"
)

// FileItem 已保存的文件记录
type FileItem struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Source    string `json:"source"` // group_xxx / private_xxx
	CreatedAt int64  `json:"created_at"`
}

type Manager struct {
	dir            string
	bot            *core.Bot
	files          core.FileClient
	perm           *perm.Store
	retentionDays  int
	mu             sync.Mutex
	index          []FileItem
	http           *http.Client
	cleanupTicker  *time.Ticker
	stopCh         chan struct{}
}

func New(dir string, bot *core.Bot, fc core.FileClient, permStore *perm.Store, retentionDays int) *Manager {
	m := &Manager{
		dir:           filepath.Join(dir, "files"),
		bot:           bot,
		files:         fc,
		perm:          permStore,
		retentionDays: retentionDays,
		http:          &http.Client{Timeout: 120 * time.Second},
		stopCh:        make(chan struct{}),
	}
	os.MkdirAll(m.dir, 0o755)
	m.loadIndex()
	// 启动定期清理
	if retentionDays > 0 {
		m.cleanupTicker = time.NewTicker(6 * time.Hour)
		go m.cleanupLoop()
	}
	return m
}

// Close 停止后台清理
func (m *Manager) Close() {
	select {
	case <-m.stopCh:
		return
	default:
		close(m.stopCh)
	}
	if m.cleanupTicker != nil {
		m.cleanupTicker.Stop()
	}
}

func (m *Manager) cleanupLoop() {
	for {
		select {
		case <-m.cleanupTicker.C:
			m.Cleanup()
		case <-m.stopCh:
			return
		}
	}
}

// Cleanup 删除超过保留天数的缓存文件
func (m *Manager) Cleanup() {
	if m.retentionDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(m.retentionDays) * 24 * time.Hour).Unix()
	var removed []string
	m.mu.Lock()
	var keep []FileItem
	for _, it := range m.index {
		if it.CreatedAt > 0 && it.CreatedAt < cutoff {
			// 仅删除位于缓存目录内的文件
			if strings.HasPrefix(it.Path, m.dir) {
				_ = os.Remove(it.Path)
				removed = append(removed, it.Name)
			}
			continue
		}
		keep = append(keep, it)
	}
	m.index = keep
	m.mu.Unlock()
	if len(removed) > 0 {
		fmt.Printf("[file] 清理过期缓存文件 %d 个: %v\n", len(removed), removed)
	}
}

// enabled 群未启用且非主人时不处理
func (m *Manager) enabled(bot *core.Bot, ev *core.Event) bool {
	if !ev.IsGroup() {
		return true
	}
	if m.perm != nil && !m.perm.BotEnabled(ev.GroupID) {
		master := bot.Cfg.Bot.MasterID == fmt.Sprintf("%d", ev.UserID)
		return master
	}
	return true
}

func (m *Manager) Name() string { return "file-manager" }

// Handle 检查消息中的图片/文件, 自动下载保存; 处理文件命令
func (m *Manager) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool {
	if ev.Type != "message" {
		return false
	}
	if !m.enabled(bot, ev) {
		return false
	}
	// 文件命令
	t := strings.TrimSpace(ev.Text())
	cmd, arg := cmdOfFile(t)
	switch cmd {
	case "/files", "/文件列表":
		bot.Reply(ev, m.renderList())
		return true
	case "/cleanfiles", "/清理文件":
		m.Cleanup()
		bot.Reply(ev, "🗑️ 已清理过期缓存文件")
		return true
	case "/sendfile":
		return m.handleSendFile(bot, ev, arg)
	}
	// 自动保存收到的图片/文件(静默保存, 不回复提示)
	for _, seg := range ev.Message {
		switch seg.Type {
		case "image":
			url, _ := seg.Data["url"].(string)
			if url == "" {
				continue
			}
			_, _ = m.saveFromURL(url, "", sourceOf(ev))
		case "file":
			url, _ := seg.Data["url"].(string)
			name, _ := seg.Data["name"].(string)
			if url == "" {
				continue
			}
			_, _ = m.saveFromURL(url, name, sourceOf(ev))
		}
	}
	return false // 不消费消息, 让后续服务继续处理
}

func (m *Manager) handleSendFile(bot *core.Bot, ev *core.Event, arg string) bool {
	// 格式: /sendfile <本地文件>  (群内发送到本群; 私聊发给自己)
	ref := strings.TrimSpace(arg)
	if ref == "" {
		bot.Reply(ev, "用法: /sendfile <已保存文件名>")
		return true
	}
	path, err := m.ResolvePath(ref)
	if err != nil {
		bot.Reply(ev, err.Error())
		return true
	}
	name := filepath.Base(path)
	if ev.IsGroup() {
		if err := m.files.UploadGroupFile(ev.GroupID, path, name); err != nil {
			bot.Reply(ev, "上传失败: "+err.Error())
			return true
		}
		bot.Reply(ev, "✅ 已上传到本群: "+name)
	} else {
		if err := m.files.UploadPrivateFile(ev.UserID, path, name); err != nil {
			bot.Reply(ev, "上传失败: "+err.Error())
			return true
		}
		bot.Reply(ev, "✅ 已发送文件: "+name)
	}
	return true
}

func (m *Manager) renderList() string {
	list := m.List()
	var b strings.Builder
	b.WriteString("📁 已保存文件")
	if m.retentionDays > 0 {
		b.WriteString(fmt.Sprintf(" (保留 %d 天)", m.retentionDays))
	}
	b.WriteString(":\n")
	if len(list) == 0 {
		b.WriteString("  (空)")
		return b.String()
	}
	for _, it := range list {
		sz := fmt.Sprintf("%.1f", float64(it.Size)/1024)
		b.WriteString(fmt.Sprintf("- %s (%s KB, %s, %s)\n", it.Name, sz, it.Source, time.Unix(it.CreatedAt, 0).Format("01-02 15:04")))
	}
	b.WriteString("\n用 /sendfile <文件名> 发送到本群, /cleanfiles 手动清理")
	return b.String()
}

func sourceOf(ev *core.Event) string {
	if ev.IsGroup() {
		return fmt.Sprintf("group_%d", ev.GroupID)
	}
	return fmt.Sprintf("private_%d", ev.UserID)
}

// cmdOfFile 提取命令首个 token 和参数
func cmdOfFile(text string) (cmd, arg string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", ""
	}
	cmd = fields[0]
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	return cmd, arg
}

// saveFromURL 从 URL 下载并保存到本地
func (m *Manager) saveFromURL(rawURL, hintName, source string) (string, error) {
	resp, err := m.http.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// 生成文件名
	name := hintName
	if name == "" {
		name = filenameFromURL(rawURL)
	}
	if name == "" {
		name = "file_" + time.Now().Format("20060102_150405")
	}
	// 去除路径分隔符
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")

	path := filepath.Join(m.dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}

	m.mu.Lock()
	m.index = append(m.index, FileItem{
		Name: name, Path: path, Size: int64(len(data)),
		Source: source, CreatedAt: time.Now().Unix(),
	})
	m.mu.Unlock()
	m.saveIndex()
	return name, nil
}

// SaveImageFromCache 通过 get_image 获取缓存图片信息并保存
func (m *Manager) SaveImageFromCache(fileID, source string) (string, error) {
	info, err := m.files.GetImage(fileID)
	if err != nil {
		return "", err
	}
	urlStr, _ := info["url"].(string)
	if urlStr == "" {
		return "", fmt.Errorf("图片无 URL")
	}
	return m.saveFromURL(urlStr, "", source)
}

// SaveFileFromCache 通过 get_file 获取文件并保存
func (m *Manager) SaveFileFromCache(fileID, source string) (string, error) {
	info, err := m.files.GetFile(fileID)
	if err != nil {
		return "", err
	}
	// get_file 可能返回本地路径
	path, _ := info["file"].(string)
	if path != "" && fileExists(path) {
		// 复制到本地目录
		name := filepath.Base(path)
		dest := filepath.Join(m.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return "", err
		}
		m.mu.Lock()
		m.index = append(m.index, FileItem{Name: name, Path: dest, Size: int64(len(data)), Source: source, CreatedAt: time.Now().Unix()})
		m.mu.Unlock()
		m.saveIndex()
		return name, nil
	}
	// 否则尝试 URL
	urlStr, _ := info["url"].(string)
	if urlStr == "" {
		return "", fmt.Errorf("无法定位文件")
	}
	return m.saveFromURL(urlStr, "", source)
}

// SaveFromPath 将本地路径文件登记进索引(用于上传)
func (m *Manager) RegisterLocal(path, source string) (string, error) {
	if !fileExists(path) {
		return "", fmt.Errorf("文件不存在: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	name := filepath.Base(path)
	m.mu.Lock()
	m.index = append(m.index, FileItem{Name: name, Path: path, Size: info.Size(), Source: source, CreatedAt: time.Now().Unix()})
	m.mu.Unlock()
	m.saveIndex()
	return name, nil
}

// List 列出已保存文件
func (m *Manager) List() []FileItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]FileItem, len(m.index))
	copy(out, m.index)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// ResolvePath 按名称或路径解析本地文件
func (m *Manager) ResolvePath(ref string) (string, error) {
	// 先尝试直接路径
	if fileExists(ref) {
		return ref, nil
	}
	// 在索引中查找
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.index {
		if it.Name == ref {
			return it.Path, nil
		}
	}
	return "", fmt.Errorf("未找到文件: %s", ref)
}

func (m *Manager) loadIndex() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.index = nil
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		m.index = append(m.index, FileItem{
			Name: e.Name(), Path: filepath.Join(m.dir, e.Name()),
			Size: info.Size(), CreatedAt: info.ModTime().Unix(),
		})
	}
}

func (m *Manager) saveIndex() {
	// 索引即目录扫描结果, 无需额外持久化
}

// AI 工具
func (m *Manager) Tools() []ai.Tool {
	return []ai.Tool{
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "list_files",
				Description: "列出 bot 已保存的文件(名称、大小、来源)",
				Parameters:  ai.ToolParameters{Type: "object", Properties: map[string]*ai.ToolParam{}},
			},
			Risk: ai.RiskLow,
			Callback: func(ctx context.Context, args map[string]interface{}) (string, error) {
				list := m.List()
				if len(list) == 0 {
					return "暂无已保存的文件", nil
				}
				var b strings.Builder
				for _, it := range list {
					b.WriteString(fmt.Sprintf("- %s (%d KB, %s)\n", it.Name, it.Size/1024, it.Source))
				}
				return b.String(), nil
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "upload_file_to_group",
				Description: "将本地已保存的文件上传到指定群",
				Parameters: ai.ToolParameters{Type: "object", Properties: map[string]*ai.ToolParam{
					"group_id": {Type: "integer", Description: "目标群号"},
					"name":     {Type: "string", Description: "已保存的文件名(用 list_files 查看)"},
				}, Required: []string{"group_id", "name"}},
			},
			Risk: ai.RiskHigh,
			Callback: func(ctx context.Context, args map[string]interface{}) (string, error) {
				gid := int64(args["group_id"].(float64))
				name := args["name"].(string)
				path, err := m.ResolvePath(name)
				if err != nil {
					return err.Error(), nil
				}
				if err := m.files.UploadGroupFile(gid, path, filepath.Base(path)); err != nil {
					return "上传失败: " + err.Error(), nil
				}
				return "已上传到群 " + fmt.Sprintf("%d", gid), nil
			},
		},
		{
			Type: "function",
			Function: ai.ToolFunction{
				Name:        "get_group_files",
				Description: "获取指定群根目录的文件列表",
				Parameters: ai.ToolParameters{Type: "object", Properties: map[string]*ai.ToolParam{
					"group_id": {Type: "integer", Description: "群号"},
				}, Required: []string{"group_id"}},
			},
			Risk: ai.RiskLow,
			Callback: func(ctx context.Context, args map[string]interface{}) (string, error) {
				gid := int64(args["group_id"].(float64))
				info, err := m.files.GetGroupRootFiles(gid)
				if err != nil {
					return "获取失败: " + err.Error(), nil
				}
				return fmt.Sprintf("%v", info), nil
			},
		},
		readFileTool(),
		writeFileTool(),
		listDirTool(),
	}
}

// readFileTool 读取服务器本地文件
func readFileTool() ai.Tool {
	t := ai.NewTool("read_file", "读取服务器上的文本文件内容。可用于查看配置文件、日志、代码等。",
		map[string]*ai.ToolParam{
			"path":    {Type: "string", Description: "文件绝对路径"},
			"max_len": {Type: "integer", Description: "最大读取字符数(可选, 默认4000)"},
		},
		[]string{"path"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "错误: path 不能为空", nil
			}
			maxLen := 4000
			if v, ok := args["max_len"].(float64); ok && v > 0 {
				maxLen = int(v)
			}
			if !fileExists(path) {
				return "文件不存在: " + path, nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "读取失败: " + err.Error(), nil
			}
			content := string(data)
			if len([]rune(content)) > maxLen {
				content = string([]rune(content)[:maxLen]) + "\n...(内容过长已截断)"
			}
			return content, nil
		})
	t.Risk = ai.RiskLow
	return t
}

// writeFileTool 写入/修改服务器文件
func writeFileTool() ai.Tool {
	t := ai.NewTool("write_file", "向服务器文件写入内容。若文件不存在则创建, 存在则覆盖。可用于修改配置、创建文件等。",
		map[string]*ai.ToolParam{
			"path":    {Type: "string", Description: "文件绝对路径"},
			"content": {Type: "string", Description: "要写入的内容"},
		},
		[]string{"path", "content"},
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path == "" {
				return "错误: path 不能为空", nil
			}
			// 确保目录存在
			if dir := filepath.Dir(path); dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return "创建目录失败: " + err.Error(), nil
				}
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "写入失败: " + err.Error(), nil
			}
			sz := len([]rune(content))
			return fmt.Sprintf("✅ 已写入 %s (%d 字符)", path, sz), nil
		})
	t.Risk = ai.RiskCritical
	return t
}

// listDirTool 列出目录内容
func listDirTool() ai.Tool {
	t := ai.NewTool("list_directory", "列出服务器指定目录下的文件和子目录。",
		map[string]*ai.ToolParam{
			"path": {Type: "string", Description: "目录路径(可选, 默认 /root)"},
		},
		nil,
		func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				path = "/root"
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return "读取目录失败: " + err.Error(), nil
			}
			var b strings.Builder
			b.WriteString("📂 " + path + ":\n")
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				info, err := e.Info()
				if err == nil {
					b.WriteString(fmt.Sprintf("- %s (%d KB)\n", name, info.Size()/1024))
				} else {
					b.WriteString("- " + name + "\n")
				}
			}
			return b.String(), nil
		})
	t.Risk = ai.RiskLow
	return t
}

func filenameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := filepath.Base(u.Path)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return base
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

var _ = md5hex
