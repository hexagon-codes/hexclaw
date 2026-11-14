package web

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/streamstate"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

type frameWriteBarrierListener struct {
	net.Listener
	wrap func(net.Conn) net.Conn
}

func (l frameWriteBarrierListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.wrap(conn), nil
}

type frameWriteBarrierConn struct {
	net.Conn
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (c *frameWriteBarrierConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err == nil && bytes.Contains(data, []byte("首帧")) {
		c.once.Do(func() {
			close(c.started)
			<-c.release
		})
	}
	return n, err
}

func TestWebAdapter_ResumeRetainsEmptyCanonicalArtifact(t *testing.T) {
	ref := messagecontent.AttachmentRef{AssetID: "stdout-ref", Name: "stdout.bin", MIME: "application/octet-stream", Digest: "sha256:" + strings.Repeat("b", 64), AltText: "stdout.bin"}
	content, err := messagecontent.New(messagecontent.ProducerSkill, "en", "", []messagecontent.AttachmentRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := messagecontent.BuildManifest(content, messagecontent.RenderRequest{Surface: messagecontent.SurfaceDesktop, RendererVersion: "test-v1", Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, Attachments: true}, Parts: []messagecontent.RenderPart{{Kind: messagecontent.PartArtifact, ArtifactRef: ref.AssetID, ArtifactDigest: ref.Digest, AltText: ref.AltText}}})
	if err != nil {
		t.Fatal(err)
	}
	a := New()
	a.streams.Start("desktop-user", "original-session", "restore-artifact")
	a.streams.Append("restore-artifact", &adapter.ReplyChunk{Content: "older preview"})
	a.streams.Append("restore-artifact", &adapter.ReplyChunk{Done: true, MessageContent: &content, RenderManifest: &manifest})
	a.streams.Cancel("restore-artifact")
	conn, ctx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(ctx, conn, wsMessage{Type: "resume", RequestID: "restore-artifact"}); err != nil {
		t.Fatal(err)
	}
	var frame wsMessage
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "stream_snapshot" || frame.Status != "cancelled" || frame.Content != "" || frame.MessageContent == nil || frame.MessageContent.Markdown != "" || len(frame.MessageContent.Attachments) != 1 || frame.MessageContent.Attachments[0].AssetID != "stdout-ref" || frame.RenderManifest == nil || frame.RenderManifest.RenderID != manifest.RenderID {
		t.Fatalf("resume lost authoritative artifact: %#v", frame)
	}
}

// 用户停止后连接仍存活；错误帧必须交付真实部分结果而不是只剩错误文字。
func TestWebAdapter_FailedTerminalPreservesObservedResult(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		canceled, queued bool
	}{
		{name: "nonzero"},
		{name: "canceled", canceled: true},
		{name: "canceled_queued", canceled: true, queued: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			name, canceled := scenario.name, scenario.canceled
			a := New()
			started := make(chan struct{})
			content := "partial-output; exit code: 7; artifact /api/v1/files/generated/stdout.txt"
			a.SetStreamHandler(func(ctx context.Context, _ *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
				out := make(chan *adapter.ReplyChunk, 1)
				close(started)
				go func() {
					cause := errors.New("exit status 7")
					status := adapter.RuntimeTerminalFailed
					if canceled {
						<-ctx.Done()
						cause, status = context.Canceled, adapter.RuntimeTerminalCancelled
						if scenario.queued {
							// 取消后的运行事件不能让消费端提前丢弃后续真实终帧。
							out <- &adapter.ReplyChunk{Sequence: 1, RuntimeEvent: &adapter.RuntimeEvent{Kind: adapter.RuntimeEventToolFailed, ToolCallID: "original-call", ToolName: "shell"}}
						}
					}
					out <- &adapter.ReplyChunk{Content: content, Done: true, Error: cause, MessageContent: canonicalReplyContent(content, nil), Metadata: map[string]string{"producer_kind": "skill", "session_id": "failure-session"}, ToolCalls: []adapter.ToolCall{{ID: "original-call", Name: "shell", Status: "error", Result: content}}, RuntimeEvent: &adapter.RuntimeEvent{Kind: adapter.RuntimeEventTerminal, TerminalStatus: status}}
					close(out)
				}()
				return out, nil
			})
			conn, ctx, _ := dialWebAdapter(t, a)
			incomingSession := "failure-session"
			if !canceled {
				incomingSession = ""
			}
			if err := wsjson.Write(ctx, conn, wsMessage{Type: "message", Content: "/sh task", SessionID: incomingSession, RequestID: "failure-" + name}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if canceled {
				if err := wsjson.Write(ctx, conn, wsMessage{Type: "cancel", RequestID: "failure-" + name}); err != nil {
					t.Fatal(err)
				}
			}
			var frame wsMessage
			if err := wsjson.Read(ctx, conn, &frame); err != nil {
				t.Fatalf("terminal not delivered: %v", err)
			}
			if frame.Type != "error" || !frame.Done || frame.SessionID != "failure-session" || frame.MessageContent == nil || frame.MessageContent.Markdown != content || len(frame.ToolCalls) != 1 || frame.ToolCalls[0].Status != "error" {
				t.Fatalf("failed result lost: %#v", frame)
			}
			if canceled {
				snapshot, ok := a.GetStreamSnapshot("desktop-user", "failure-"+name)
				if !ok || !snapshot.Done || snapshot.Content != content || len(snapshot.ToolCalls) != 1 || snapshot.ToolCalls[0].Status != "error" {
					t.Fatalf("canceled snapshot lost observed terminal: %#v", snapshot)
				}
				if scenario.queued && (snapshot.LastSequence != 1 || len(snapshot.RuntimeEvents) != 1 || snapshot.RuntimeEvents[0].Event.Kind != adapter.RuntimeEventToolFailed) {
					t.Fatalf("queued observed event was discarded: %#v", snapshot)
				}
			}
			if err := wsjson.Write(ctx, conn, wsMessage{Type: "ping"}); err != nil {
				t.Fatal(err)
			}
			var pong wsMessage
			if err := wsjson.Read(ctx, conn, &pong); err != nil || pong.Type != "pong" {
				t.Fatalf("connection closed after terminal: %#v, %v", pong, err)
			}
		})
	}
}

// 真实事故复现(2026-06-25)：流式中途 chunk.Error 经 WS 'error' 帧下发时
// 直接用 chunk.Error.Error()（原始串），未走 upstreamerr.PublicMessage，
// 导致 Nvidia 免费 VL 模型的 400 整坨 JSON 灌进桌面聊天气泡。
// 兄弟路径 web.go:390（启动失败）与 api/server.go(SSE/HTTP) 都已净化，唯独中途 chunk 漏。
func TestWebAdapter_StreamChunkErrorIsSanitized(t *testing.T) {
	a := New()
	chunks := make(chan *adapter.ReplyChunk, 1)
	a.SetStreamHandler(func(_ context.Context, _ *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		return chunks, nil
	})

	conn, ctx, _ := dialWebAdapter(t, a)

	if err := wsjson.Write(ctx, conn, wsMessage{
		Type: "message", Content: "hi", SessionID: "s1", RequestID: "r1", UserID: "desktop-user",
	}); err != nil {
		t.Fatalf("send ws message failed: %v", err)
	}

	chunks <- &adapter.ReplyChunk{
		Error: errors.New(`runtime stream 失败: llm complete: openai api error: 400 Bad Request, ` +
			`body: {"error":{"message":"Provider returned error","code":400,` +
			`"metadata":{"provider_name":"Nvidia"}},"user_id":"user_x"}`),
		Done: true,
	}
	close(chunks)

	var frame wsMessage
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatalf("read error frame failed: %v", err)
	}
	if frame.Type != "error" {
		t.Fatalf("frame type = %q, want error", frame.Type)
	}
	if strings.Contains(frame.Content, "body:") || strings.Contains(frame.Content, "runtime stream") {
		t.Fatalf("WS 中途 chunk 错误未净化，泄露原始内容: %q", frame.Content)
	}
	if frame.Content != "Provider returned error (code: 400)" {
		t.Fatalf("净化结果 = %q, 期望 %q", frame.Content, "Provider returned error (code: 400)")
	}
}

func dialWebAdapter(t *testing.T, a *WebAdapter, wrappers ...func(net.Conn) net.Conn) (*websocket.Conn, context.Context, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Handler().ServeHTTP(w, r.WithContext(skill.WithAuthenticatedUser(r.Context(), "desktop-user")))
	}))
	if len(wrappers) != 0 {
		srv.Listener = frameWriteBarrierListener{Listener: srv.Listener, wrap: wrappers[0]}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("dial websocket failed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		cancel()
	})
	// 握手完成不代表服务端已登记连接；pong 确认连接与归属登记后进入读循环。
	if err := wsjson.Write(ctx, conn, wsMessage{Type: "ping"}); err != nil {
		t.Fatalf("write websocket readiness ping: %v", err)
	}
	var ready wsMessage
	if err := wsjson.Read(ctx, conn, &ready); err != nil {
		t.Fatalf("read websocket readiness pong: %v", err)
	}
	if ready.Type != "pong" {
		t.Fatalf("websocket readiness frame=%q, want pong", ready.Type)
	}
	return conn, ctx, cancel
}

func TestWebAdapter_StreamMessagesIncludeSessionAndRequestID(t *testing.T) {
	a := New()
	chunks := make(chan *adapter.ReplyChunk, 2)
	a.SetStreamHandler(func(ctx context.Context, msg *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		if got := msg.SessionID; got != "sess-stream-1" {
			t.Fatalf("session id not forwarded to stream handler: %q", got)
		}
		if got := msg.Metadata["request_id"]; got != "req-stream-1" {
			t.Fatalf("request_id not forwarded to stream handler: %q", got)
		}
		return chunks, nil
	})

	conn, ctx, _ := dialWebAdapter(t, a)

	if err := wsjson.Write(ctx, conn, wsMessage{
		Type:      "message",
		Content:   "你好",
		SessionID: "sess-stream-1",
		RequestID: "req-stream-1",
		UserID:    "desktop-user",
	}); err != nil {
		t.Fatalf("send ws message failed: %v", err)
	}

	chunks <- &adapter.ReplyChunk{Content: "你"}
	chunks <- &adapter.ReplyChunk{Content: "好", Done: true}
	close(chunks)

	var first wsMessage
	if err := wsjson.Read(ctx, conn, &first); err != nil {
		t.Fatalf("read first chunk failed: %v", err)
	}
	if first.Type != "chunk" {
		t.Fatalf("first message type = %q, want chunk", first.Type)
	}
	if first.SessionID != "sess-stream-1" {
		t.Fatalf("first chunk session_id = %q", first.SessionID)
	}
	if first.RequestID != "req-stream-1" {
		t.Fatalf("first chunk request_id = %q", first.RequestID)
	}

	var second wsMessage
	if err := wsjson.Read(ctx, conn, &second); err != nil {
		t.Fatalf("read final chunk failed: %v", err)
	}
	if !second.Done {
		t.Fatal("final chunk should be done")
	}
	if second.SessionID != "sess-stream-1" || second.RequestID != "req-stream-1" {
		t.Fatalf("final chunk ids mismatch: session=%q request=%q", second.SessionID, second.RequestID)
	}
}

func TestWebAdapter_CancelSuppressesLateStreamTerminal(t *testing.T) {
	a := New()
	chunks := make(chan *adapter.ReplyChunk, 2)
	cancelObserved := make(chan struct{})
	a.SetStreamHandler(func(ctx context.Context, _ *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		go func() {
			<-ctx.Done()
			close(cancelObserved)
		}()
		return chunks, nil
	})

	writeStarted, releaseWrite := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWrite) }) }
	conn, ctx, _ := dialWebAdapter(t, a, func(conn net.Conn) net.Conn {
		return &frameWriteBarrierConn{Conn: conn, started: writeStarted, release: releaseWrite}
	})
	t.Cleanup(release)
	if err := wsjson.Write(ctx, conn, wsMessage{
		Type: "message", Content: "cancel late", SessionID: "sess-cancel-late", RequestID: "req-cancel-late", UserID: "desktop-user",
	}); err != nil {
		t.Fatalf("send ws message failed: %v", err)
	}

	chunks <- &adapter.ReplyChunk{Content: "首帧"}
	var first wsMessage
	if err := wsjson.Read(ctx, conn, &first); err != nil {
		t.Fatalf("read first chunk failed: %v", err)
	}
	if first.Content != "首帧" || first.Done {
		t.Fatalf("first chunk = %+v", first)
	}
	select {
	case <-writeStarted:
	case <-ctx.Done():
		t.Fatal("first frame write did not reach the controlled return window")
	}

	if err := wsjson.Write(ctx, conn, wsMessage{
		Type: "cancel", SessionID: "sess-cancel-late", RequestID: "req-cancel-late", UserID: "desktop-user",
	}); err != nil {
		t.Fatalf("send cancel failed: %v", err)
	}
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("stream handler context was not cancelled")
	}

	snapshot, ok := a.GetStreamSnapshot("desktop-user", "req-cancel-late")
	if !ok || snapshot.Done {
		t.Fatal("cancel acceptance must leave the source stream running")
	}
	// 源退出后只发布真实取消快照，迟到成功内容不能覆盖已接收的部分回复。
	chunks <- &adapter.ReplyChunk{Content: "late success", Done: true}
	close(chunks)
	release()

	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	defer cancelRead()
	var late wsMessage
	if err := wsjson.Read(readCtx, conn, &late); err != nil {
		t.Fatalf("read cancellation terminal: %v", err)
	}
	if late.Type != "stream_snapshot" || !late.Done || late.Status != string(streamstate.StatusCancelled) || late.Content != "首帧" {
		t.Fatalf("cancellation terminal = %+v", late)
	}
}

func TestWebAdapter_CancelWhileOpeningStreamKeepsCancellationTerminal(t *testing.T) {
	a := New()
	started := make(chan struct{})
	a.SetStreamHandler(func(ctx context.Context, _ *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	conn, ctx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(ctx, conn, wsMessage{Type: "message", Content: "cancel before stream", SessionID: "sess-cancel-opening", RequestID: "req-cancel-opening"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("stream did not start")
	}
	if err := wsjson.Write(ctx, conn, wsMessage{Type: "cancel", SessionID: "sess-cancel-opening", RequestID: "req-cancel-opening"}); err != nil {
		t.Fatal(err)
	}
	var terminal wsMessage
	if err := wsjson.Read(ctx, conn, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "stream_snapshot" || terminal.Status != string(streamstate.StatusCancelled) || !terminal.Done {
		t.Fatalf("user cancellation became an error frame: %+v", terminal)
	}
}

func TestWebAdapter_ResumeStreamSendsSnapshotAndContinuesStreaming(t *testing.T) {
	a := New()
	chunks := make(chan *adapter.ReplyChunk, 4)
	a.SetStreamHandler(func(ctx context.Context, msg *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		return chunks, nil
	})

	primaryConn, primaryCtx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(primaryCtx, primaryConn, wsMessage{
		Type:      "message",
		Content:   "继续",
		SessionID: "sess-resume-1",
		RequestID: "req-resume-1",
		UserID:    "desktop-user",
	}); err != nil {
		t.Fatalf("send primary message failed: %v", err)
	}

	chunks <- &adapter.ReplyChunk{Content: "第一个片段"}

	var first wsMessage
	if err := wsjson.Read(primaryCtx, primaryConn, &first); err != nil {
		t.Fatalf("read primary first chunk failed: %v", err)
	}
	if first.Content != "第一个片段" {
		t.Fatalf("first chunk content = %q", first.Content)
	}

	resumeConn, resumeCtx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(resumeCtx, resumeConn, wsMessage{
		Type:      "resume",
		RequestID: "req-resume-1",
		UserID:    "desktop-user",
	}); err != nil {
		t.Fatalf("send resume request failed: %v", err)
	}

	var snapshot wsMessage
	if err := wsjson.Read(resumeCtx, resumeConn, &snapshot); err != nil {
		t.Fatalf("read stream snapshot failed: %v", err)
	}
	if snapshot.Type != "stream_snapshot" {
		t.Fatalf("snapshot type = %q content=%q, want stream_snapshot", snapshot.Type, snapshot.Content)
	}
	if snapshot.Content != "第一个片段" {
		t.Fatalf("snapshot content = %q", snapshot.Content)
	}
	if snapshot.SessionID != "sess-resume-1" || snapshot.RequestID != "req-resume-1" {
		t.Fatalf("snapshot ids mismatch: session=%q request=%q", snapshot.SessionID, snapshot.RequestID)
	}
	if snapshot.Done {
		t.Fatal("snapshot should not be done while stream is active")
	}

	chunks <- &adapter.ReplyChunk{Content: "第二个片段", Done: true}
	close(chunks)

	var primaryFinal wsMessage
	if err := wsjson.Read(primaryCtx, primaryConn, &primaryFinal); err != nil {
		t.Fatalf("read primary final chunk failed: %v", err)
	}
	var resumedFinal wsMessage
	if err := wsjson.Read(resumeCtx, resumeConn, &resumedFinal); err != nil {
		t.Fatalf("read resumed final chunk failed: %v", err)
	}

	for name, msg := range map[string]wsMessage{"primary": primaryFinal, "resumed": resumedFinal} {
		if msg.Type != "chunk" {
			t.Fatalf("%s final type = %q, want chunk", name, msg.Type)
		}
		if msg.Content != "第二个片段" || !msg.Done {
			t.Fatalf("%s final payload unexpected: %+v", name, msg)
		}
		if msg.SessionID != "sess-resume-1" || msg.RequestID != "req-resume-1" {
			t.Fatalf("%s final ids mismatch: session=%q request=%q", name, msg.SessionID, msg.RequestID)
		}
	}
}

func TestBug20260717_DisconnectCancelsOrphanStreamAfterGrace(t *testing.T) {
	a := New()
	a.disconnectGrace = 20 * time.Millisecond
	started := make(chan struct{})
	canceled := make(chan struct{})
	a.SetStreamHandler(func(ctx context.Context, _ *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		chunks := make(chan *adapter.ReplyChunk)
		close(started)
		go func() {
			<-ctx.Done()
			close(canceled)
			close(chunks)
		}()
		return chunks, nil
	})

	conn, ctx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(ctx, conn, wsMessage{
		Type: "message", Content: "long task", RequestID: "req-orphan", UserID: "desktop-user",
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := conn.Close(websocket.StatusNormalClosure, "test disconnect"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("orphan stream context was not canceled after all subscribers disconnected")
	}
}

func TestBug20260717_ResumeWithinGraceKeepsStreamAlive(t *testing.T) {
	a := New()
	a.disconnectGrace = 250 * time.Millisecond
	started := make(chan struct{})
	canceled := make(chan struct{})
	chunks := make(chan *adapter.ReplyChunk, 1)
	a.SetStreamHandler(func(ctx context.Context, _ *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
		close(started)
		go func() {
			<-ctx.Done()
			close(canceled)
		}()
		return chunks, nil
	})

	primary, primaryCtx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(primaryCtx, primary, wsMessage{
		Type: "message", Content: "resumable", RequestID: "req-grace", UserID: "desktop-user",
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := primary.Close(websocket.StatusNormalClosure, "refresh"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(20 * time.Millisecond)
	resumed, resumedCtx, _ := dialWebAdapter(t, a)
	if err := wsjson.Write(resumedCtx, resumed, wsMessage{
		Type: "resume", RequestID: "req-grace", UserID: "desktop-user",
	}); err != nil {
		t.Fatal(err)
	}
	var snapshot wsMessage
	if err := wsjson.Read(resumedCtx, resumed, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != "stream_snapshot" {
		t.Fatalf("resume frame type = %q content=%q", snapshot.Type, snapshot.Content)
	}

	time.Sleep(a.disconnectGrace + 50*time.Millisecond)
	select {
	case <-canceled:
		t.Fatal("stream was canceled even though a subscriber resumed within the grace period")
	default:
	}

	chunks <- &adapter.ReplyChunk{Content: "done", Done: true}
	close(chunks)
}
