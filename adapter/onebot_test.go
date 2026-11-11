package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gobot/core"
)

func localOneBot(t *testing.T, handler func(*websocket.Conn)) *OneBot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	o := NewOneBot("ws"+strings.TrimPrefix(server.URL, "http"), "")
	t.Cleanup(func() { o.Close(); server.Close() })
	if err := o.Connect(); err != nil {
		t.Fatal(err)
	}
	return o
}

func respondQQ(conn *websocket.Conn, echo interface{}, status string, code int, data interface{}) error {
	return conn.WriteJSON(map[string]interface{}{"status": status, "retcode": code, "data": data, "echo": echo, "message": "test detail"})
}

func TestQQAPIFinalResponseAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		code         int
		stream, fail bool
	}{
		{"normal", "ok", 0, false, false},
		{"stream", "ok", 0, true, false},
		{"failure", "failed", 1401, false, true},
		{"bad-status", "failed", 0, false, true},
		{"async-ack", "async", 0, false, true},
		{"stream-fails-at-end", "failed", 1200, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := localOneBot(t, func(conn *websocket.Conn) {
				var req map[string]interface{}
				if conn.ReadJSON(&req) != nil {
					return
				}
				if tc.stream {
					_ = respondQQ(conn, req["echo"], "ok", 0, map[string]interface{}{"type": "stream", "index": 0, "data": "chunk"})
				}
				_ = respondQQ(conn, req["echo"], tc.status, tc.code, map[string]interface{}{"type": "response", "value": "final"})
				_, _, _ = conn.ReadMessage()
			})
			frames := 0
			data, err := o.CallQQAPI(context.Background(), "example", map[string]interface{}{}, func(frame json.RawMessage) error { frames++; return nil })
			if (err != nil) != tc.fail {
				t.Fatalf("data=%s err=%v", data, err)
			}
			if tc.fail && !strings.Contains(err.Error(), "test detail") {
				t.Fatal("missing API error detail", err)
			}
			if !tc.fail && !strings.Contains(string(data), "final") {
				t.Fatal("returned an intermediate response", string(data))
			}
			if tc.stream && frames != 1 {
				t.Fatal("stream not consumed", frames)
			}
		})
	}
}

func TestQQCallCancellationAndClose(t *testing.T) {
	for _, closeConnection := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closeConnection], func(t *testing.T) {
			sent := make(chan struct{})
			o := localOneBot(t, func(conn *websocket.Conn) {
				var req interface{}
				if conn.ReadJSON(&req) == nil {
					close(sent)
				}
				_, _, _ = conn.ReadMessage()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := o.CallContext(ctx, "get_status", nil); done <- err }()
			select {
			case <-sent:
			case <-time.After(time.Second):
				t.Fatal("request not sent")
			}
			if closeConnection {
				_ = o.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("uncertain operation reported success")
				}
				if !closeConnection && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not stop")
			}
			o.mu.Lock()
			pending := len(o.echoChans)
			o.mu.Unlock()
			if pending != 0 {
				t.Fatal("pending request leaked")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := NewOneBot("", "")
	if _, err := o.CallContext(ctx, "example", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestQQSlowStreamDoesNotBlockEventDispatch(t *testing.T) {
	startFlood, release := make(chan struct{}), make(chan struct{})
	events := make(chan struct{}, 1)
	o := localOneBot(t, func(conn *websocket.Conn) {
		var req map[string]interface{}
		if conn.ReadJSON(&req) != nil {
			return
		}
		_ = respondQQ(conn, req["echo"], "ok", 0, map[string]interface{}{"type": "stream"})
		<-startFlood
		for i := 0; i < 100; i++ {
			if respondQQ(conn, req["echo"], "ok", 0, map[string]interface{}{"type": "stream"}) != nil {
				return
			}
		}
		_ = conn.WriteJSON(map[string]interface{}{"post_type": "notice"})
		_, _, _ = conn.ReadMessage()
	})
	o.SetEventHandler(func(ev *core.Event) { events <- struct{}{} })
	done := make(chan error, 1)
	go func() {
		_, err := o.CallQQAPI(context.Background(), "download_file_stream", nil, func(json.RawMessage) error {
			select {
			case <-startFlood:
			default:
				close(startFlood)
				<-release
			}
			return nil
		})
		done <- err
	}()
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("stream blocked unrelated event")
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("overflow reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("overflow did not fail pending call")
	}
}

func TestLegacyCallRejectsStreamAndConsumerError(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		o := localOneBot(t, func(conn *websocket.Conn) {
			var req map[string]interface{}
			if conn.ReadJSON(&req) != nil {
				return
			}
			_ = respondQQ(conn, req["echo"], "ok", 0, map[string]interface{}{"type": "stream"})
			_, _, _ = conn.ReadMessage()
		})
		var err error
		if legacy {
			_, err = o.Call("stream", nil)
		} else {
			_, err = o.CallQQAPI(context.Background(), "stream", nil, func(json.RawMessage) error { return errors.New("disk full") })
		}
		if err == nil {
			t.Fatal("incomplete stream accepted")
		}
	}
}

func TestTrackedSendReturnsRealMessageID(t *testing.T) {
	for _, id := range []interface{}{int64(-123), "456", nil, int64(0)} {
		o := localOneBot(t, func(conn *websocket.Conn) {
			var req map[string]interface{}
			if conn.ReadJSON(&req) != nil {
				return
			}
			_ = respondQQ(conn, req["echo"], "ok", 0, map[string]interface{}{"message_id": id})
			_, _, _ = conn.ReadMessage()
		})
		got, err := o.SendMessageWithReceipt(context.Background(), 7, 0, []core.Segment{core.TextSegment("test")})
		valid := id != nil && id != int64(0)
		if valid && (err != nil || got == 0) {
			t.Fatal(got, err)
		}
		if !valid && err == nil {
			t.Fatal("missing receipt accepted")
		}
	}
}
