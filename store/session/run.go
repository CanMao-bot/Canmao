package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Run 与 Step 在调用外部系统前落盘。进程中断后的 executing 表示结果未知，不能自动重试。
type Run struct {
	ID, SessionID         int64
	Status, Input, Output string
}

type Step struct {
	ID                         int64
	Name, Args, Status, Result string
}

func (s *Store) initRuns() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS agent_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id INTEGER NOT NULL, request_key TEXT UNIQUE, status TEXT NOT NULL, input TEXT NOT NULL, output TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS agent_steps (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, call_id TEXT NOT NULL, name TEXT NOT NULL, args TEXT NOT NULL, status TEXT NOT NULL, result TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL, UNIQUE(run_id, call_id))`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// 恢复时将未完成记录写入上下文，让后续回合看到可能已经发生的副作用。
	rows, err := s.db.Query(`SELECT id,session_id,status,input,output FROM agent_runs WHERE status='running' ORDER BY id`)
	if err != nil {
		return err
	}
	var runs []*Run
	for rows.Next() {
		r := &Run{}
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Status, &r.Input, &r.Output); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range runs {
		steps, err := s.Steps(r.ID)
		if err != nil {
			return err
		}
		var trace strings.Builder
		for _, step := range steps {
			result := []rune(step.Result)
			if len(result) > 500 {
				result = result[:500]
			}
			fmt.Fprintf(&trace, "%s [%s]: %s\n", step.Name, step.Status, string(result))
		}
		output := "[上次任务已中断] 进程退出时本轮尚未完成；executing 表示执行结果未知，请核对后再继续，不要自动重试。"
		if err = s.FinishRun(r, "interrupted", output, trace.String()); err != nil {
			if !errors.Is(err, ErrNotOwned) {
				return err
			}
			if _, err = s.db.Exec(`UPDATE agent_runs SET status='interrupted' WHERE id=? AND status='running'`, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// BeginRun 对同一 OneBot 消息去重，并原子保存用户输入。
func (s *Store) BeginRun(sessionID int64, requestKey, input string) (*Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if requestKey != "" {
		var r Run
		err := s.db.QueryRow(`SELECT id, session_id, status, input, output FROM agent_runs WHERE request_key=?`, requestKey).Scan(&r.ID, &r.SessionID, &r.Status, &r.Input, &r.Output)
		if err == nil {
			return &r, true, nil
		}
		if err != sql.ErrNoRows {
			return nil, false, err
		}
	}
	se, err := s.getByIDLocked(sessionID)
	if err != nil {
		return nil, false, err
	}
	if se == nil {
		return nil, false, ErrNotOwned
	}
	now := time.Now().Unix()
	se.Messages = append(se.Messages, Message{Role: "user", Content: input, Time: now})
	raw, err := json.Marshal(se.Messages)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var key interface{}
	if requestKey != "" {
		key = requestKey
	}
	res, err := tx.Exec(`INSERT INTO agent_runs(session_id, request_key, status, input, created_at, updated_at) VALUES(?,?,'running',?,?,?)`, sessionID, key, input, now, now)
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`UPDATE sessions SET messages=?, updated_at=?, revision=revision+1 WHERE id=?`, string(raw), now, sessionID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &Run{ID: id, SessionID: sessionID, Status: "running", Input: input}, false, nil
}

func (s *Store) BeginStep(runID int64, callID, name, args string) (*Step, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var step Step
	err := s.db.QueryRow(`SELECT id, name, args, status, result FROM agent_steps WHERE run_id=? AND call_id=?`, runID, callID).Scan(&step.ID, &step.Name, &step.Args, &step.Status, &step.Result)
	if err == nil {
		if step.Name != name || step.Args != args {
			return nil, false, fmt.Errorf("工具调用 ID 被不同参数重复使用")
		}
		return &step, true, nil
	}
	if err != sql.ErrNoRows {
		return nil, false, err
	}
	res, err := s.db.Exec(`INSERT INTO agent_steps(run_id,call_id,name,args,status,updated_at) VALUES(?,?,?,?,'pending',?)`, runID, callID, name, args, time.Now().Unix())
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	return &Step{ID: id, Name: name, Args: args, Status: "pending"}, false, err
}

func (s *Store) UpdateStep(id int64, status, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE agent_steps SET status=?,result=?,updated_at=? WHERE id=?`, status, result, time.Now().Unix(), id)
	return err
}

func (s *Store) Steps(runID int64) ([]Step, error) {
	rows, err := s.db.Query(`SELECT id,name,args,status,result FROM agent_steps WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []Step
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.ID, &st.Name, &st.Args, &st.Status, &st.Result); err != nil {
			return nil, err
		}
		steps = append(steps, st)
	}
	return steps, rows.Err()
}

// FinishRun 将最终状态、执行摘要和回复原子提交；失败也保留完整一轮。
func (s *Store) FinishRun(run *Run, status, output, trace string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, err := s.getByIDLocked(run.SessionID)
	if err != nil {
		return err
	}
	if se == nil {
		return ErrNotOwned
	}
	now := time.Now().Unix()
	if trace != "" {
		se.Messages = append(se.Messages, Message{Role: "assistant", Content: trace, Time: now})
	}
	se.Messages = append(se.Messages, Message{Role: "assistant", Content: output, Time: now})
	raw, err := json.Marshal(se.Messages)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE agent_runs SET status=?,output=?,updated_at=? WHERE id=? AND status='running'`, status, output, now, run.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("任务已结束，不能重复提交")
	}
	if _, err = tx.Exec(`UPDATE sessions SET messages=?,updated_at=?,revision=revision+1 WHERE id=?`, string(raw), now, se.ID); err != nil {
		return err
	}
	return tx.Commit()
}
