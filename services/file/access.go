package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gobot/core"
)

func (m *Manager) isOwner(ev *core.Event) bool {
	return ev != nil && ev.UserID > 0 && m.bot.Cfg.Bot.MasterID == strconv.FormatInt(ev.UserID, 10)
}

func validSource(source string) bool {
	for _, prefix := range []string{"private_", "group_"} {
		if strings.HasPrefix(source, prefix) {
			n, err := strconv.ParseInt(strings.TrimPrefix(source, prefix), 10, 64)
			return err == nil && n > 0
		}
	}
	return false
}

func (m *Manager) scopeRoot(ev *core.Event) (string, error) {
	if ev == nil || ev.UserID <= 0 {
		return "", fmt.Errorf("缺少文件访问身份")
	}
	return filepath.Abs(filepath.Join(m.dir, sourceOf(ev)))
}

// SaveMedia 保存生成的媒体并登记来源，返回可供 OneBot 发送的绝对路径。
func (m *Manager) SaveMedia(ctx context.Context, name string, data []byte) (string, error) {
	ev := core.EventFrom(ctx)
	if ev == nil || ev.UserID <= 0 || !validSource(sourceOf(ev)) {
		return "", fmt.Errorf("缺少文件保存身份")
	}
	name, err := m.saveData(data, name, sourceOf(ev))
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(m.dir, sourceOf(ev), name))
}

// OpenRoot 在打开文件时执行目录约束，阻止 ..、符号链接和路径替换逃逸。
func (m *Manager) openFile(ctx context.Context, path string) (*os.File, error) {
	ev := core.EventFrom(ctx)
	if m.isOwner(ev) {
		if path == "" {
			path = m.dir
		}
		return os.Open(path)
	}
	rootPath, err := m.scopeRoot(ev)
	if err != nil {
		return nil, err
	}
	if path == "" {
		path = "."
	}
	if info, err := os.Lstat(rootPath); err != nil {
		return nil, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("会话文件目录不能为符号链接")
	}
	if filepath.IsAbs(path) {
		path, err = filepath.Rel(rootPath, path)
		if err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(path)
}

func (m *Manager) listFor(ev *core.Event) []FileItem {
	items := m.List()
	if m.isOwner(ev) {
		return items
	}
	if ev == nil {
		return nil
	}
	filtered := make([]FileItem, 0, len(items))
	for _, it := range items {
		if it.Source == sourceOf(ev) {
			filtered = append(filtered, it)
		}
	}
	return filtered
}

func (m *Manager) resolveFor(ctx context.Context, ref string) (string, error) {
	ev := core.EventFrom(ctx)
	if m.isOwner(ev) {
		return m.ResolvePath(ref)
	}
	root, err := m.scopeRoot(ev)
	if err != nil {
		return "", err
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	f, err := m.openFile(ctx, path)
	if err != nil {
		return "", fmt.Errorf("文件不属于当前会话或不存在: %w", err)
	}
	f.Close()
	return filepath.EvalSymlinks(path)
}
