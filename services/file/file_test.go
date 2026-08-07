package file

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/perm"
)

type fakeFC struct{}

func (f *fakeFC) GetImage(file string) (map[string]interface{}, error) { return nil, nil }
func (f *fakeFC) GetFile(file string) (map[string]interface{}, error)  { return nil, nil }
func (f *fakeFC) DownloadFile(url, threadCount string) (map[string]interface{}, error) {
	return map[string]interface{}{"file": "/tmp/x"}, nil
}
func (f *fakeFC) UploadGroupFile(groupID int64, file, name string) error     { return nil }
func (f *fakeFC) UploadPrivateFile(userID int64, file, name string) error    { return nil }
func (f *fakeFC) GetGroupRootFiles(groupID int64) (map[string]interface{}, error) {
	return map[string]interface{}{"files": []interface{}{}}, nil
}
func (f *fakeFC) GetGroupFilesByFolder(groupID, folderID int64) (map[string]interface{}, error) {
	return nil, nil
}

type fakeSender struct{}

func (f *fakeSender) SendGroupMsg(gid, uid int64, msg []core.Segment) error { return nil }
func (f *fakeSender) SendPrivateMsg(uid int64, msg []core.Segment) error    { return nil }

func newTestManager(t *testing.T) (*Manager, *core.Bot, *perm.Store) {
	t.Helper()
	dir := t.TempDir()
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "3436464181"}})
	bot.SetSender(&fakeSender{})
	pst, err := perm.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := New(dir, bot, &fakeFC{}, pst, 0)
	return m, bot, pst
}

func TestSaveFromURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello file content"))
	}))
	defer srv.Close()

	m, _, _ := newTestManager(t)
	name, err := m.saveFromURL(srv.URL+"/test.txt", "", "private_100")
	if err != nil {
		t.Fatal(err)
	}
	if name != "test.txt" {
		t.Fatalf("name = %q, want test.txt", name)
	}
	if !fileExists(filepath.Join(m.dir, name)) {
		t.Fatal("文件未保存")
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("列表长度 = %d, want 1", len(list))
	}
}

func TestAutoSaveImageSegment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("image-bytes"))
	}))
	defer srv.Close()

	m, bot, pst := newTestManager(t)
	_ = pst
	// 群默认关闭, 需先启用才能触发自动保存
	pst.SetGroupBot(123, true)
	ev := &core.Event{
		Type: "message", DetailType: "group", GroupID: 123, UserID: 456,
		Message: []core.Segment{{Type: "image", Data: map[string]interface{}{"url": srv.URL + "/pic.png"}}},
	}
	m.Handle(context.Background(), bot, ev)
	if len(m.List()) != 1 {
		t.Fatalf("自动保存失败, 列表 = %d", len(m.List()))
	}
}

func TestDisabledGroupSilence(t *testing.T) {
	m, bot, pst := newTestManager(t)
	// 关闭群
	pst.SetGroupBot(123, false)
	ev := &core.Event{
		Type: "message", DetailType: "group", GroupID: 123, UserID: 456,
		Message: []core.Segment{{Type: "image", Data: map[string]interface{}{"url": "http://x/f.png"}}},
	}
	m.Handle(context.Background(), bot, ev)
	if len(m.List()) != 0 {
		t.Fatalf("未启用群不应自动保存, 列表 = %d", len(m.List()))
	}
	// 主人例外
	ev2 := &core.Event{
		Type: "message", DetailType: "group", GroupID: 123, UserID: 3436464181,
		Message: []core.Segment{{Type: "text", Data: map[string]interface{}{"text": "/files"}}},
	}
	m.Handle(context.Background(), bot, ev2)
	// 不应 panic, 且列表仍为 0(无图片)
}

func TestResolvePath(t *testing.T) {
	m, _, _ := newTestManager(t)
	p := filepath.Join(m.dir, "a.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	m.loadIndex()
	got, err := m.ResolvePath("a.txt")
	if err != nil || got != p {
		t.Fatalf("ResolvePath(a.txt) = %q, %v", got, err)
	}
	_, err = m.ResolvePath("nope.txt")
	if err == nil {
		t.Fatal("不存在的文件应报错")
	}
}

func TestCleanupRetention(t *testing.T) {
	dir := t.TempDir()
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "3436464181"}})
	bot.SetSender(&fakeSender{})
	pst, _ := perm.New(dir)
	defer pst.Close()

	// 保留 1 天
	m := New(dir, bot, &fakeFC{}, pst, 1)

	// 伪造一个旧文件
	oldPath := filepath.Join(m.dir, "old.txt")
	os.WriteFile(oldPath, []byte("old"), 0o644)
	oldTime := time.Now().Add(-48 * time.Hour)
	os.Chtimes(oldPath, oldTime, oldTime)

	// 一个新文件
	newPath := filepath.Join(m.dir, "new.txt")
	os.WriteFile(newPath, []byte("new"), 0o644)

	m.loadIndex()
	// 设置旧文件时间
	m.mu.Lock()
	for i := range m.index {
		if m.index[i].Name == "old.txt" {
			m.index[i].CreatedAt = oldTime.Unix()
		}
	}
	m.mu.Unlock()

	m.Cleanup()

	if fileExists(oldPath) {
		t.Fatal("过期文件应被删除")
	}
	if !fileExists(newPath) {
		t.Fatal("新文件不应被删除")
	}
	list := m.List()
	for _, it := range list {
		if it.Name == "old.txt" {
			t.Fatal("索引中不应再有旧文件")
		}
	}
}
