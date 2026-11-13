package memory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"gobot/core"
	"gobot/store/memory"
)

// Manager combines bounded, scope-local lexical retrieval with optional vectors.
// Failed embedding providers never discard otherwise valid facts.
type Manager struct {
	store        *memory.Store
	embed        *Embedder
	cfg          *core.Config
	mu           sync.Mutex
	embedRetryAt time.Time
}

func New(st *memory.Store, e *Embedder, cfg *core.Config) *Manager {
	return &Manager{store: st, embed: e, cfg: cfg}
}
func (m *Manager) Name() string { return "memory" }

var credentialPattern = regexp.MustCompile(`(?i)(\b(sk-|gh[pousr]_|github_pat_)[a-z0-9_-]{12,}|(?:api[_ -]?key|(?:access[_ -]?)?token|password|cookie|密码|密钥|令牌)\s*[:：=是为]|-----BEGIN .*PRIVATE KEY-----)`)
var factKeyPattern = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_.:-]{0,119}$`)

func ValidContent(content string) error {
	n := len([]rune(strings.TrimSpace(content)))
	if n < 2 || n > 600 {
		return fmt.Errorf("记忆需为 2 到 600 字的简短事实")
	}
	if credentialPattern.MatchString(content) {
		return fmt.Errorf("账号凭据、密码和密钥不能写入长期记忆")
	}
	return nil
}
func validScope(scope string, gid, uid int64) error {
	if uid <= 0 || (scope == "group" && gid <= 0) || (scope == "private" && gid != 0) || (scope != "group" && scope != "private") {
		return fmt.Errorf("记忆必须绑定真实用户及当前会话")
	}
	return nil
}
func (m *Manager) vector(ctx context.Context, text string) ([]float64, string) {
	if m.embed == nil || !m.embed.Configured() {
		return nil, ""
	}
	m.mu.Lock()
	retry := m.embedRetryAt
	m.mu.Unlock()
	if time.Now().Before(retry) {
		return nil, ""
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	vec, err := m.embed.Embed(callCtx, text)
	if err != nil {
		m.mu.Lock()
		m.embedRetryAt = time.Now().Add(time.Minute)
		m.mu.Unlock()
		return nil, ""
	}
	return vec, fmt.Sprintf("%x", sha256.Sum256([]byte(m.embed.cfg.BaseURL+"\n"+m.embed.cfg.Model)))
}
func (m *Manager) Save(ctx context.Context, scope string, gid, uid int64, content string) error {
	return m.SaveFact(ctx, scope, gid, uid, content, "", 0)
}
func (m *Manager) SaveFact(ctx context.Context, scope string, gid, uid int64, content, key string, messageID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validScope(scope, gid, uid); err != nil {
		return err
	}
	content = strings.Join(strings.Fields(content), " ")
	if err := ValidContent(content); err != nil {
		return err
	}
	if key == "" {
		key = memory.ContentKey(content)
	}
	if !factKeyPattern.MatchString(key) || credentialPattern.MatchString(key) {
		return fmt.Errorf("记忆键只能包含字母、数字及 ._:-，不允许凭据")
	}
	old, err := m.store.FindFact(ctx, scope, gid, uid, key)
	if err != nil {
		return err
	}
	if old != nil && memory.ContentKey(old.Content) == memory.ContentKey(content) {
		return nil
	}
	vec, model := m.vector(ctx, content)
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = m.store.Add(&memory.Entry{Scope: scope, GroupID: gid, UserID: uid, Content: content, FactKey: key, Vector: vec, VectorModel: model, CreatedAt: now, UpdatedAt: now, LastUsed: now, SourceMessageID: messageID})
	return err
}

// Han bigrams support Chinese without a separate tokenizer service.
func memoryTerms(text string) map[string]bool {
	terms := map[string]bool{}
	var word []rune
	var previous rune
	flush := func() {
		if len(word) > 1 {
			terms[string(word)] = true
		}
		word = nil
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			if previous != 0 {
				terms[string([]rune{previous, r})] = true
			}
			previous = r
			continue
		}
		previous = 0
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}
func lexicalScore(query, content string) float64 {
	a, b := memoryTerms(query), memoryTerms(content)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	hits := 0
	for term := range a {
		if b[term] {
			hits++
		}
	}
	return float64(hits) / math.Sqrt(float64(len(a)*len(b)))
}
func (m *Manager) Recall(ctx context.Context, scope string, gid, uid int64, query string, limit int) (string, error) {
	if err := validScope(scope, gid, uid); err != nil {
		return "", err
	}
	if strings.TrimSpace(query) == "" {
		return "", nil
	}
	if runes := []rune(query); len(runes) > 2000 {
		query = string(runes[:2000])
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	entries, err := m.store.Recent(ctx, scope, gid, uid, 500)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	qvec, model := m.vector(ctx, query)
	type scored struct {
		e     *memory.Entry
		score float64
	}
	var found []scored
	for _, e := range entries {
		if ValidContent(e.Content) != nil || strings.HasPrefix(e.Content, "[对话]") {
			continue
		}
		lexical := lexicalScore(query, e.Content)
		score := lexical
		if (e.FactKey == "profile:name" && (strings.Contains(query, "名字") || strings.Contains(query, "我叫什么"))) || (e.FactKey == "profile:residence" && (strings.Contains(query, "住哪") || strings.Contains(query, "住在"))) {
			score = math.Max(score, 0.55)
		}
		if len(qvec) > 0 && e.VectorModel == model {
			cos := CosineSimilarity(qvec, e.Vector)
			if cos >= 0.45 {
				score = math.Max(score, 0.75*cos+0.25*lexical)
			}
		}
		if score < 0.18 {
			continue
		}
		age := time.Since(time.Unix(max(e.UpdatedAt, e.CreatedAt), 0)).Hours() / 24
		score *= 0.92 + 0.08*math.Exp(-math.Max(age, 0)/90)
		found = append(found, scored{e, score})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score == found[j].score {
			return found[i].e.ID > found[j].e.ID
		}
		return found[i].score > found[j].score
	})
	var b strings.Builder
	count := 0
	seen := map[string]bool{}
	for _, candidate := range found {
		e := candidate.e
		key := memory.ContentKey(e.Content)
		if seen[key] {
			continue
		}
		line := fmt.Sprintf("- [记忆:%d 用户:%d 更新:%s] %s\n", e.ID, e.UserID, time.Unix(max(e.UpdatedAt, e.CreatedAt), 0).Format("2006-01-02"), e.Content)
		if len([]rune(b.String()))+len([]rune(line)) > 1800 {
			break
		}
		b.WriteString(line)
		seen[key] = true
		_ = m.store.Touch(e.ID)
		count++
		if count >= limit {
			break
		}
	}
	return b.String(), ctx.Err()
}
func (m *Manager) List(ctx context.Context, scope string, gid, uid int64, limit int) ([]*memory.Entry, error) {
	if err := validScope(scope, gid, uid); err != nil {
		return nil, err
	}
	return m.store.Recent(ctx, scope, gid, uid, limit)
}
func (m *Manager) Forget(ctx context.Context, scope string, gid, uid, id int64) (bool, error) {
	if err := validScope(scope, gid, uid); err != nil {
		return false, err
	}
	return m.store.DeleteScoped(ctx, scope, gid, uid, id)
}
func (m *Manager) Handle(context.Context, *core.Bot, *core.Event) bool { return false }
