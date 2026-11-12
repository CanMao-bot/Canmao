package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"gobot/core"
)

type modelTransportFunc func(*http.Request) (*http.Response, error)

func (fn modelTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestModelRetriesConnectionInterruptionWithSameRequest(t *testing.T) {
	client := NewClient(&core.AIConfig{BaseURL: "http://model.invalid", Model: "test", Timeout: 3})
	var bodies []string
	client.http.Transport = modelTransportFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			return nil, io.EOF
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"once","type":"function","function":{"name":"example","arguments":"{}"}}]}}]}`))}, nil
	})
	msg, err := client.Complete(context.Background(), []Message{NewTextMessage("user", "hello")}, nil)
	if err != nil || len(msg.ToolCalls) != 1 {
		t.Fatalf("%+v %v", msg, err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatal("重试修改了请求或次数错误")
	}
}

type interruptedModelBody struct{}

func (interruptedModelBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (interruptedModelBody) Close() error             { return nil }

func TestModelRetriesTruncatedResponseAndStopsAfterOneRetry(t *testing.T) {
	client := NewClient(&core.AIConfig{BaseURL: "http://model.invalid", Model: "test", Timeout: 3})
	calls := 0
	client.http.Transport = modelTransportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: interruptedModelBody{}}, nil
	})
	_, err := client.Complete(context.Background(), []Message{NewTextMessage("user", "hello")}, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestModelDoesNotRetryHTTPRejectionOrInvalidJSON(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500, 200} {
		client := NewClient(&core.AIConfig{BaseURL: "http://model.invalid", Model: "test", Timeout: 3})
		calls := 0
		client.http.Transport = modelTransportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("invalid"))}, nil
		})
		_, err := client.Complete(context.Background(), []Message{NewTextMessage("user", "hello")}, nil)
		if err == nil || calls != 1 {
			t.Fatalf("status=%d calls=%d err=%v", status, calls, err)
		}
	}
}

func TestModelRetryHonorsCancellation(t *testing.T) {
	client := NewClient(&core.AIConfig{BaseURL: "http://model.invalid", Model: "test", Timeout: 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client.http.Transport = modelTransportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, io.EOF
	})
	_, err := client.Complete(ctx, []Message{NewTextMessage("user", "hello")}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
