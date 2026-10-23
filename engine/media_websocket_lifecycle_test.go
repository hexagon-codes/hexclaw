package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	webadapter "github.com/hexagon-codes/hexclaw/adapter/web"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/streamstate"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func TestMediaWebSocketExplicitCancelKeepsLateAssetWithoutCompletingTurn(t *testing.T) {
	t.Run("connected", func(t *testing.T) { verifyMediaWebSocketExplicitCancel(t, false) })
	t.Run("after_disconnect", func(t *testing.T) { verifyMediaWebSocketExplicitCancel(t, true) })
}

func verifyMediaWebSocketExplicitCancel(t *testing.T, reconnect bool) {
	pngB64 := mediaPNG(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": pngB64}}})
	}))
	defer upstream.Close()
	defer unblock()
	eng, store := mediaEngineFixture(t, upstream.URL, mockllm.NewLLMProvider("test"))
	wa := webadapter.New()
	if err := wa.Start(t.Context(), eng.Process); err != nil {
		t.Fatal(err)
	}
	wa.SetStreamHandler(eng.ProcessStream)
	t.Cleanup(func() { _ = wa.Stop(context.Background()) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wa.Handler().ServeHTTP(w, r.WithContext(skill.WithAuthenticatedUser(r.Context(), "user")))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"tauri://localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	const requestID, sessionID = "media-ws-stop", "media-ws-session"
	if err := wsjson.Write(ctx, conn, map[string]any{
		"type": "message", "content": "Draw a square", "request_id": requestID, "session_id": sessionID,
		"provider": "image-fixture", "model": "fixture-image", "metadata": map[string]string{"image_generation": "true"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("media request did not reach the provider")
	}
	ops, err := eng.mediaFlow.RequestOperations(ctx, "user", mediaflow.Origin{Kind: "conversation", SessionID: sessionID, RequestID: requestID})
	if err != nil || len(ops) != 1 {
		t.Fatalf("accepted operation missing: %v %+v", err, ops)
	}
	op := ops[0]
	if reconnect {
		if err := conn.Close(websocket.StatusNormalClosure, "transport reconnect"); err != nil {
			t.Fatal(err)
		}
		grace := time.NewTimer(5100 * time.Millisecond)
		defer grace.Stop()
		select {
		case <-ctx.Done():
			t.Fatal("disconnect grace did not finish")
		case <-grace.C:
		}
		if snapshot, ok := wa.GetStreamSnapshot("user", requestID); !ok || snapshot.Done {
			t.Fatal("ordinary disconnect ended accepted media work")
		}
		conn, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"tauri://localhost"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := wsjson.Write(ctx, conn, map[string]string{"type": "cancel", "request_id": requestID, "session_id": sessionID}); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	stopDeadline := time.NewTimer(2 * time.Second)
	defer stopDeadline.Stop()
	for !op.CancelRequested {
		select {
		case <-stopDeadline.C:
			t.Fatal("explicit WebSocket cancellation was treated as a disconnect")
		case <-ticker.C:
		}
		op, err = eng.mediaFlow.Read(ctx, "user", op.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	unblock()
	for op.SaveState != "ready" {
		select {
		case <-ctx.Done():
			t.Fatal("late media bytes were not preserved")
		case <-ticker.C:
		}
		op, err = eng.mediaFlow.Read(ctx, "user", op.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !op.CancelRequested || len(op.Assets) != 1 || calls.Load() != 1 {
		t.Fatalf("late asset or original cancellation lost: %+v calls=%d", op, calls.Load())
	}
	for {
		eng.mu.RLock()
		active := eng.mediaRequests["user\x00"+requestID] > 0
		eng.mu.RUnlock()
		if !active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("cancelled turn continued running")
		case <-ticker.C:
		}
	}
	var assistantCount, deliveryCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE role='assistant' AND request_id=?`, requestID).Scan(&assistantCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM media_deliveries WHERE completion_key=?`, requestID).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if assistantCount != 0 || deliveryCount != 0 {
		t.Fatalf("cancelled turn created a completion: assistant=%d delivery=%d", assistantCount, deliveryCount)
	}
	snapshot, ok := wa.GetStreamSnapshot("user", requestID)
	if !ok || snapshot.Status != streamstate.StatusCancelled || snapshot.Content != "" {
		t.Fatalf("cancelled stream was replaced with a completed reply: %+v", snapshot)
	}
}
