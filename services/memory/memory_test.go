package memory

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gobot/core"
	store "gobot/store/memory"
)

func memoryFixture(t *testing.T, e *Embedder) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, e, &core.Config{}), st
}

func TestMemoryPersistsDeduplicatesAndUpdatesWithoutEmbedding(t *testing.T) {
	m, st := memoryFixture(t, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := m.SaveFact(ctx, "private", 0, 3, "我叫小明", "profile:name", 10); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.ListByScope("private", 0, 3)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	id := rows[0].ID
	if err := m.SaveFact(ctx, "private", 0, 3, "我叫小林", "profile:name", 11); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.ListByScope("private", 0, 3)
	if len(rows) != 1 || rows[0].ID != id || rows[0].SourceMessageID != 11 || rows[0].Content != "我叫小林" {
		t.Fatal(rows)
	}
	recall, err := m.Recall(ctx, "private", 0, 3, "我的名字是什么", 5)
	if err != nil || !strings.Contains(recall, "我叫小林") || strings.Contains(recall, "小明") {
		t.Fatal(recall, err)
	}
	for i := 0; i < 2; i++ {
		if err := m.Save(ctx, "private", 0, 3, "喜欢   编程"); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ = st.ListByScope("private", 0, 3)
	if len(rows) != 2 {
		t.Fatal("duplicates persisted", len(rows))
	}
}

func TestRecallNeverImportsPrivateOtherUsersOrGroups(t *testing.T) {
	m, _ := memoryFixture(t, nil)
	ctx := context.Background()
	for _, entry := range []struct {
		scope    string
		gid, uid int64
		content  string
	}{{"private", 0, 3, "我喜欢私聊咖啡"}, {"group", 10, 3, "我喜欢当前群咖啡"}, {"group", 20, 3, "我喜欢其他群咖啡"}, {"group", 10, 4, "我喜欢其他用户咖啡"}} {
		if err := m.Save(ctx, entry.scope, entry.gid, entry.uid, entry.content); err != nil {
			t.Fatal(err)
		}
	}
	result, err := m.Recall(ctx, "group", 10, 3, "喜欢咖啡", 5)
	if err != nil || !strings.Contains(result, "当前群咖啡") || strings.Contains(result, "私聊咖啡") || strings.Contains(result, "其他群咖啡") || strings.Contains(result, "其他用户咖啡") {
		t.Fatal(result, err)
	}
	result, err = m.Recall(ctx, "private", 0, 3, "喜欢咖啡", 5)
	if err != nil || strings.Contains(result, "当前群咖啡") {
		t.Fatal(result, err)
	}
	if result, err := m.Recall(ctx, "group", 10, 3, "火星气象预报", 5); err != nil || result != "" {
		t.Fatal("unrelated memory recalled", result, err)
	}
}

func TestEmbeddingFailureFallsBackAndUsesBackoff(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	m, st := memoryFixture(t, NewEmbedder(EmbeddingConfig{BaseURL: server.URL, APIKey: "test", Model: "embedding"}))
	ctx := context.Background()
	if err := m.Save(ctx, "private", 0, 3, "我喜欢咖啡"); err != nil {
		t.Fatal(err)
	}
	result, err := m.Recall(ctx, "private", 0, 3, "喜欢咖啡", 5)
	if err != nil || !strings.Contains(result, "咖啡") {
		t.Fatal(result, err)
	}
	rows, _ := st.ListByScope("private", 0, 3)
	if len(rows) != 1 || len(rows[0].Vector) != 0 || calls.Load() != 1 {
		t.Fatal("failed embedding lost fact or backoff absent", calls.Load(), rows)
	}
}

func TestVectorsMustMatchModelAndDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"data": []interface{}{map[string]interface{}{"embedding": []float64{1, 0}}}})
	}))
	defer server.Close()
	m, st := memoryFixture(t, NewEmbedder(EmbeddingConfig{BaseURL: server.URL, APIKey: "test", Model: "embedding"}))
	ctx := context.Background()
	if err := m.Save(ctx, "private", 0, 3, "特定长期事实"); err != nil {
		t.Fatal(err)
	}
	result, err := m.Recall(ctx, "private", 0, 3, "不同表达", 5)
	if err != nil || !strings.Contains(result, "特定长期事实") {
		t.Fatal(result, err)
	}
	_, err = st.Add(&store.Entry{Scope: "private", UserID: 3, Content: "旧模型机密", Vector: []float64{1, 0}, VectorModel: "different-model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err = m.Recall(ctx, "private", 0, 3, "不同表达", 5)
	if err != nil || strings.Contains(result, "旧模型机密") {
		t.Fatal("mismatched vectors used", result, err)
	}
	if got := CosineSimilarity([]float64{math.NaN()}, []float64{1}); got != 0 {
		t.Fatal(got)
	}
	if got := CosineSimilarity([]float64{1e200}, []float64{1e200}); got != 0 {
		t.Fatal(got)
	}
}

func TestScopedForgetAndInvalidMemory(t *testing.T) {
	m, st := memoryFixture(t, nil)
	ctx := context.Background()
	if err := m.Save(ctx, "private", 0, 3, "我喜欢咖啡"); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.ListByScope("private", 0, 3)
	id := rows[0].ID
	for _, scope := range []struct {
		s    string
		g, u int64
	}{{"private", 0, 4}, {"group", 10, 3}} {
		if ok, err := m.Forget(ctx, scope.s, scope.g, scope.u, id); err != nil || ok {
			t.Fatal("cross-scope forget", ok, err)
		}
	}
	if ok, err := m.Forget(ctx, "private", 0, 3, id); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for _, text := range []string{"", strings.Repeat("长", 601), "我的密码是 example-password", "api_key=example-value"} {
		if err := m.Save(ctx, "private", 0, 3, text); err == nil {
			t.Fatal("invalid/sensitive memory accepted")
		}
	}
	if err := m.Save(ctx, "group", 10, 0, "不具名内容"); err == nil {
		t.Fatal("anonymous personal fact accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.Save(canceled, "private", 0, 3, "已取消事实"); err == nil {
		t.Fatal("canceled memory saved")
	}
}

func TestConcurrentIdenticalSavesAreUnique(t *testing.T) {
	m, st := memoryFixture(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Save(context.Background(), "private", 0, 3, "喜欢编程"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := st.ListByScope("private", 0, 3)
	if err != nil || len(entries) != 1 {
		t.Fatal(len(entries), err)
	}
}

func TestRecallExcludesLegacyChitchatAndRespectsBudget(t *testing.T) {
	m, st := memoryFixture(t, nil)
	ctx := context.Background()
	if _, err := st.Add(&store.Entry{Scope: "private", UserID: 3, Content: "[对话] 我喜欢咖啡闲聊", Vector: []float64{}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := m.Save(ctx, "private", 0, 3, "咖啡事实"+strings.Repeat("详细", 120)+strings.Repeat("。", i)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := m.Recall(ctx, "private", 0, 3, "咖啡事实详细", 20)
	if err != nil || result == "" || strings.Contains(result, "闲聊") || len([]rune(result)) > 1800 {
		t.Fatal("invalid bounded recall", len([]rune(result)), err)
	}
}
