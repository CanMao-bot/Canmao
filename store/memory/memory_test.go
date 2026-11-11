package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestLegacyMigrationPreservesRowsAndReopens(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE memories(id INTEGER PRIMARY KEY AUTOINCREMENT,scope TEXT NOT NULL,group_id INTEGER NOT NULL DEFAULT 0,user_id INTEGER NOT NULL DEFAULT 0,content TEXT NOT NULL,vector TEXT NOT NULL,created_at INTEGER DEFAULT 0,last_used INTEGER DEFAULT 0,use_count INTEGER DEFAULT 0);
	INSERT INTO memories(scope,user_id,content,vector) VALUES('private',3,'原始记忆','[1,0]')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for i := 0; i < 2; i++ {
		st, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := st.Recent(context.Background(), "private", 0, 3, 10)
		if err != nil || len(rows) != 1 || rows[0].Content != "原始记忆" || len(rows[0].Vector) != 2 {
			t.Fatal(rows, err)
		}
		st.Close()
	}
}

func TestFactUpsertRetainsIDAndScope(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entry := &Entry{Scope: "group", GroupID: 10, UserID: 3, Content: "原名", FactKey: "profile:name", CreatedAt: 20, UseCount: 4}
	id, err := st.Add(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Content = "新名"
	entry.CreatedAt = 30
	id2, err := st.Add(entry)
	if err != nil || id2 != id {
		t.Fatal(id, id2, err)
	}
	found, err := st.FindFact(context.Background(), "group", 10, 3, "profile:name")
	if err != nil || found.CreatedAt != 20 || found.UseCount != 4 || found.Content != "新名" {
		t.Fatal(found, err)
	}
	entry.UserID = 4
	id3, err := st.Add(entry)
	if err != nil || id3 == id {
		t.Fatal("scope collision", id3, err)
	}
}
