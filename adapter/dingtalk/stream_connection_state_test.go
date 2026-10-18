package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	dtclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	dtutils "github.com/open-dingtalk/dingtalk-stream-sdk-go/utils"

	"github.com/hexagon-codes/hexclaw/adapter"
)

type streamTicketTransport func(*http.Request) (*http.Response, error)

func (f streamTicketTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func waitStreamHealth(t *testing.T, a *DingtalkAdapter, healthy bool, contains string) {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		err := a.Health(context.Background())
		if healthy && err == nil || !healthy && err != nil && strings.Contains(err.Error(), contains) {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("health did not reach healthy=%v with %q; last error=%v", healthy, contains, err)
		}
	}
}

func streamNoopHandler(context.Context, *adapter.Message) (*adapter.Reply, error) { return nil, nil }

func TestDingtalkStreamHealth_ReconnectWithoutSystemPing(t *testing.T) {
	// SDK取票仅替换固定HTTP边界；连接与重连使用本地真实WebSocket，不访问外网。
	connections := make(chan *websocket.Conn, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections <- conn
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	allowReconnect := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowReconnect) }) }
	reconnecting := make(chan struct{})
	var attempts atomic.Int32
	previous := http.DefaultTransport
	http.DefaultTransport = streamTicketTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != dtutils.GetConnectionEndpointAPIUrl {
			return nil, errors.New("unexpected HTTP request in local Stream test")
		}
		if attempts.Add(1) == 2 {
			close(reconnecting)
			select {
			case <-allowReconnect:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		body, err := json.Marshal(map[string]string{
			"endpoint": "ws" + strings.TrimPrefix(server.URL, "http"), "ticket": "local-test-ticket",
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	a := newTestAdapter()
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop local Stream: %v", err)
		}
	})
	if err := a.Start(context.Background(), streamNoopHandler); err != nil {
		t.Fatal(err)
	}
	var first *websocket.Conn
	select {
	case first = <-connections:
	case <-time.After(4 * time.Second):
		t.Fatal("first local WebSocket was not connected")
	}
	waitStreamHealth(t, a, true, "")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconnecting:
	case <-time.After(4 * time.Second):
		t.Fatal("SDK did not begin reconnecting")
	}
	waitStreamHealth(t, a, false, "disconnected")
	release()
	select {
	case <-connections:
	case <-time.After(4 * time.Second):
		t.Fatal("replacement local WebSocket was not connected")
	}
	waitStreamHealth(t, a, true, "")
	if got := attempts.Load(); got != 2 {
		t.Fatalf("ticket requests=%d, want exactly two", got)
	}
}

func TestDingtalkStreamHealth_ReceiptOwnershipAndOrdering(t *testing.T) {
	a, b := newTestAdapter(), newTestAdapter()
	a.handler, b.handler = streamNoopHandler, streamNoopHandler
	current, foreign := dtclient.NewStreamClient(), dtclient.NewStreamClient()
	a.streamClient, b.streamClient = current, foreign
	b.applyStreamConnectionState(foreign, dtclient.ConnectionState{Generation: 1, Sequence: 1, Connected: true})

	a.applyStreamConnectionState(foreign, dtclient.ConnectionState{Generation: 1, Sequence: 50, Connected: true})
	if a.Health(context.Background()) == nil {
		t.Fatal("foreign client receipt made A healthy")
	}
	a.applyStreamConnectionState(current, dtclient.ConnectionState{Generation: 1, Sequence: 1, Connected: true})
	if err := a.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.applyStreamConnectionState(current, dtclient.ConnectionState{Generation: 1, Sequence: 2, Connected: false})
	a.applyStreamConnectionState(current, dtclient.ConnectionState{Generation: 1, Sequence: 1, Connected: true})
	if err := a.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("late receipt revived disconnected generation: %v", err)
	}
	a.applyStreamConnectionState(current, dtclient.ConnectionState{Generation: 2, Sequence: 3, Connected: true})
	a.applyStreamConnectionState(current, dtclient.ConnectionState{Generation: 1, Sequence: 4, Connected: false})
	if err := a.Health(context.Background()); err != nil {
		t.Fatalf("old generation invalidated replacement connection: %v", err)
	}
	if err := a.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.applyStreamConnectionState(current, dtclient.ConnectionState{Generation: 3, Sequence: 5, Connected: true})
	if err := a.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("late receipt revived stopped adapter: %v", err)
	}
	if err := b.Health(context.Background()); err != nil {
		t.Fatalf("A changed independent adapter B: %v", err)
	}
}

func TestDingtalkStreamHealth_InitialFailureKeepsCause(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = streamTicketTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("local initial handshake unavailable")
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	a := newTestAdapter()
	t.Cleanup(func() {
		if err := a.Stop(context.Background()); err != nil {
			t.Errorf("stop failed handshake: %v", err)
		}
	})
	if err := a.Start(context.Background(), streamNoopHandler); err != nil {
		t.Fatal(err)
	}
	waitStreamHealth(t, a, false, "local initial handshake unavailable")
}
