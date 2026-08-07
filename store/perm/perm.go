package perm

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleBanned = "banned"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "perm.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开权限数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			user_id INTEGER PRIMARY KEY,
			role TEXT NOT NULL DEFAULT 'member',
			note TEXT DEFAULT '',
			updated_at INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS groups (
			group_id INTEGER PRIMARY KEY,
			name TEXT DEFAULT '',
			ai_enabled INTEGER DEFAULT 1,
			bot_enabled INTEGER DEFAULT 1,
			updated_at INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS group_members (
			group_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL DEFAULT 'member',
			PRIMARY KEY (group_id, user_id)
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("初始化权限表: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) GetUserRole(userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM users WHERE user_id=?`, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return RoleMember, nil
	}
	if err != nil {
		return RoleMember, err
	}
	return role, nil
}

func (s *Store) SetUserRole(userID int64, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO users(user_id, role, updated_at) VALUES(?,?,unixepoch())
		ON CONFLICT(user_id) DO UPDATE SET role=?, updated_at=unixepoch()`,
		userID, role, role)
	return err
}

func (s *Store) GetGroupRole(groupID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM group_members WHERE group_id=? AND user_id=?`, groupID, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return RoleMember, nil
	}
	if err != nil {
		return RoleMember, err
	}
	return role, nil
}

func (s *Store) SetGroupRole(groupID, userID int64, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO group_members(group_id, user_id, role) VALUES(?,?,?)
		ON CONFLICT(group_id, user_id) DO UPDATE SET role=?`,
		groupID, userID, role, role)
	return err
}

func (s *Store) GroupEnabled(groupID int64) bool {
	var v int
	err := s.db.QueryRow(`SELECT ai_enabled FROM groups WHERE group_id=?`, groupID).Scan(&v)
	if err == sql.ErrNoRows {
		return false // 默认关闭 AI
	}
	if err != nil || v == 0 {
		return false
	}
	return true
}

func (s *Store) SetGroupAI(groupID int64, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`INSERT INTO groups(group_id, ai_enabled, updated_at) VALUES(?,?,unixepoch())
		ON CONFLICT(group_id) DO UPDATE SET ai_enabled=?, updated_at=unixepoch()`,
		groupID, v, v)
	return err
}

func (s *Store) SetGroupBot(groupID int64, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.Exec(`INSERT INTO groups(group_id, bot_enabled, updated_at) VALUES(?,?,unixepoch())
		ON CONFLICT(group_id) DO UPDATE SET bot_enabled=?, updated_at=unixepoch()`,
		groupID, v, v)
	return err
}

func (s *Store) BotEnabled(groupID int64) bool {
	var v int
	err := s.db.QueryRow(`SELECT bot_enabled FROM groups WHERE group_id=?`, groupID).Scan(&v)
	if err == sql.ErrNoRows {
		return false // 默认群关闭
	}
	return err == nil && v != 0
}

// CanUseAI: 判断该用户在该群是否可用 AI
func (s *Store) CanUseAI(groupID, userID int64, isMaster bool) (bool, error) {
	if isMaster {
		return true, nil
	}
	role, err := s.GetUserRole(userID)
	if err != nil {
		return false, err
	}
	if role == RoleBanned {
		return false, nil
	}
	if !s.GroupEnabled(groupID) {
		return false, nil
	}
	return true, nil
}

func (s *Store) CanUseBot(groupID, userID int64, isMaster bool) (bool, error) {
	if isMaster {
		return true, nil
	}
	if !s.BotEnabled(groupID) {
		return false, nil
	}
	role, err := s.GetUserRole(userID)
	if err != nil {
		return false, err
	}
	return role != RoleBanned, nil
}
