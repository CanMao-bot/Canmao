package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Time    int64  `json:"time"`
}

type Session struct {
	ID        int64
	Scope     string
	GroupID   int64
	UserID    int64
	Title     string
	Summary   string
	Messages  []Message
	CreatedAt int64
	UpdatedAt int64
}

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "session.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开会话数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrateLegacy(); err != nil {
		db.Close()
		return nil, err
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scope TEXT NOT NULL,
			group_id INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER NOT NULL DEFAULT 0,
			title TEXT DEFAULT '',
			summary TEXT DEFAULT '',
			messages TEXT DEFAULT '[]',
			created_at INTEGER DEFAULT 0,
			updated_at INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS session_state (
			scope TEXT NOT NULL,
			group_id INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER NOT NULL DEFAULT 0,
			current_id INTEGER DEFAULT 0,
			PRIMARY KEY(scope, group_id, user_id)
		)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("初始化会话表: %w", err)
		}
	}
	return s, nil
}

// migrateLegacy 将旧版 sessions 表(key/messages/updated_at)迁移到新结构
func (s *Store) migrateLegacy() error {
	rows, err := s.db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return err
	}
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		cols[name] = true
	}
	rows.Close()

	// 新结构含 scope 列, 旧结构含 key 列
	if cols["scope"] {
		return nil // 已是新结构
	}
	if !cols["key"] {
		return nil // 无 sessions 表或未知结构, 交由后续 CREATE 处理
	}

	// 备份并读取旧数据
	if _, err := s.db.Exec(`ALTER TABLE sessions RENAME TO sessions_legacy`); err != nil {
		return fmt.Errorf("迁移: 重命名旧表失败: %w", err)
	}

	type legacyRow struct {
		key      string
		messages string
		updated  int64
	}
	var legacy []legacyRow
	rs, err := s.db.Query(`SELECT key, messages, updated_at FROM sessions_legacy`)
	if err != nil {
		return fmt.Errorf("迁移: 读取旧数据失败: %w", err)
	}
	for rs.Next() {
		var r legacyRow
		if err := rs.Scan(&r.key, &r.messages, &r.updated); err != nil {
			rs.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	rs.Close()

	// 重建新表
	createSQL := `CREATE TABLE IF NOT EXISTS sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		scope TEXT NOT NULL,
		group_id INTEGER NOT NULL DEFAULT 0,
		user_id INTEGER NOT NULL DEFAULT 0,
		title TEXT DEFAULT '',
		summary TEXT DEFAULT '',
		messages TEXT DEFAULT '[]',
		created_at INTEGER DEFAULT 0,
		updated_at INTEGER DEFAULT 0
	)`
	if _, err := s.db.Exec(createSQL); err != nil {
		return fmt.Errorf("迁移: 重建表失败: %w", err)
	}

	// 插入旧数据, key 格式: g<group>_u<user> 或 p<user>
	for _, r := range legacy {
		scope, gid, uid := parseLegacyKey(r.key)
		now := r.updated
		if now == 0 {
			now = time.Now().Unix()
		}
		res, err := s.db.Exec(
			`INSERT INTO sessions(scope, group_id, user_id, title, messages, created_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
			scope, gid, uid, "", r.messages, now, now)
		if err != nil {
			return fmt.Errorf("迁移: 插入数据失败: %w", err)
		}
		id, _ := res.LastInsertId()
		// 记录为当前会话
		_, _ = s.db.Exec(`INSERT OR REPLACE INTO session_state(scope, group_id, user_id, current_id) VALUES(?,?,?,?)`,
			scope, gid, uid, id)
	}

	// 删除旧表
	if _, err := s.db.Exec(`DROP TABLE sessions_legacy`); err != nil {
		return fmt.Errorf("迁移: 删除旧表失败: %w", err)
	}
	fmt.Printf("[session] 已迁移 %d 条旧会话记录\n", len(legacy))
	return nil
}

// parseLegacyKey 解析旧 key: g<group>_u<user> 或 p<user>
func parseLegacyKey(key string) (scope string, groupID, userID int64) {
	if strings.HasPrefix(key, "g") {
		// g<group>_u<user>
		rest := strings.TrimPrefix(key, "g")
		if idx := strings.Index(rest, "_u"); idx >= 0 {
			gid, _ := strconv.ParseInt(rest[:idx], 10, 64)
			uid, _ := strconv.ParseInt(rest[idx+2:], 10, 64)
			return "group", gid, uid
		}
	}
	if strings.HasPrefix(key, "p") {
		uid, _ := strconv.ParseInt(strings.TrimPrefix(key, "p"), 10, 64)
		return "private", 0, uid
	}
	return "private", 0, 0
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) create(scope string, groupID, userID int64, title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`INSERT INTO sessions(scope, group_id, user_id, title, messages, created_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
		scope, groupID, userID, title, "[]", now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	// 设为当前会话
	_, err = s.db.Exec(`INSERT OR REPLACE INTO session_state(scope, group_id, user_id, current_id) VALUES(?,?,?,?)`,
		scope, groupID, userID, id)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Scope: scope, GroupID: groupID, UserID: userID, Title: title, Messages: []Message{}, CreatedAt: now, UpdatedAt: now}, nil
}

// GetCurrent 获取当前会话, 不存在则创建
func (s *Store) GetCurrent(scope string, groupID, userID int64, defaultTitle string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var curID int64
	err := s.db.QueryRow(`SELECT current_id FROM session_state WHERE scope=? AND group_id=? AND user_id=?`,
		scope, groupID, userID).Scan(&curID)
	if err == sql.ErrNoRows || curID == 0 {
		return s.createLocked(scope, groupID, userID, defaultTitle)
	}
	ses, err := s.getByIDLocked(curID)
	if err != nil || ses == nil {
		return s.createLocked(scope, groupID, userID, defaultTitle)
	}
	return ses, nil
}

// Create 新建一个会话并切换为当前
func (s *Store) Create(scope string, groupID, userID int64, title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(scope, groupID, userID, title)
}

func (s *Store) createLocked(scope string, groupID, userID int64, title string) (*Session, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`INSERT INTO sessions(scope, group_id, user_id, title, messages, created_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
		scope, groupID, userID, title, "[]", now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, err = s.db.Exec(`INSERT OR REPLACE INTO session_state(scope, group_id, user_id, current_id) VALUES(?,?,?,?)`,
		scope, groupID, userID, id)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, Scope: scope, GroupID: groupID, UserID: userID, Title: title, Messages: []Message{}, CreatedAt: now, UpdatedAt: now}, nil
}

// List 列出该用户的所有会话(不含消息, 仅元数据)
func (s *Store) List(scope string, groupID, userID int64) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT id, scope, group_id, user_id, title, summary, created_at, updated_at FROM sessions
		 WHERE scope=? AND group_id=? AND user_id=? ORDER BY updated_at DESC`,
		scope, groupID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var se Session
		if err := rows.Scan(&se.ID, &se.Scope, &se.GroupID, &se.UserID, &se.Title, &se.Summary, &se.CreatedAt, &se.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// Get 按 ID 获取完整会话(含消息)
func (s *Store) Get(id int64) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getByIDLocked(id)
}

func (s *Store) getByIDLocked(id int64) (*Session, error) {
	var se Session
	var raw string
	err := s.db.QueryRow(
		`SELECT id, scope, group_id, user_id, title, summary, messages, created_at, updated_at FROM sessions WHERE id=?`,
		id).Scan(&se.ID, &se.Scope, &se.GroupID, &se.UserID, &se.Title, &se.Summary, &raw, &se.CreatedAt, &se.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &se.Messages); err != nil {
		se.Messages = []Message{}
	}
	return &se, nil
}

// Switch 切换当前会话
func (s *Store) Switch(scope string, groupID, userID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR REPLACE INTO session_state(scope, group_id, user_id, current_id) VALUES(?,?,?,?)`,
		scope, groupID, userID, id)
	return err
}

// Append 追加消息到指定会话
func (s *Store) Append(id int64, msgs []Message, maxWindow int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.getByIDLocked(id)
	if err != nil || se == nil {
		return fmt.Errorf("会话不存在: %w", err)
	}
	se.Messages = append(se.Messages, msgs...)
	if maxWindow > 0 && len(se.Messages) > maxWindow {
		se.Messages = se.Messages[len(se.Messages)-maxWindow:]
	}
	raw, _ := json.Marshal(se.Messages)
	_, err = s.db.Exec(`UPDATE sessions SET messages=?, updated_at=? WHERE id=?`,
		string(raw), time.Now().Unix(), id)
	return err
}

// SetSummary 合并新摘要进 summary
func (s *Store) SetSummary(id int64, newSummary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.getByIDLocked(id)
	if err != nil || se == nil {
		return fmt.Errorf("会话不存在: %w", err)
	}
	combined := se.Summary
	if combined != "" && newSummary != "" {
		combined += "\n"
	}
	combined += newSummary
	_, err = s.db.Exec(`UPDATE sessions SET summary=?, updated_at=? WHERE id=?`,
		combined, time.Now().Unix(), id)
	return err
}

// ReplaceMessages 用新消息窗口替换(压缩后只保留最近消息)
func (s *Store) ReplaceMessages(id int64, msgs []Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(msgs)
	_, err := s.db.Exec(`UPDATE sessions SET messages=?, updated_at=? WHERE id=?`,
		string(raw), time.Now().Unix(), id)
	return err
}

// Rename 设置会话标题
func (s *Store) Rename(id int64, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE sessions SET title=?, updated_at=? WHERE id=?`, title, time.Now().Unix(), id)
	return err
}

// Delete 删除会话
func (s *Store) Delete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	return err
}
