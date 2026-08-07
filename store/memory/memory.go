package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Entry 一条记忆
type Entry struct {
	ID        int64   `json:"id"`
	Scope     string  `json:"scope"` // group / private
	GroupID   int64   `json:"group_id"`
	UserID    int64   `json:"user_id"`
	Content   string  `json:"content"`
	Vector    []float64 `json:"vector"`
	CreatedAt int64   `json:"created_at"`
	LastUsed  int64   `json:"last_used"`
	UseCount  int     `json:"use_count"`
}

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "memory.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开记忆数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS memories (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope TEXT NOT NULL,
		group_id INTEGER NOT NULL DEFAULT 0,
		user_id INTEGER NOT NULL DEFAULT 0,
		content TEXT NOT NULL,
		vector TEXT NOT NULL,
		created_at INTEGER DEFAULT 0,
		last_used INTEGER DEFAULT 0,
		use_count INTEGER DEFAULT 0
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化记忆表: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_mem_scope ON memories(scope, group_id, user_id)`); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Add 添加记忆
func (s *Store) Add(e *Entry) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := json.Marshal(e.Vector)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO memories(scope, group_id, user_id, content, vector, created_at, last_used, use_count) VALUES(?,?,?,?,?,?,?,?)`,
		e.Scope, e.GroupID, e.UserID, e.Content, string(v), e.CreatedAt, e.LastUsed, e.UseCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListByScope 列出某作用域所有记忆(含向量)
func (s *Store) ListByScope(scope string, groupID, userID int64) ([]*Entry, error) {
	rows, err := s.db.Query(
		`SELECT id, scope, group_id, user_id, content, vector, created_at, last_used, use_count FROM memories WHERE scope=? AND group_id=? AND user_id=?`,
		scope, groupID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListAll 列出全部记忆(全局, 供跨群检索)
func (s *Store) ListAll() ([]*Entry, error) {
	rows, err := s.db.Query(`SELECT id, scope, group_id, user_id, content, vector, created_at, last_used, use_count FROM memories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Touch 更新使用时间和次数
func (s *Store) Touch(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE memories SET last_used=?, use_count=use_count+1 WHERE id=?`, time.Now().Unix(), id)
	return err
}

// Delete 删除记忆
func (s *Store) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM memories WHERE id=?`, id)
	return err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanEntry(r scanner) (*Entry, error) {
	var e Entry
	var v string
	var useCount int
	if err := r.Scan(&e.ID, &e.Scope, &e.GroupID, &e.UserID, &e.Content, &v, &e.CreatedAt, &e.LastUsed, &useCount); err != nil {
		return nil, err
	}
	e.UseCount = useCount
	_ = json.Unmarshal([]byte(v), &e.Vector)
	return &e, nil
}
