package file

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gobot/core"
	"gobot/store/perm"
)

type fakeFC struct {
	getFile      map[string]interface{} // GetFile 返回数据
	getImage     map[string]interface{} // GetImage 返回数据
	privateFiles []string               // 记录 UploadPrivateFile 的文件名
}

func (f *fakeFC) GetImage(file string) (map[string]interface{}, error) { return f.getImage, nil }
func (f *fakeFC) GetFile(file string) (map[string]interface{}, error)  { return f.getFile, nil }
func (f *fakeFC) DownloadFile(url, threadCount string) (map[string]interface{}, error) {
	return map[string]interface{}{"file": "/tmp/x"}, nil
}
func (f *fakeFC) UploadGroupFile(groupID int64, file, name string) error { return nil }
func (f *fakeFC) UploadPrivateFile(userID int64, file, name string) error {
	f.privateFiles = append(f.privateFiles, name)
	return nil
}
func (f *fakeFC) GetGroupRootFiles(groupID int64) (map[string]interface{}, error) {
	return map[string]interface{}{"files": []interface{}{}}, nil
}
func (f *fakeFC) GetGroupFilesByFolder(groupID, folderID int64) (map[string]interface{}, error) {
	return nil, nil
}

type fakeSender struct{}

func (f *fakeSender) SendGroupMsg(gid, uid int64, msg []core.Segment) error { return nil }
func (f *fakeSender) SendPrivateMsg(uid int64, msg []core.Segment) error    { return nil }

func newTestManager(t *testing.T) (*Manager, *core.Bot, *perm.Store, *fakeFC) {
	t.Helper()
	dir := t.TempDir()
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "1000001"}})
	bot.SetSender(&fakeSender{})
	pst, err := perm.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeFC{}
	m := New(dir, bot, fc, pst, 0)
	return m, bot, pst, fc
}

func TestSaveFromURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello file content"))
	}))
	defer srv.Close()

	m, _, _, _ := newTestManager(t)
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

	m, bot, pst, _ := newTestManager(t)
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
	m, bot, pst, _ := newTestManager(t)
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
		Type: "message", DetailType: "group", GroupID: 123, UserID: 1000001,
		Message: []core.Segment{{Type: "text", Data: map[string]interface{}{"text": "/files"}}},
	}
	m.Handle(context.Background(), bot, ev2)
	// 不应 panic, 且列表仍为 0(无图片)
}

func TestResolvePath(t *testing.T) {
	m, _, _, _ := newTestManager(t)
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
	bot := core.NewBot(&core.Config{Bot: core.BotConfig{MasterID: "1000001"}})
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

// 私聊文件段无 url 时, 应走 get_file fallback 保存, 并追加提示到 RawMessage
func TestPrivateFileSegmentFallback(t *testing.T) {
	m, bot, _, fc := newTestManager(t)
	// get_file 返回本地路径
	src := filepath.Join(t.TempDir(), "report.pdf")
	os.WriteFile(src, []byte("pdf-bytes"), 0o644)
	fc.getFile = map[string]interface{}{"file": src}

	ev := &core.Event{
		Type: "message", DetailType: "private", UserID: 100,
		Message: []core.Segment{{Type: "file", Data: map[string]interface{}{
			"file_id": "abc123", "name": "report.pdf", "size": float64(9),
		}}},
	}
	m.Handle(context.Background(), bot, ev)

	list := m.List()
	if len(list) != 1 || list[0].Name != "report.pdf" {
		t.Fatalf("get_file fallback 保存失败, 列表 = %v", list)
	}
	if !strings.Contains(ev.RawMessage, "[收到文件: report.pdf, 已保存为 report.pdf]") {
		t.Fatalf("RawMessage 未追加提示: %q", ev.RawMessage)
	}
}

// 群文件 notice group_upload 应自动保存并回复提示
func TestGroupUploadNotice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("group-file-bytes"))
	}))
	defer srv.Close()

	m, bot, pst, _ := newTestManager(t)
	pst.SetGroupBot(123, true)
	ev := &core.Event{
		Type: "notice", NoticeType: "group_upload", GroupID: 123, UserID: 456,
		File: map[string]interface{}{
			"id": "fid1", "name": "data.zip", "size": float64(2048), "url": srv.URL + "/data.zip",
		},
	}
	if !m.Handle(context.Background(), bot, ev) {
		t.Fatal("group_upload 应被消费")
	}
	list := m.List()
	if len(list) != 1 || list[0].Name != "data.zip" {
		t.Fatalf("群文件未保存, 列表 = %v", list)
	}
	if list[0].Source != "group_123" {
		t.Fatalf("来源 = %q, want group_123", list[0].Source)
	}
}

// send_file 工具私聊分支: 应调用 UploadPrivateFile
func TestSendFileToolPrivate(t *testing.T) {
	m, _, _, fc := newTestManager(t)
	p := filepath.Join(m.dir, "x.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	m.loadIndex()

	var cb func(ctx context.Context, args map[string]interface{}) (string, error)
	for _, tl := range m.Tools() {
		if tl.Function.Name == "send_file" {
			cb = tl.Callback
		}
	}
	if cb == nil {
		t.Fatal("未找到 send_file 工具")
	}

	ev := &core.Event{Type: "message", DetailType: "private", UserID: 100}
	ctx := context.WithValue(context.Background(), "event", ev)
	out, err := cb(ctx, map[string]interface{}{"name": "x.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.privateFiles) != 1 || fc.privateFiles[0] != "x.txt" {
		t.Fatalf("私聊发送记录 = %v, want [x.txt]", fc.privateFiles)
	}
	if !strings.Contains(out, "x.txt") {
		t.Fatalf("返回 = %q", out)
	}
}
