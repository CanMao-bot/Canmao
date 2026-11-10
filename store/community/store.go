// Package community persists community policy and games independently of AI sessions.
package community

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const MaxMoney int64 = 1_000_000_000_000

type Plot struct {
	Crop  string `json:"crop"`
	Ready int64  `json:"ready"`
}
type Account struct {
	Coins       int64    `json:"coins"`
	Bank        int64    `json:"bank"`
	XP          int64    `json:"xp"`
	LastSign    string   `json:"last_sign"`
	Streak      int      `json:"streak"`
	Signs       int      `json:"signs"`
	SpeechDay   string   `json:"speech_day"`
	SpeechMonth string   `json:"speech_month"`
	Today       int64    `json:"today"`
	Month       int64    `json:"month"`
	Total       int64    `json:"total"`
	Rewarded    int      `json:"rewarded"`
	LastSpeech  int64    `json:"last_speech"`
	Plots       []Plot   `json:"plots"`
	Mounts      []string `json:"mounts"`
	Mount       string   `json:"mount"`
	Rod         string   `json:"rod"`
	Durability  int      `json:"durability"`
	BiteAt      int64    `json:"bite_at"`
	EscapeAt    int64    `json:"escape_at"`
	Fish        int64    `json:"fish"`
}
type Rule struct {
	Key      string `json:"key"`
	Text     string `json:"text"`
	Scene    string `json:"scene"` // group/private/all
	Fuzzy    bool   `json:"fuzzy"`
	MinLevel int    `json:"min_level"`
	Action   string `json:"action"` // reply/warn/ban/kick/recall/black
}
type Settings struct {
	PrivateRelay bool              `json:"private_relay"`
	Menus        map[string]string `json:"menus"`
	Mode         string            `json:"mode"`
	Features     map[string]bool   `json:"features"`
	Replies      []Rule            `json:"replies"`
	Keywords     []Rule            `json:"keywords"`
	Events       map[string]string `json:"events"`
}
type Ranked struct {
	UserID int64
	Value  int64
}
type Audit struct {
	At     int64
	Actor  int64
	Action string
	Detail string
}
type Store struct{ db *sql.DB }

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "community.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	for _, q := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS settings(scope INTEGER PRIMARY KEY, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS accounts(scope TEXT NOT NULL, user_id INTEGER NOT NULL, data TEXT NOT NULL, PRIMARY KEY(scope,user_id))`,
		`CREATE TABLE IF NOT EXISTS roles(scope INTEGER NOT NULL, user_id INTEGER NOT NULL, level INTEGER NOT NULL, PRIMARY KEY(scope,user_id))`,
		`CREATE TABLE IF NOT EXISTS lists(scope INTEGER NOT NULL, user_id INTEGER NOT NULL, kind TEXT NOT NULL, PRIMARY KEY(scope,user_id,kind))`,
		`CREATE TABLE IF NOT EXISTS selections(user_id INTEGER PRIMARY KEY, group_id INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS warnings(group_id INTEGER NOT NULL, user_id INTEGER NOT NULL, count INTEGER NOT NULL, PRIMARY KEY(group_id,user_id))`,
		`CREATE TABLE IF NOT EXISTS seen(key TEXT PRIMARY KEY, at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS seen_at ON seen(at)`,
		`CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, scope INTEGER NOT NULL, at INTEGER NOT NULL, actor INTEGER NOT NULL, action TEXT NOT NULL, detail TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_scope ON audit(scope,id)`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

func readSettings(q interface{ QueryRow(string, ...any) *sql.Row }, scope int64) (Settings, error) {
	v := Settings{Features: map[string]bool{}, Events: map[string]string{}, Menus: map[string]string{}}
	var raw string
	err := q.QueryRow(`SELECT data FROM settings WHERE scope=?`, scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v)
	if v.Features == nil {
		v.Features = map[string]bool{}
	}
	if v.Events == nil {
		v.Events = map[string]string{}
	}
	if v.Menus == nil {
		v.Menus = map[string]string{}
	}
	return v, err
}
func (s *Store) Settings(scope int64) (Settings, error) { return readSettings(s.db, scope) }
func (s *Store) ChangeSettings(scope, actor int64, fn func(*Settings) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := readSettings(tx, scope)
	if err != nil {
		return err
	}
	if err = fn(&v); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO settings VALUES(?,?) ON CONFLICT(scope) DO UPDATE SET data=excluded.data`, scope, string(raw)); err != nil {
		return err
	}
	if err = audit(tx, scope, actor, "settings", "更新群娱配置"); err != nil {
		return err
	}
	return tx.Commit()
}

func readAccount(tx interface{ QueryRow(string, ...any) *sql.Row }, scope string, uid int64) (Account, error) {
	var a Account
	var raw string
	err := tx.QueryRow(`SELECT data FROM accounts WHERE scope=? AND user_id=?`, scope, uid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(raw), &a)
	return a, err
}
func saveAccount(tx *sql.Tx, scope string, uid int64, a Account) error {
	if a.Coins < 0 || a.Bank < 0 || a.XP < 0 || a.Coins > MaxMoney || a.Bank > MaxMoney || a.XP > MaxMoney {
		return errors.New("账户余额或经验超出允许范围")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO accounts VALUES(?,?,?) ON CONFLICT(scope,user_id) DO UPDATE SET data=excluded.data`, scope, uid, string(raw))
	return err
}
func (s *Store) Account(scope string, uid int64) (Account, error) {
	return readAccount(s.db, scope, uid)
}

// Change commits all affected accounts and the audit row in one transaction.
func (s *Store) Change(scope string, ids []int64, actor int64, action string, fn func(map[int64]*Account) (string, error)) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	accounts := map[int64]*Account{}
	for _, uid := range ids {
		if uid <= 0 {
			return "", errors.New("用户编号无效")
		}
		if accounts[uid] != nil {
			continue
		}
		a, err := readAccount(tx, scope, uid)
		if err != nil {
			return "", err
		}
		accounts[uid] = &a
	}
	msg, err := fn(accounts)
	if err != nil {
		return "", err
	}
	for uid, a := range accounts {
		if err = saveAccount(tx, scope, uid, *a); err != nil {
			return "", err
		}
	}
	if action != "speech" {
		if err = audit(tx, 0, actor, action, scope+": "+msg); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return msg, nil
}

func (s *Store) Rank(scope, field string) ([]Ranked, error) {
	allowed := map[string]bool{"coins": true, "bank": true, "xp": true, "signs": true, "streak": true, "today": true, "month": true, "total": true, "fish": true}
	if !allowed[field] {
		return nil, errors.New("未知排行")
	}
	// JSON path comes only from the allowlist above.
	rows, err := s.db.Query(`SELECT user_id, COALESCE(json_extract(data, '$.`+field+`'),0) AS value FROM accounts WHERE scope=? ORDER BY value DESC,user_id LIMIT 10`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ranked
	for rows.Next() {
		var r Ranked
		if err := rows.Scan(&r.UserID, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) SpeechRank(scope, field, period string) ([]Ranked, error) {
	if field != "today" && field != "month" {
		return nil, errors.New("未知发言排行")
	}
	periodField := "speech_day"
	if field == "month" {
		periodField = "speech_month"
	}
	rows, err := s.db.Query(`SELECT user_id, CASE WHEN json_extract(data,'$.`+periodField+`')=? THEN COALESCE(json_extract(data,'$.`+field+`'),0) ELSE 0 END AS value FROM accounts WHERE scope=? ORDER BY value DESC,user_id LIMIT 10`, period, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ranked
	for rows.Next() {
		var r Ranked
		if err := rows.Scan(&r.UserID, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Role(scope, uid int64) (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT level FROM roles WHERE scope=? AND user_id=?`, scope, uid).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

func (s *Store) StreakRank(scope, day, yesterday string) ([]Ranked, error) {
	rows, err := s.db.Query(`SELECT user_id,CASE WHEN json_extract(data,'$.last_sign') IN (?,?) THEN COALESCE(json_extract(data,'$.streak'),0) ELSE 0 END AS value FROM accounts WHERE scope=? ORDER BY value DESC,user_id LIMIT 10`, day, yesterday, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ranked
	for rows.Next() {
		var r Ranked
		if err = rows.Scan(&r.UserID, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) SetRole(scope, uid int64, level int, actor int64) error {
	if uid <= 0 || (scope == 0 && level != 0 && level != 50) || (scope > 0 && level != 0 && level != 30) {
		return errors.New("权限值无效")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO roles VALUES(?,?,?) ON CONFLICT(scope,user_id) DO UPDATE SET level=excluded.level`, scope, uid, level); err != nil {
		return err
	}
	if err = audit(tx, scope, actor, "role", fmt.Sprintf("user=%d level=%d", uid, level)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Roles(scope int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT user_id FROM roles WHERE scope=? AND level>0 ORDER BY user_id LIMIT 200`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) Listed(scope, uid int64, kind string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM lists WHERE scope=? AND user_id=? AND kind=?`, scope, uid, kind).Scan(&n)
	return n > 0, err
}
func (s *Store) SetList(scope, uid int64, kind string, on bool, actor int64) error {
	if uid <= 0 || !strings.Contains("|black|white|gray|private|contact|", "|"+kind+"|") {
		return errors.New("名单类型无效")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if on {
		_, err = tx.Exec(`INSERT OR IGNORE INTO lists VALUES(?,?,?)`, scope, uid, kind)
	} else {
		_, err = tx.Exec(`DELETE FROM lists WHERE scope=? AND user_id=? AND kind=?`, scope, uid, kind)
	}
	if err != nil {
		return err
	}
	if err = audit(tx, scope, actor, "list", fmt.Sprintf("user=%d kind=%s enabled=%v", uid, kind, on)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) List(scope int64, kind string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT user_id FROM lists WHERE scope=? AND kind=? ORDER BY user_id LIMIT 200`, scope, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) SelectGroup(uid, gid int64) error {
	_, err := s.db.Exec(`INSERT INTO selections VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET group_id=excluded.group_id`, uid, gid)
	return err
}
func (s *Store) Selected(uid int64) (int64, error) {
	var gid int64
	err := s.db.QueryRow(`SELECT group_id FROM selections WHERE user_id=?`, uid).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return gid, err
}
func (s *Store) Warn(gid, uid, delta int64) (int64, error) {
	var n int64
	if delta == 0 {
		err := s.db.QueryRow(`SELECT count FROM warnings WHERE group_id=? AND user_id=?`, gid, uid).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return n, err
	}
	err := s.db.QueryRow(`INSERT INTO warnings VALUES(?,?,max(0,?)) ON CONFLICT(group_id,user_id) DO UPDATE SET count=max(0,count+?) RETURNING count`, gid, uid, delta, delta).Scan(&n)
	return n, err
}

// Claim bounds durable message deduplication to 7 days; duplicate delivery never rewards twice.
func (s *Store) Claim(key string, now time.Time) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM seen WHERE at<?`, now.Add(-7*24*time.Hour).Unix()); err != nil {
		return false, err
	}
	r, err := tx.Exec(`INSERT OR IGNORE INTO seen VALUES(?,?)`, key, now.Unix())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}
func audit(tx *sql.Tx, scope, actor int64, action, detail string) error {
	_, err := tx.Exec(`INSERT INTO audit(scope,at,actor,action,detail) VALUES(?,unixepoch(),?,?,?)`, scope, actor, action, detail)
	return err
}
func (s *Store) Audit(scope, actor int64, action, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = audit(tx, scope, actor, action, detail); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Audits(scope int64) ([]Audit, error) {
	rows, err := s.db.Query(`SELECT at,actor,action,detail FROM audit WHERE scope=? ORDER BY id DESC LIMIT 20`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var a Audit
		if err = rows.Scan(&a.At, &a.Actor, &a.Action, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
