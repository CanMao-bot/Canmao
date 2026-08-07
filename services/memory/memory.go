package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"gobot/core"
	"gobot/store/memory"
)

// Manager 记忆管理器: 记忆写入 + 向量检索
type Manager struct {
	store  *memory.Store
	embed  *Embedder
	cfg    *core.Config
}

func New(st *memory.Store, e *Embedder, cfg *core.Config) *Manager {
	return &Manager{store: st, embed: e, cfg: cfg}
}

func (m *Manager) Name() string { return "memory" }

// Save 保存一条记忆(提取自对话)
func (m *Manager) Save(ctx context.Context, scope string, groupID, userID int64, content string) error {
	if m.embed == nil || strings.TrimSpace(content) == "" {
		return nil
	}
	vec, err := m.embed.Embed(ctx, content)
	if err != nil {
		return err
	}
	_, err = m.store.Add(&memory.Entry{
		Scope: scope, GroupID: groupID, UserID: userID,
		Content: content, Vector: vec,
		CreatedAt: time.Now().Unix(), LastUsed: time.Now().Unix(),
	})
	return err
}

// Recall 检索相关记忆(本作用域 + 全局), 返回排序后的记忆文本
func (m *Manager) Recall(ctx context.Context, scope string, groupID, userID int64, query string, limit int) (string, error) {
	if m.embed == nil {
		return "", nil
	}
	qvec, err := m.embed.Embed(ctx, query)
	if err != nil {
		return "", err
	}

	type scored struct {
		e *memory.Entry
		s float64
	}

	var candidates []*scored
	added := map[int64]bool{}

	// 本作用域记忆(优先)
	local, err := m.store.ListByScope(scope, groupID, userID)
	if err == nil {
		for _, e := range local {
			if added[e.ID] {
				continue
			}
			added[e.ID] = true
			candidates = append(candidates, &scored{e, CosineSimilarity(qvec, e.Vector)})
		}
	}
	// 全局记忆(用户维度, 跨群共享个人记忆)
	all, err := m.store.ListAll()
	if err == nil {
		for _, e := range all {
			if e.Scope == "private" && e.UserID == userID {
				if added[e.ID] {
					continue
				}
				added[e.ID] = true
				candidates = append(candidates, &scored{e, CosineSimilarity(qvec, e.Vector) * 0.85})
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].s > candidates[j].s })

	var b strings.Builder
	n := 0
	for _, c := range candidates {
		if c.s < 0.4 {
			continue // 相似度阈值
		}
		if n >= limit {
			break
		}
		b.WriteString("- " + c.e.Content + "\n")
		m.store.Touch(c.e.ID)
		n++
	}
	return b.String(), nil
}

// Handle 实现 core.Service(记忆在 AI 服务内部调用, 这里返回 false 不消费)
func (m *Manager) Handle(ctx context.Context, bot *core.Bot, ev *core.Event) bool { return false }
