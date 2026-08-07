package group

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Model 用户组的模型覆盖配置
type Model struct {
	Master  string `json:"master"`  // 主人默认模型
	Admin   string `json:"admin"`   // 管理员默认模型
	Member  string `json:"member"`  // 普通成员默认模型
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dataDir, "model_groups.json")}, nil
}

func (s *Store) Load() (*Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Model{}, nil
		}
		return nil, err
	}
	m := &Model{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) Save(m *Model) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
