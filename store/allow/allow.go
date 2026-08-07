package allow

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Entry 一条永久信任记录
type Entry struct {
	ID        int64
	Scope     string // "group" / "private"
	GroupID   int64
	UserID    int64
	ToolName  string
	CreatedAt int64
}

// Store 管理"以后都允许"的免审批记录
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "allow.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 allow 数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS allowlist (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope TEXT NOT NULL,
		group_id INTEGER NOT NULL DEFAULT 0,
		user_id INTEGER NOT NULL DEFAULT 0,
		tool_name TEXT NOT NULL,
		created_at INTEGER DEFAULT 0,
		UNIQUE(scope, group_id, user_id, tool_name)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 allowlist 表: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// IsAllowed 检查该作用域下某工具是否已被永久允许
func (s *Store) IsAllowed(scope string, groupID, userID int64, toolName string) bool {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM allowlist WHERE scope=? AND group_id=? AND user_id=? AND tool_name=?`,
		scope, groupID, userID, toolName).Scan(&id)
	return err == nil
}

// Add 记录永久允许
func (s *Store) Add(scope string, groupID, userID int64, toolName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO allowlist(scope, group_id, user_id, tool_name, created_at) VALUES(?,?,?,?,?)`,
		scope, groupID, userID, toolName, time.Now().Unix())
	return err
}

// Revoke 移除永久允许
func (s *Store) Revoke(scope string, groupID, userID int64, toolName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`DELETE FROM allowlist WHERE scope=? AND group_id=? AND user_id=? AND tool_name=?`,
		scope, groupID, userID, toolName)
	return err
}

// ListByTool 列出某工具名下的所有信任记录
func (s *Store) ListByTool(toolName string) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT id, scope, group_id, user_id, tool_name, created_at FROM allowlist WHERE tool_name=?`, toolName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Scope, &e.GroupID, &e.UserID, &e.ToolName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllList 列出某作用域下该用户的所有信任记录
func (s *Store) AllList(scope string, groupID, userID int64) ([]Entry, error) {
	rows, err := s.db.Query(
		`SELECT id, scope, group_id, user_id, tool_name, created_at FROM allowlist WHERE scope=? AND group_id=? AND user_id=?`,
		scope, groupID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Scope, &e.GroupID, &e.UserID, &e.ToolName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
