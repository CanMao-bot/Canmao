package file

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gobot/core"
)

func TestFileToolsRestrictOrdinaryUsersToTheirScope(t *testing.T) {
	m, bot, _, _ := newTestManager(t)
	ev := &core.Event{Type: "message", DetailType: "private", UserID: 3}
	ctx := core.WithEvent(context.Background(), ev)
	dir := filepath.Join(m.dir, sourceOf(ev))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, "own.txt")
	if err := os.WriteFile(inside, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	read := m.readFileTool()
	if got, err := read.Callback(ctx, map[string]interface{}{"path": "own.txt"}); err != nil || got != "owned" {
		t.Fatalf("本会话文件应可读: %s %v", got, err)
	}
	for _, path := range []string{outside, "../outside.txt"} {
		if _, err := read.Callback(ctx, map[string]interface{}{"path": path}); err == nil {
			t.Fatal("普通用户不能读取范围外文件")
		}
	}
	if _, err := read.Callback(context.Background(), map[string]interface{}{"path": inside}); err == nil {
		t.Fatal("缺少身份时不得读取")
	}
	if _, err := m.writeFileTool().Callback(ctx, map[string]interface{}{"path": outside, "content": "overwrite"}); err == nil {
		t.Fatal("普通用户不得写服务器文件")
	}
	if _, err := m.listDirTool().Callback(ctx, map[string]interface{}{"path": filepath.Dir(outside)}); err == nil {
		t.Fatal("目录列表也应受限")
	}
	owner, _ := strconv.ParseInt(bot.Cfg.Bot.MasterID, 10, 64)
	ownerCtx := core.WithEvent(context.Background(), &core.Event{Type: "message", DetailType: "private", UserID: owner})
	if got, err := read.Callback(ownerCtx, map[string]interface{}{"path": outside}); err != nil || got != "private" {
		t.Fatal("主人应保留服务器文件访问能力")
	}
	if _, err := m.resolveFor(core.WithEvent(context.Background(), &core.Event{DetailType: "private", UserID: 4}), inside); err == nil {
		t.Fatal("文件发送不能跨用户读取")
	}
}

func TestFileSymlinksCannotEscapeScope(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	ev := &core.Event{Type: "message", DetailType: "private", UserID: 3}
	dir := filepath.Join(m.dir, sourceOf(ev))
	os.MkdirAll(dir, 0o755)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("本机不支持创建符号链接: %v", err)
	}
	ctx := core.WithEvent(context.Background(), ev)
	if _, err := m.readFileTool().Callback(ctx, map[string]interface{}{"path": "link"}); err == nil {
		t.Fatal("符号链接不得逃逸")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("new")) }))
	defer server.Close()
	name, err := m.saveFromURL(server.URL, "link", sourceOf(ev))
	if err != nil {
		t.Fatal(err)
	}
	if name == "link" {
		t.Fatal("自动下载不能覆盖已有链接")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "private" {
		t.Fatal("自动保存跟随链接写入了范围外文件")
	}
}

func TestSavedFileScopeSurvivesIndexReload(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("data")) }))
	defer server.Close()
	if _, err := m.saveFromURL(server.URL, "a.txt", "private_3"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.saveFromURL(server.URL, "b.txt", "private_4"); err != nil {
		t.Fatal(err)
	}
	m.loadIndex()
	list := m.listFor(&core.Event{DetailType: "private", UserID: 3})
	if len(list) != 1 || list[0].Name != "a.txt" || !strings.Contains(list[0].Path, "private_3") {
		t.Fatalf("重载后丢失来源: %+v", list)
	}
}

func TestCachedAndGeneratedFilesKeepScopeAndDoNotOverwrite(t *testing.T) {
	m, _, _, fc := newTestManager(t)
	src := filepath.Join(t.TempDir(), "cache.txt")
	if err := os.WriteFile(src, []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc.getFile = map[string]interface{}{"file": src}
	name, err := m.SaveFileFromCache("file-id", "private_3")
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithEvent(context.Background(), &core.Event{DetailType: "private", UserID: 3})
	if got, err := m.readFileTool().Callback(ctx, map[string]interface{}{"path": name}); err != nil || got != "cache" {
		t.Fatalf("缓存文件不可读: %s %v", got, err)
	}
	path, err := m.SaveMedia(ctx, name, []byte("generated"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == name {
		t.Fatal("生成文件覆盖已接收文件")
	}
	m.loadIndex()
	if got := len(m.listFor(core.EventFrom(ctx))); got != 2 {
		t.Fatalf("重新加载后来源丢失: %d", got)
	}
	if got, err := m.readFileTool().Callback(ctx, map[string]interface{}{"path": name}); err != nil || got != "cache" {
		t.Fatal("缓存文件被覆盖")
	}
}
