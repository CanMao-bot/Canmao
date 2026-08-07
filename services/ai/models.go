package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"gobot/core"
	"gobot/store/group"
	"gobot/store/provider"
)

// ModelInfo 单个模型信息
type ModelInfo struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	OwnedBy       string `json:"owned_by"`
	ContextWindow int    `json:"context_window"`
}

// 内置已知模型上下文窗口 (token) 用于展示能力;
// 实际列表一律从各 provider API 获取
var knownContextWindows = map[string]int{
	"deepseek-chat":     65536,
	"deepseek-reasoner": 65536,
	"deepseek-v3":       65536,
	"deepseek-v4-flash":    131072,
	"deepseek-v4-flash-0731": 131072,
	"deepseek-v4-pro":      131072,
	"gpt-4o":            128000,
	"gpt-4o-mini":       128000,
	"gpt-4-turbo":       128000,
	"gpt-4":             8192,
	"gpt-3.5-turbo":     16385,
	"claude-3-5-sonnet": 200000,
	"claude-3-opus":     200000,
	"claude-sonnet-4":   200000,
	"claude-opus-4":     200000,
	"qwen-max":            32768,
	"qwen-plus":           131072,
	"qwen-turbo":          1000000,
	"qwen3.7-max":         131072,
	"qwen3.7-plus":        131072,
	"qwen3.6-flash":       131072,
	"qwen3.8-max":         131072,
	"glm-4":               128000,
	"glm-4-plus":          128000,
	"glm-5.2":             131072,
	"wan2.7-image":        0,
	"wan2.7-image-pro":    0,
	"kimi-k2":           131072,
	"kimi-k3":           1048576,
	"moonshotai/kimi-k3": 1048576,
	"moonshot-v1-32k":   32768,
	"gemini-1.5-pro":    2097152,
	"mistral-large":     32000,
	"llama-3.1-405b":    131072,
}

// ModelRegistry 管理多个模型提供商与当前模型
type ModelRegistry struct {
	mu      sync.RWMutex
	store   *provider.Store
	group   *group.Store
	cfg     *core.AIConfig
	http    *http.Client
	// 已拉取的各 provider 模型列表
	modelsCache map[string][]ModelInfo
	// 各 provider 当前模型
	modelsByProvider map[string]string
	// 各 provider 上下文窗口覆盖
	ctxByProvider map[string]int
	// 用户组模型覆盖 (master/admin/member)
	groupModels map[string]string
}

func NewModelRegistry(cfg *core.AIConfig) *ModelRegistry {
	return &ModelRegistry{
		cfg:              cfg,
		http:             &http.Client{Timeout: 30 * time.Second},
		modelsCache:      make(map[string][]ModelInfo),
		modelsByProvider: make(map[string]string),
		ctxByProvider:    make(map[string]int),
		groupModels:      make(map[string]string),
	}
}

func (r *ModelRegistry) SetStore(st *provider.Store) { r.store = st }

// SetGroupStore 设置用户组模型存储
func (r *ModelRegistry) SetGroupStore(st *group.Store) {
	r.group = st
	if st != nil {
		if m, err := st.Load(); err == nil {
			r.mu.Lock()
			r.groupModels["master"] = m.Master
			r.groupModels["admin"] = m.Admin
			r.groupModels["member"] = m.Member
			r.mu.Unlock()
		}
	}
}

// ModelForLevel 返回指定用户组的模型(未覆盖则用当前 provider 模型)
func (r *ModelRegistry) ModelForLevel(level userLevel) string {
	r.mu.RLock()
	over, ok := r.groupModels[levelKey(level)]
	r.mu.RUnlock()
	if ok && over != "" {
		return over
	}
	p, err := r.CurrentProvider()
	if err != nil {
		return ""
	}
	return p.Model
}

// SetGroupModel 为用户组设置默认模型
func (r *ModelRegistry) SetGroupModel(level userLevel, model string) error {
	if r.group == nil {
		return fmt.Errorf("用户组存储未初始化")
	}
	key := levelKey(level)
	r.mu.Lock()
	r.groupModels[key] = model
	r.mu.Unlock()
	m, err := r.group.Load()
	if err != nil {
		return err
	}
	switch level {
	case levelMaster:
		m.Master = model
	case levelAdmin:
		m.Admin = model
	default:
		m.Member = model
	}
	return r.group.Save(m)
}

// GroupModelsRender 渲染各组模型配置
func (r *ModelRegistry) GroupModelsRender() string {
	p, err := r.CurrentProvider()
	if err != nil {
		return "未配置提供商"
	}
	base := p.Model
	r.mu.RLock()
	master := r.groupModels["master"]
	admin := r.groupModels["admin"]
	member := r.groupModels["member"]
	r.mu.RUnlock()
	line := func(name, m string) string {
		if m == "" {
			m = base
		}
		return fmt.Sprintf("%s: %s", name, m)
	}
	return fmt.Sprintf("👥 用户组模型配置:\n%s\n%s\n%s\n\n全局默认: %s\n可用 /model group <组> <模型ID> 设置", line("主人", master), line("管理员", admin), line("普通成员", member), base)
}

func levelKey(l userLevel) string {
	switch l {
	case levelMaster:
		return "master"
	case levelAdmin:
		return "admin"
	default:
		return "member"
	}
}

// InitFromConfig 用 config.yaml 的单一 AI 配置作为初始 provider(若 store 为空)
func (r *ModelRegistry) InitFromConfig() error {
	if r.store == nil {
		return nil
	}
	d, err := r.store.Load()
	if err != nil {
		return err
	}
	// 已有数据则加载
	if len(d.Providers) > 0 {
		r.mu.Lock()
		for _, p := range d.Providers {
			r.modelsByProvider[p.Name] = p.Model
		}
		r.mu.Unlock()
		return nil
	}
	// store 为空且 config 有 base_url 时导入
	if r.cfg.BaseURL != "" {
		p := provider.Provider{
			Name:    "default",
			BaseURL: r.cfg.BaseURL,
			APIKey:  r.cfg.APIKey,
			Model:   r.cfg.Model,
		}
		d.Providers = append(d.Providers, p)
		d.Current = "default"
		if err := r.store.Save(d); err != nil {
			return err
		}
		r.mu.Lock()
		r.modelsByProvider["default"] = r.cfg.Model
		r.mu.Unlock()
	}
	return nil
}

// Providers 返回所有 provider(排序)
func (r *ModelRegistry) Providers() []provider.Provider {
	if r.store == nil {
		return nil
	}
	d, _ := r.store.Load()
	if d == nil {
		return nil
	}
	out := make([]provider.Provider, len(d.Providers))
	copy(out, d.Providers)
	return out
}

// CurrentProvider 当前 provider
func (r *ModelRegistry) CurrentProvider() (*provider.Provider, error) {
	if r.store == nil {
		return nil, fmt.Errorf("provider store 未初始化")
	}
	d, err := r.store.Load()
	if err != nil {
		return nil, err
	}
	if d.Current == "" && len(d.Providers) > 0 {
		d.Current = d.Providers[0].Name
	}
	for i := range d.Providers {
		if d.Providers[i].Name == d.Current {
			return &d.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("当前 provider 不存在")
}

// AddProvider 添加提供商; replace=true 覆盖已有
func (r *ModelRegistry) AddProvider(p provider.Provider, replace bool) error {
	if r.store == nil {
		return fmt.Errorf("provider store 未初始化")
	}
	d, err := r.store.Load()
	if err != nil {
		return err
	}
	found := false
	for i := range d.Providers {
		if d.Providers[i].Name == p.Name {
			if !replace {
				return fmt.Errorf("provider %s 已存在, 可用 /provider add %s ... force 覆盖", p.Name, p.Name)
			}
			d.Providers[i] = p
			found = true
			break
		}
	}
	if !found {
		d.Providers = append(d.Providers, p)
	}
	// 添加后自动切换为新 provider(便于立即查看/使用其模型)
	d.Current = p.Name
	if err := r.store.Save(d); err != nil {
		return err
	}
	r.mu.Lock()
	r.modelsByProvider[p.Name] = p.Model
	r.mu.Unlock()
	return nil
}

// RemoveProvider 移除提供商
func (r *ModelRegistry) RemoveProvider(name string) error {
	if r.store == nil {
		return fmt.Errorf("provider store 未初始化")
	}
	d, err := r.store.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i := range d.Providers {
		if d.Providers[i].Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("provider %s 不存在", name)
	}
	d.Providers = append(d.Providers[:idx], d.Providers[idx+1:]...)
	if d.Current == name {
		if len(d.Providers) > 0 {
			d.Current = d.Providers[0].Name
		} else {
			d.Current = ""
		}
	}
	if err := r.store.Save(d); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.modelsByProvider, name)
	delete(r.modelsCache, name)
	delete(r.ctxByProvider, name)
	r.mu.Unlock()
	return nil
}

// SetProviderProxy 设置 provider 的代理(空则清除)
func (r *ModelRegistry) SetProviderProxy(name, proxy string) error {
	if r.store == nil {
		return fmt.Errorf("provider store 未初始化")
	}
	d, err := r.store.Load()
	if err != nil {
		return err
	}
	found := false
	for i := range d.Providers {
		if d.Providers[i].Name == name {
			d.Providers[i].Proxy = proxy
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("provider %s 不存在", name)
	}
	return r.store.Save(d)
}

// SwitchProvider 切换当前提供商
func (r *ModelRegistry) SwitchProvider(name string) error {	if r.store == nil {
		return fmt.Errorf("provider store 未初始化")
	}
	d, err := r.store.Load()
	if err != nil {
		return err
	}
	found := false
	for i := range d.Providers {
		if d.Providers[i].Name == name {
			d.Current = name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("provider %s 不存在", name)
	}
	if err := r.store.Save(d); err != nil {
		return err
	}
	// 切换后清空该 provider 的模型缓存, 下次 /model list 强制重新拉取
	r.mu.Lock()
	delete(r.modelsCache, name)
	r.mu.Unlock()
	return nil
}

// Current 当前 provider 的当前模型
func (r *ModelRegistry) Current() string {
	p, err := r.CurrentProvider()
	if err != nil {
		return ""
	}
	return p.Model
}

// SetCurrentModel 切换当前 provider 的模型
func (r *ModelRegistry) SetCurrentModel(id string) error {
	if r.store == nil {
		return fmt.Errorf("provider store 未初始化")
	}
	d, err := r.store.Load()
	if err != nil {
		return err
	}
	found := false
	for i := range d.Providers {
		if d.Providers[i].Name == d.Current {
			d.Providers[i].Model = id
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("当前 provider 不存在")
	}
	if err := r.store.Save(d); err != nil {
		return err
	}
	r.mu.Lock()
	r.modelsByProvider[d.Current] = id
	r.mu.Unlock()
	return nil
}

// ContextWindow 当前 provider 当前模型的上下文窗口
func (r *ModelRegistry) ContextWindow() int {
	p, err := r.CurrentProvider()
	if err != nil {
		return 64000
	}
	r.mu.RLock()
	if w, ok := r.ctxByProvider[p.Name]; ok {
		r.mu.RUnlock()
		return w
	}
	r.mu.RUnlock()
	if w, ok := knownContextWindows[strings.ToLower(p.Model)]; ok {
		return w
	}
	if r.cfg != nil && r.cfg.ContextWindow > 0 {
		return r.cfg.ContextWindow
	}
	return 64000
}

// SetContextWindow 为当前 provider 记录上下文窗口(自动获取/手动设置)
func (r *ModelRegistry) SetContextWindow(name string, win int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctxByProvider[name] = win
}

// Endpoint 返回当前 provider 的连接参数, 供 Client 使用
func (r *ModelRegistry) Endpoint() (baseURL, apiKey, model string, err error) {
	p, err := r.CurrentProvider()
	if err != nil {
		return "", "", "", err
	}
	return p.BaseURL, p.APIKey, p.Model, nil
}

// EndpointEx 返回当前 provider 的连接参数(含代理)
func (r *ModelRegistry) EndpointEx() (baseURL, apiKey, model, proxy string, err error) {
	p, err := r.CurrentProvider()
	if err != nil {
		return "", "", "", "", err
	}
	return p.BaseURL, p.APIKey, p.Model, p.Proxy, nil
}

// httpClientFor 为指定代理构造 http client
func httpClientFor(proxy string, timeout time.Duration) *http.Client {
	tr := &http.Transport{}
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// Fetch 从当前 provider 拉取模型列表
func (r *ModelRegistry) Fetch(ctx context.Context) ([]ModelInfo, error) {
	p, err := r.CurrentProvider()
	if err != nil {
		return nil, err
	}
	return r.FetchFor(ctx, p)
}

// FetchFor 从指定 provider 拉取模型列表
func (r *ModelRegistry) FetchFor(ctx context.Context, p *provider.Provider) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	client := httpClientFor(p.Proxy, 30*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型列表 API 返回 %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析模型列表: %w", err)
	}
	for i := range result.Data {
		m := &result.Data[i]
		if m.ContextWindow == 0 {
			m.ContextWindow = r.contextOf(m.ID)
		}
	}
	r.mu.Lock()
	r.modelsCache[p.Name] = result.Data
	if len(result.Data) > 0 {
		r.modelsByProvider[p.Name] = p.Model
	}
	r.mu.Unlock()
	return result.Data, nil
}

func (r *ModelRegistry) contextOf(id string) int {
	if w, ok := knownContextWindows[strings.ToLower(id)]; ok {
		return w
	}
	// 未知模型回退到配置的上下文窗口
	if r.cfg != nil && r.cfg.ContextWindow > 0 {
		return r.cfg.ContextWindow
	}
	return 0
}

// List 当前 provider 的模型列表
func (r *ModelRegistry) List() []ModelInfo {
	p, err := r.CurrentProvider()
	if err != nil {
		return nil
	}
	r.mu.RLock()
	out := make([]ModelInfo, len(r.modelsCache[p.Name]))
	copy(out, r.modelsCache[p.Name])
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// modelIDs 提取模型 ID 列表
func modelIDs(models []ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

// HasFetched 当前 provider 是否已拉取
func (r *ModelRegistry) HasFetched() bool {
	p, err := r.CurrentProvider()
	if err != nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.modelsCache[p.Name]) > 0
}

// HasProvider 是否已配置至少一个 provider
func (r *ModelRegistry) HasProvider() bool {
	if r.store == nil {
		return false
	}
	d, err := r.store.Load()
	return err == nil && len(d.Providers) > 0
}

// Render 格式化当前 provider 模型列表
func (r *ModelRegistry) Render() string {
	p, err := r.CurrentProvider()
	if err != nil {
		return "尚未配置模型提供商"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔌 当前提供商: %s\n", p.Name))
	b.WriteString("🤖 可用模型:\n")
	cur := p.Model
	r.mu.RLock()
	list := r.modelsCache[p.Name]
	r.mu.RUnlock()
	if len(list) == 0 {
		b.WriteString("(尚未拉取, 请稍后重试)\n")
		b.WriteString(fmt.Sprintf("当前模型: %s\n", cur))
		return b.String()
	}
	for _, m := range list {
		marker := "  "
		if m.ID == cur {
			marker = "▶"
		}
		win := m.ContextWindow
		if win == 0 {
			win = r.ContextWindow()
		}
		b.WriteString(fmt.Sprintf("%s %s — 上下文 %d tokens\n", marker, m.ID, win))
	}
	b.WriteString("\n使用 /model <ID> 切换模型, /provider 管理提供商")
	return b.String()
}

// RenderProviders 格式化所有提供商
func (r *ModelRegistry) RenderProviders() string {
	if r.store == nil {
		return "尚未配置模型提供商(provider store 未初始化)\n\n添加方法:\n/provider add <名称> <BaseURL> <APIKey>\n示例: /provider add deepseek https://api.deepseek.com/v1 sk-xxx"
	}
	d, err := r.store.Load()
	if err != nil {
		return "读取提供商失败: " + err.Error()
	}
	if len(d.Providers) == 0 {
		return "尚未配置任何模型提供商\n\n添加方法:\n/provider add <名称> <BaseURL> <APIKey>\n示例: /provider add deepseek https://api.deepseek.com/v1 sk-xxx"
	}
	var b strings.Builder
	b.WriteString("🔌 模型提供商:\n")
	for _, p := range d.Providers {
		marker := "  "
		if p.Name == d.Current {
			marker = "▶"
		}
		model := p.Model
		if model == "" {
			model = "(未设置, 用 /model 选择)"
		}
		proxyTxt := ""
		if p.Proxy != "" {
			proxyTxt = " | 代理: " + p.Proxy
		}
		b.WriteString(fmt.Sprintf("%s %s — %s | 模型: %s%s\n", marker, p.Name, p.BaseURL, model, proxyTxt))
	}
	b.WriteString("\n/provider use <名称> 切换\n/provider add <名称> <BaseURL> <APIKey> [代理] 添加\n/provider proxy <名称> <代理URL> 设置代理\n/provider remove <名称> 删除")
	return b.String()
}
