package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Entry 一条记忆
type Entry struct {
	ID              int64     `json:"id"`
	Scope           string    `json:"scope"` // group / private
	GroupID         int64     `json:"group_id"`
	UserID          int64     `json:"user_id"`
	Content         string    `json:"content"`
	Vector          []float64 `json:"vector"`
	CreatedAt       int64     `json:"created_at"`
	LastUsed        int64     `json:"last_used"`
	UseCount        int       `json:"use_count"`
	FactKey         string    `json:"fact_key"`
	UpdatedAt       int64     `json:"updated_at"`
	SourceMessageID int64     `json:"source_message_id"`
	VectorModel     string    `json:"vector_model"`
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
	rows, err := db.Query(`PRAGMA table_info(memories)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &nn, &dflt, &pk); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, col := range []struct{ name, typ string }{{"fact_key", "TEXT NOT NULL DEFAULT ''"}, {"updated_at", "INTEGER NOT NULL DEFAULT 0"}, {"source_message_id", "INTEGER NOT NULL DEFAULT 0"}, {"vector_model", "TEXT NOT NULL DEFAULT ''"}} {
		if !columns[col.name] {
			if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN ` + col.name + ` ` + col.typ); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_fact ON memories(scope,group_id,user_id,fact_key) WHERE fact_key<>''`); err != nil {
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
	if e.FactKey == "" {
		e.FactKey = ContentKey(e.Content)
	}
	if e.UpdatedAt == 0 {
		e.UpdatedAt = time.Now().Unix()
	}
	res, err := s.db.Exec(
		`INSERT INTO memories(scope,group_id,user_id,content,vector,created_at,last_used,use_count,fact_key,updated_at,source_message_id,vector_model) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scope,group_id,user_id,fact_key) WHERE fact_key<>'' DO UPDATE SET content=excluded.content,vector=excluded.vector,updated_at=excluded.updated_at,source_message_id=excluded.source_message_id,vector_model=excluded.vector_model`,
		e.Scope, e.GroupID, e.UserID, e.Content, string(v), e.CreatedAt, e.LastUsed, e.UseCount, e.FactKey, e.UpdatedAt, e.SourceMessageID, e.VectorModel)
	if err != nil {
		return 0, err
	}
	_ = res
	var id int64
	err = s.db.QueryRow(`SELECT id FROM memories WHERE scope=? AND group_id=? AND user_id=? AND fact_key=?`, e.Scope, e.GroupID, e.UserID, e.FactKey).Scan(&id)
	return id, err
}

func ContentKey(content string) string {
	return fmt.Sprintf("note:%x", sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(content), " ")))))
}

const entryColumns = `id,scope,group_id,user_id,content,vector,created_at,last_used,use_count,fact_key,updated_at,source_message_id,vector_model`

func (s *Store) Recent(ctx context.Context, scope string, groupID, userID int64, limit int) ([]*Entry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+entryColumns+` FROM memories WHERE scope=? AND group_id=? AND user_id=? ORDER BY updated_at DESC,id DESC LIMIT ?`, scope, groupID, userID, limit)
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

func (s *Store) FindFact(ctx context.Context, scope string, gid, uid int64, key string) (*Entry, error) {
	e, err := scanEntry(s.db.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM memories WHERE scope=? AND group_id=? AND user_id=? AND fact_key=?`, scope, gid, uid, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

func (s *Store) DeleteScoped(ctx context.Context, scope string, gid, uid, id int64) (bool, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE scope=? AND group_id=? AND user_id=? AND id=?`, scope, gid, uid, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// ListByScope 列出某作用域所有记忆(含向量)
func (s *Store) ListByScope(scope string, groupID, userID int64) ([]*Entry, error) {
	rows, err := s.db.Query(
		`SELECT `+entryColumns+` FROM memories WHERE scope=? AND group_id=? AND user_id=?`,
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
	rows, err := s.db.Query(`SELECT ` + entryColumns + ` FROM memories`)
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
	if err := r.Scan(&e.ID, &e.Scope, &e.GroupID, &e.UserID, &e.Content, &v, &e.CreatedAt, &e.LastUsed, &useCount, &e.FactKey, &e.UpdatedAt, &e.SourceMessageID, &e.VectorModel); err != nil {
		return nil, err
	}
	e.UseCount = useCount
	_ = json.Unmarshal([]byte(v), &e.Vector)
	return &e, nil
}
