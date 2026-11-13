package ai

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

type environmentSender struct {
	*securitySender
	call func(context.Context, string) (json.RawMessage, error)
}

func (s *environmentSender) CallQQAPI(ctx context.Context, action string, _ map[string]interface{}, _ func(json.RawMessage) error) (json.RawMessage, error) {
	return s.call(ctx, action)
}

func TestGroupEnvironmentParallelCacheAndFeatureSwitch(t *testing.T) {
	s := securityFixture(t)
	arrivals := make(chan string, 3)
	release := make(chan struct{})
	var calls atomic.Int32
	s.bot.SetSender(&environmentSender{s.bot.Sender.(*securitySender), func(ctx context.Context, action string) (json.RawMessage, error) {
		calls.Add(1)
		arrivals <- action
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		switch action {
		case "get_group_info":
			return json.RawMessage(`{"group_name":"测试群"}`), nil
		case "get_group_member_info":
			return json.RawMessage(`{"role":"member","card":"test"}`), nil
		default:
			return json.RawMessage(`[{"role":"owner","user_id":5,"nickname":"test owner"}]`), nil
		}
	}})
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		defer close(done)
		name, meta := s.groupEnvironment(ctx, 10, true)
		if name != "测试群" || meta == "" {
			t.Errorf("环境数据未保留: %q %q", name, meta)
		}
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-arrivals:
		case <-time.After(time.Second):
			t.Fatal("群环境接口仍串行等待")
		}
	}
	close(release)
	awaitSignal(t, done, "群环境读取未返回")
	name, meta := s.groupEnvironment(context.Background(), 10, false)
	if name != "测试群" || meta != "" || calls.Load() != 3 {
		t.Fatal("缓存或 GroupMeta 开关失效")
	}
	_, meta = s.groupEnvironment(context.Background(), 10, true)
	if meta == "" || calls.Load() != 3 {
		t.Fatal("重复请求群元信息")
	}
}

func TestGroupEnvironmentCancellationKeepsStaleValues(t *testing.T) {
	s := securityFixture(t)
	s.groupNames = map[int64]cachedGroupName{10: {name: "cached", at: time.Now().Add(-time.Hour)}}
	s.groupMetas = map[int64]cachedGroupMeta{10: {text: "cached role", at: time.Now().Add(-time.Hour)}}
	var canceled atomic.Int32
	s.bot.SetSender(&environmentSender{s.bot.Sender.(*securitySender), func(ctx context.Context, _ string) (json.RawMessage, error) {
		<-ctx.Done()
		canceled.Add(1)
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	name, meta := s.groupEnvironment(ctx, 10, true)
	if name != "cached" || meta != "cached role" || canceled.Load() != 3 || time.Since(started) > time.Second {
		t.Fatal("取消未传播或丢失旧缓存")
	}
}
