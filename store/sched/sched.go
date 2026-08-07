package sched

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

// Task 定时任务
type Task struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`      // once=一次性 / repeat=重复
	Content   string `json:"content"`   // 要发送的内容
	Scope     string `json:"scope"`     // group / private
	GroupID   int64  `json:"group_id"`
	UserID    int64  `json:"user_id"`
	TargetAt  int64  `json:"target_at"` // 首次触发时间(unix)
	Interval  int64  `json:"interval"`  // repeat 间隔秒
	Enabled   bool   `json:"enabled"`
	RunCount  int    `json:"run_count"`
	CreatedAt int64  `json:"created_at"`
}

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "sched.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开定时数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		content TEXT NOT NULL,
		scope TEXT NOT NULL,
		group_id INTEGER NOT NULL DEFAULT 0,
		user_id INTEGER NOT NULL DEFAULT 0,
		target_at INTEGER DEFAULT 0,
		interval INTEGER DEFAULT 0,
		enabled INTEGER DEFAULT 1,
		run_count INTEGER DEFAULT 0,
		created_at INTEGER DEFAULT 0
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化定时表: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(t *Task) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(
		`INSERT INTO tasks(type, content, scope, group_id, user_id, target_at, interval, enabled, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.Type, t.Content, t.Scope, t.GroupID, t.UserID, t.TargetAt, t.Interval, boolToInt(t.Enabled), t.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Due 获取到期的启用任务
func (s *Store) Due(now int64) ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, type, content, scope, group_id, user_id, target_at, interval, enabled, run_count, created_at
		 FROM tasks WHERE enabled=1 AND target_at<=?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// List 列出某用户的所有任务
func (s *Store) List(scope string, groupID, userID int64) ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT id, type, content, scope, group_id, user_id, target_at, interval, enabled, run_count, created_at
		 FROM tasks WHERE scope=? AND group_id=? AND user_id=? ORDER BY target_at ASC`, scope, groupID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTarget 更新下次触发时间(重复任务用)
func (s *Store) UpdateTarget(id, next int64, runCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE tasks SET target_at=?, run_count=? WHERE id=?`, next, runCount, id)
	return err
}

// Disable 停用任务
func (s *Store) Disable(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE tasks SET enabled=0 WHERE id=?`, id)
	return err
}

// Delete 删除任务
func (s *Store) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id=?`, id)
	return err
}

func scanTask(r interface{ Scan(...interface{}) error }) (*Task, error) {
	var t Task
	var enabled int
	var runCount int
	if err := r.Scan(&t.ID, &t.Type, &t.Content, &t.Scope, &t.GroupID, &t.UserID, &t.TargetAt, &t.Interval, &enabled, &runCount, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.Enabled = enabled == 1
	t.RunCount = runCount
	return &t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FormatTime 格式化时间为人类可读
func FormatTime(unix int64) string {
	return time.Unix(unix, 0).Format("2006-01-02 15:04")
}

var _ = json.Marshal
