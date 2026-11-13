package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type dispatchProbe struct{ started, release, control chan struct{} }

func (*dispatchProbe) Name() string { return "dispatch-test" }
func (p *dispatchProbe) Handle(ctx context.Context, _ *Bot, _ *Event) bool {
	p.started <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
	}
	return true
}

type shutdownProbe struct{ started, release chan struct{} }

type policyProbe struct{ checked, controls chan string }

func (p *policyProbe) Name() string                              { return "policy-test" }
func (p *policyProbe) Handle(context.Context, *Bot, *Event) bool { return true }
func (p *policyProbe) AllowEvent(ev *Event) bool {
	p.checked <- ev.RawMessage
	return ev.RawMessage != "blocked"
}
func (p *policyProbe) HandleControl(_ context.Context, _ *Bot, ev *Event) bool {
	p.controls <- ev.RawMessage
	return true
}
func TestEventPolicyPrecedesControlPath(t *testing.T) {
	b := NewBot(&Config{})
	p := &policyProbe{checked: make(chan string, 2), controls: make(chan string, 2)}
	b.RegisterService(p)
	b.Dispatch(&Event{Type: "message", RawMessage: "blocked"})
	b.Dispatch(&Event{Type: "message", RawMessage: "allowed"})
	for i := 0; i < 2; i++ {
		select {
		case <-p.checked:
		case <-time.After(time.Second):
			t.Fatal("policy not checked")
		}
	}
	select {
	case v := <-p.controls:
		if v != "allowed" {
			t.Fatal("blocked control passed policy")
		}
	case <-time.After(time.Second):
		t.Fatal("allowed control not processed")
	}
	b.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.controls) != 0 {
		t.Fatal("extra control passed policy")
	}
}

func (*shutdownProbe) Name() string { return "shutdown-test" }
func (p *shutdownProbe) Handle(context.Context, *Bot, *Event) bool {
	close(p.started)
	<-p.release
	return true
}

func TestShutdownWaitsForWorkersAndRejectsNewDispatch(t *testing.T) {
	b := NewBot(&Config{})
	p := &shutdownProbe{make(chan struct{}), make(chan struct{})}
	b.RegisterService(p)
	b.Dispatch(&Event{Type: "message"})
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("任务未启动")
	}
	b.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("必须等待活动任务: %v", err)
	}
	b.Dispatch(&Event{Type: "message"})
	close(p.release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := b.Wait(ctx2); err != nil {
		t.Fatal(err)
	}
	if len(b.ingress) != 0 {
		t.Fatal("关闭后不得接收新事件")
	}
}
func (p *dispatchProbe) HandleControl(_ context.Context, _ *Bot, ev *Event) bool {
	if ev.RawMessage != "approve" {
		return false
	}
	p.control <- struct{}{}
	return true
}

func TestFullExecutionPoolStillRoutesControlAndDoesNotBlockReceiver(t *testing.T) {
	b := NewBot(&Config{})
	defer b.Shutdown()
	p := &dispatchProbe{started: make(chan struct{}, maxDispatchConcurrency), release: make(chan struct{}), control: make(chan struct{}, 1)}
	b.RegisterService(p)
	defer close(p.release)
	for i := 0; i < maxDispatchConcurrency; i++ {
		b.Dispatch(&Event{Type: "message", MessageID: int64(i + 1)})
	}
	for i := 0; i < maxDispatchConcurrency; i++ {
		select {
		case <-p.started:
		case <-time.After(2 * time.Second):
			t.Fatal("执行名额未占满")
		}
	}
	returned := make(chan struct{})
	go func() {
		b.Dispatch(&Event{Type: "message", RawMessage: "approve"})
		b.Dispatch(&Event{Type: "message"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("接收路径等待执行名额")
	}
	select {
	case <-p.control:
	case <-time.After(time.Second):
		t.Fatal("满载时审批回答不能被饿死")
	}
}
