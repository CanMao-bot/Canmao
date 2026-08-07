package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Provider 一个模型提供商
type Provider struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`  // 当前使用的模型
	Proxy   string `json:"proxy"`  // 可选 HTTP/SOCKS5 代理, 如 http://127.0.0.1:7890
}

// Data 持久化数据
type Data struct {
	Current  string     `json:"current"`  // 当前提供商名
	Providers []Provider `json:"providers"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dataDir, "providers.json")}, nil
}

func (s *Store) Load() (*Data, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Data{}, nil
		}
		return nil, err
	}
	d := &Data{}
	if err := json.Unmarshal(data, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Store) Save(d *Data) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
