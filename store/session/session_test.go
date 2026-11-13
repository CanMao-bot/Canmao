package session

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestScopeIsolationAndInvalidLegacyPointer(t *testing.T) {
	s := newStore(t)
	victim, err := s.Create("private", 0, 3, "private")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Switch("private", 0, 4, victim.ID); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("跨用户切换: %v", err)
	}
	if _, err = s.GetScoped(victim.ID, "group", 7, 0); !errors.Is(err, ErrNotOwned) {
		t.Fatal(err)
	}
	if err = s.DeleteScoped(victim.ID, "private", 0, 4); !errors.Is(err, ErrNotOwned) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO session_state(scope,group_id,user_id,current_id) VALUES('private',0,4,?)`, victim.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetCurrent("private", 0, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID == victim.ID || !current.BelongsTo("private", 0, 4) {
		t.Fatal("旧非法指针必须隔离")
	}
	group, err := s.Create("group", 7, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Switch("group", 7, 0, group.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Switch("group", 8, 0, group.ID); !errors.Is(err, ErrNotOwned) {
		t.Fatal("跨群切换必须失败")
	}
}

func TestCommitContextIsAtomicAndRejectsStaleSnapshot(t *testing.T) {
	s := newStore(t)
	ses, _ := s.Create("private", 0, 3, "")
	if err := s.Append(ses.ID, []Message{{Role: "user", Content: "old"}}, 0); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.Get(ses.ID)
	if err := s.Append(ses.ID, []Message{{Role: "assistant", Content: "new"}}, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitContext(snapshot, "stale-summary", nil); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	current, _ := s.Get(ses.ID)
	if current.Summary != "" || len(current.Messages) != 2 {
		t.Fatal("冲突不能部分写入摘要或窗口")
	}
	if err := s.CommitContext(current, "summary", []Message{{Role: "assistant", Content: "keep"}}); err != nil {
		t.Fatal(err)
	}
	if current.Summary != "summary" || len(current.Messages) != 1 {
		t.Fatal("应更新内存快照")
	}
	if err := s.SetSummary(ses.ID, "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummary(ses.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(ses.ID); err != nil {
		t.Fatal(err)
	}
	cleared, _ := s.Get(ses.ID)
	if len(cleared.Messages) != 0 || cleared.Summary != "" {
		t.Fatal("清空失败")
	}
}

func TestRunAndStepSurviveReopenWithoutReexecution(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.Create("private", 0, 3, "")
	run, duplicate, err := s.BeginRun(ses.ID, "request", "input")
	if err != nil || duplicate {
		t.Fatal(err)
	}
	step, duplicate, err := s.BeginStep(run.ID, "call", "effect", "{}")
	if err != nil || duplicate {
		t.Fatal(err)
	}
	if err = s.UpdateStep(step.ID, "executing", ""); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	again, duplicate, err := s.BeginRun(ses.ID, "request", "input")
	if err != nil || !duplicate || again.Status != "interrupted" {
		t.Fatalf("中断任务不能重放: %+v %v", again, err)
	}
	steps, err := s.Steps(run.ID)
	if err != nil || len(steps) != 1 || steps[0].Status != "executing" {
		t.Fatalf("应保留不确定的执行记录: %+v %v", steps, err)
	}
	after, err := s.Get(ses.ID)
	if err != nil || len(after.Messages) != 3 || !strings.Contains(after.Messages[1].Content, "effect [executing]") || !strings.Contains(after.Messages[2].Content, "不要自动重试") {
		t.Fatalf("重启后续回合必须看到中断步骤: %+v %v", after, err)
	}
	if err := s.FinishRun(run, "completed", "again", ""); err == nil {
		t.Fatal("已经恢复的任务不能再次提交")
	}
}

func TestRevisionMigratesExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions(id INTEGER PRIMARY KEY,scope TEXT,group_id INTEGER,user_id INTEGER,title TEXT,summary TEXT,messages TEXT,created_at INTEGER,updated_at INTEGER); INSERT INTO sessions VALUES(1,'private',0,3,'','old','[]',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ses, err := s.Get(1)
	if err != nil || ses.Summary != "old" {
		t.Fatal(err)
	}
	if err = s.CommitContext(ses, "new", nil); err != nil {
		t.Fatal(err)
	}
}
