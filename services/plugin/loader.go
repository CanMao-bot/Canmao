package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"plugin"

	"gobot/pluginapi"
)

type Manager struct {
	dir     string
	plugins []pluginapi.Plugin
}

func New(dir string) *Manager {
	return &Manager{dir: dir}
}

func (m *Manager) LoadAll() ([]pluginapi.Plugin, error) {
	if m.dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(m.dir); os.IsNotExist(err) {
		os.MkdirAll(m.dir, 0o755)
		return nil, nil
	}
	files, _ := filepath.Glob(filepath.Join(m.dir, "*.so"))
	var loaded []pluginapi.Plugin
	var firstErr error
	for _, f := range files {
		p, err := m.loadOne(f)
		if err != nil {
			// 单个插件失败不影响其他插件
			if firstErr == nil {
				firstErr = fmt.Errorf("加载插件 %s: %w", f, err)
			}
			continue
		}
		loaded = append(loaded, p)
	}
	m.plugins = loaded
	return loaded, firstErr
}

func (m *Manager) loadOne(path string) (pluginapi.Plugin, error) {
	p, err := plugin.Open(path)
	if err != nil {
		return nil, err
	}
	sym, err := p.Lookup("Plugin")
	if err != nil {
		return nil, fmt.Errorf("插件缺少 Plugin 符号: %w", err)
	}
	// plugin.Lookup 返回指针, 需要解引用
	fnPtr, ok := sym.(*func() pluginapi.Plugin)
	if !ok {
		return nil, fmt.Errorf("Plugin 符号类型错误, 应为 *func() pluginapi.Plugin")
	}
	return (*fnPtr)(), nil
}

func (m *Manager) Close() {
	for _, p := range m.plugins {
		_ = p.Close()
	}
}
