package mood

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State 全局心情状态
type State struct {
	Value     int       `json:"value"`     // 0-100, 50=中性
	Emotion   string    `json:"emotion"`   // happy/angry/sad/neutral/...
	Reason    string    `json:"reason"`    // 当前心情的原因
	UpdatedAt int64     `json:"updated_at"`
	History   []MoodLog `json:"history"`
}

// MoodLog 心情变化日志
type MoodLog struct {
	From    int    `json:"from"`
	To      int    `json:"to"`
	Reason  string `json:"reason"`
	Time    int64  `json:"time"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dataDir, "mood.json")}, nil
}

func (s *Store) Load() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Value: 50, Emotion: "neutral", UpdatedAt: time.Now().Unix()}, nil
		}
		return nil, err
	}
	st := &State{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Value == 0 && st.Emotion == "" {
		st.Value = 50
		st.Emotion = "neutral"
	}
	return st, nil
}

func (s *Store) Save(st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}
