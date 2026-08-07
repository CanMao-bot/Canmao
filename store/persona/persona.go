package persona

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Persona 人设状态(可自我优化)
type Persona struct {
	Base      string         `json:"base"`       // 基础人设(静态)
	Style     string         `json:"style"`      // 当前说话风格(可优化)
	Traits    []string       `json:"traits"`     // 性格特质(可优化)
	Rules     []string       `json:"rules"`      // 行为准则(可优化)
	Learnings []Learning     `json:"learnings"`  // 自我优化学习记录
	UpdatedAt int64          `json:"updated_at"`
}

// Learning 一条自我优化记录
type Learning struct {
	Content   string `json:"content"`   // 优化内容(如"用户更喜欢简短回复")
	Kind      string `json:"kind"`      // style / trait / rule
	Source    string `json:"source"`    // 来源(对话/用户反馈)
	CreatedAt int64  `json:"created_at"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dataDir, "persona.json")}, nil
}

func (s *Store) Load() (*Persona, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Persona{}, nil
		}
		return nil, err
	}
	p := &Persona{}
	if err := json.Unmarshal(data, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) Save(p *Persona) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.UpdatedAt = time.Now().Unix()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
