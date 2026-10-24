package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/adapter/dingtalk"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
)

// 供应商与渠道仅控制外部结果，HTTP、冻结资产、原助手事务及分页推进使用真实实现。
type mediaDeliveryPageSetupKey struct{}
type mediaDeliveryPageQueryEvent struct {
	id    string
	count int
}
type mediaDeliveryPageControl struct {
	blocked atomic.Bool
	mu      sync.Mutex
	queries map[string]int
	sends   map[string]int
	healthy string
	events  chan mediaDeliveryPageQueryEvent
}
type mediaDeliveryPageTransport struct {
	control *mediaDeliveryPageControl
	id      string
}

func (s *mediaDeliveryPageTransport) Prepare(_ context.Context, raw json.RawMessage) (string, error) {
	var part adapter.DeliveryPart
	if err := json.Unmarshal(raw, &part); err != nil {
		return "", err
	}
	if part.Attachment != nil && (part.Attachment.ID == "" || part.Attachment.Size == 0 || part.Digest == "") {
		return "", errors.New("invalid frozen image part")
	}
	return fmt.Sprintf("prepared:%s:%d", s.id, part.Ordinal), nil
}
func (s *mediaDeliveryPageTransport) Send(_ context.Context, raw json.RawMessage, prepared string) (mediaflow.DeliveryOutcome, error) {
	var part adapter.DeliveryPart
	if err := json.Unmarshal(raw, &part); err != nil {
		return mediaflow.DeliveryOutcome{}, err
	}
	if prepared != fmt.Sprintf("prepared:%s:%d", s.id, part.Ordinal) {
		return mediaflow.DeliveryOutcome{}, errors.New("prepared identity changed")
	}
	s.control.mu.Lock()
	s.control.sends[s.id]++
	s.control.mu.Unlock()
	state := "accepted"
	if s.id == s.control.healthy {
		state = "delivered"
	}
	return mediaflow.DeliveryOutcome{State: state, ReceiptRef: fmt.Sprintf("%s:%d", s.id, part.Ordinal)}, nil
}
func (s *mediaDeliveryPageTransport) Query(_ context.Context, _ string) (mediaflow.DeliveryOutcome, error) {
	s.control.mu.Lock()
	s.control.queries[s.id]++
	count := s.control.queries[s.id]
	s.control.mu.Unlock()
	select {
	case s.control.events <- mediaDeliveryPageQueryEvent{id: s.id, count: count}:
	default:
	}
	if s.control.blocked.Load() {
		return mediaflow.DeliveryOutcome{}, errors.New("controlled old receipt query unavailable")
	}
	return mediaflow.DeliveryOutcome{State: "delivered"}, nil
}

func TestMediaDeliveryPaginationAdvancesPastReceiptErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var generations atomic.Int32
	png64 := lifecyclePNG(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { generations.Add(1); lifecycleImage(w, png64) }))
	defer upstream.Close()
	app := newLifecycleApp(t, upstream.URL, nil)
	call := func(method, url string, body any, key string) (int, http.Header, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, app.app.URL+url, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+lifecycleToken)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := app.app.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, b
	}
	generate := func(key string) *mediaflow.Operation {
		t.Helper()
		status, headers, raw := call("POST", "/api/v1/images/generate", map[string]any{"model": "drawing-model", "prompt": key}, key)
		if status != 200 {
			t.Fatalf("generation HTTP%d: %s", status, raw)
		}
		op, err := app.flow.Read(ctx, "api-user", headers.Get("X-Media-Operation-ID"))
		if err != nil || op.SaveState != "ready" || len(op.Assets) != 1 {
			t.Fatalf("actual generated asset not ready: %+v %v", op, err)
		}
		return op
	}
	oldOperation, healthyOperation := generate("delivery-page-old-image"), generate("delivery-page-healthy-image")
	if err := app.store.CreateSession(ctx, &storage.Session{ID: "delivery-page-session", UserID: "api-user", Platform: "api", Title: "Delivery window verification"}); err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(app.store, config.DefaultConfig().Memory)
	control := &mediaDeliveryPageControl{queries: map[string]int{}, sends: map[string]int{}, events: make(chan mediaDeliveryPageQueryEvent, 128)}
	control.blocked.Store(true)
	backgroundReady := make(chan struct{})
	var readyOnce sync.Once
	unblockBackground := func() { readyOnce.Do(func() { close(backgroundReady) }) }
	defer unblockBackground()
	app.flow.SetDeliveryResolver(func(resolveCtx context.Context, d *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
		if resolveCtx.Value(mediaDeliveryPageSetupKey{}) != true {
			select {
			case <-backgroundReady:
			case <-resolveCtx.Done():
				return nil, nil, resolveCtx.Err()
			}
		}
		transport := &mediaDeliveryPageTransport{control: control, id: d.ID}
		if len(d.Parts) > 0 {
			return transport, nil, nil
		}
		row, err := app.store.GetMessage(resolveCtx, d.MessageID)
		if err != nil {
			return nil, nil, err
		}
		reply := &adapter.Reply{Content: row.Content, Metadata: map[string]string{"locale": "en"}}
		for _, asset := range d.Assets {
			ref := asset
			reply.Attachments = append(reply.Attachments, adapter.Attachment{ID: ref.ID, Type: ref.Kind, Name: path.Base(ref.Path), Mime: ref.MIME, ContentDigest: "sha256:" + ref.Digest, Size: ref.Size, Source: &adapter.AttachmentSource{OpenReader: func(openCtx context.Context) (io.ReadCloser, error) { return app.flow.OpenAsset(openCtx, d.Owner, ref) }}})
		}
		parts, err := dingtalk.ProjectDeliveryParts(reply)
		if err != nil {
			return nil, nil, err
		}
		var plan []json.RawMessage
		for _, part := range parts {
			b, err := json.Marshal(part)
			if err != nil {
				return nil, nil, err
			}
			plan = append(plan, b)
		}
		return transport, plan, nil
	})
	completion := func(request string, op *mediaflow.Operation, resend bool) mediaflow.ReplyCompletion {
		c := mediaflow.ReplyCompletion{Owner: "api-user", SessionID: "delivery-page-session", RequestID: request, Operations: []string{op.ID}, Assets: op.Assets, Platform: "dingtalk", InstanceID: "delivery-page-controlled-instance", ChatID: request}
		if resend {
			c.ResendOperations = []string{op.ID}
		}
		return c
	}
	commit := func(c mediaflow.ReplyCompletion) string {
		t.Helper()
		id := c.RequestID + ":assistant"
		_, err := manager.SaveAssistantReply(mediaflow.WithReplyCompletion(ctx, c), c.SessionID, "Original frozen media reply", session.AssistantMeta{MessageID: id, RequestID: c.RequestID})
		if err != nil {
			t.Fatal(err)
		}
		return mediaflow.DeliveryID(c)
	}
	var oldIDs []string
	setupCtx := context.WithValue(ctx, mediaDeliveryPageSetupKey{}, true)
	for i := 0; i < 16; i++ {
		id := commit(completion(fmt.Sprintf("delivery-page-old-reply-%02d", i), oldOperation, true))
		oldIDs = append(oldIDs, id)
		if err := app.flow.Deliver(setupCtx, "api-user", id); err != nil {
			t.Fatal(err)
		}
		d, err := app.store.MediaRepository().Delivery(ctx, "api-user", id)
		if err != nil || d.State != "accepted" || len(d.Parts) != 2 {
			t.Fatalf("old delivery must be genuinely accepted: %+v %v", d, err)
		}
	}
	// 健康项必须落到第二页，才能捕获丢失翻页或游标停滞的回归。
	largestOldID := ""
	for _, id := range oldIDs {
		if id > largestOldID {
			largestOldID = id
		}
	}
	var healthyCompletion mediaflow.ReplyCompletion
	for candidate := 0; candidate < 4096; candidate++ {
		c := completion(fmt.Sprintf("delivery-page-healthy-reply-%04d", candidate), healthyOperation, false)
		if mediaflow.DeliveryID(c) > largestOldID {
			healthyCompletion = c
			break
		}
	}
	if healthyCompletion.RequestID == "" {
		t.Fatal("could not construct a healthy delivery after the first stable page")
	}
	healthyID := commit(healthyCompletion)
	control.healthy = healthyID
	before, err := app.store.MediaRepository().Delivery(ctx, "api-user", healthyID)
	if err != nil || before.State != "pending" {
		t.Fatalf("healthy delivery was not admitted pending: %+v %v", before, err)
	}
	unblockBackground()
	observed := map[string]int{}
	for {
		complete := true
		for _, id := range oldIDs {
			if observed[id] < 2 {
				complete = false
				break
			}
		}
		if complete {
			break
		}
		select {
		case event := <-control.events:
			observed[event.id] = event.count
		case <-ctx.Done():
			t.Fatalf("could not observe two full old-delivery query passes: %v", ctx.Err())
		}
	}
	status, _, body := call("GET", "/api/v1/media/operations/"+healthyOperation.ID, nil, "")
	if status != 200 {
		t.Fatalf("healthy operation GET HTTP%d: %s", status, body)
	}
	var view struct {
		OperationID     string                     `json:"operation_id"`
		GenerationState string                     `json:"generation_state"`
		SaveState       string                     `json:"save_state"`
		Deliveries      []mediaflow.DeliveryStatus `json:"deliveries"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.OperationID != healthyOperation.ID || view.GenerationState != "succeeded" || view.SaveState != "ready" || len(view.Deliveries) != 1 {
		t.Fatalf("actual operation identity/save changed: %s", body)
	}
	control.mu.Lock()
	healthySendsBefore := control.sends[healthyID]
	oldSendsBefore := 0
	for _, id := range oldIDs {
		oldSendsBefore += control.sends[id]
	}
	control.mu.Unlock()
	healthyStateWithFault := view.Deliveries[0].State
	// 解除同一回执查询故障，不重建operation或delivery，不重新调用生成器。
	control.blocked.Store(false)
	recoveryPoll := time.NewTicker(20 * time.Millisecond)
	defer recoveryPoll.Stop()
	for {
		d, err := app.store.MediaRepository().Delivery(ctx, "api-user", healthyID)
		if err != nil {
			t.Fatal(err)
		}
		if d.State == "delivered" {
			break
		}
		select {
		case <-control.events:
		case <-recoveryPoll.C:
		case <-ctx.Done():
			t.Fatal("same healthy delivery did not recover after old query fault cleared")
		}
	}
	status, _, body = call("GET", "/api/v1/media/operations/"+healthyOperation.ID, nil, "")
	if status != 200 {
		t.Fatalf("control read HTTP%d: %s", status, body)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	oldSendsAfter := 0
	for _, id := range oldIDs {
		oldSendsAfter += control.sends[id]
	}
	healthySendsAfter := control.sends[healthyID]
	control.mu.Unlock()
	if view.OperationID != healthyOperation.ID || view.Deliveries[0].ID != healthyID || view.Deliveries[0].State != "delivered" || generations.Load() != 2 || oldSendsBefore != 32 || oldSendsAfter != oldSendsBefore || healthySendsAfter != 2 {
		t.Fatalf("counterexample recovery changed identity or repeated generation/send: %s", body)
	}
	if healthyStateWithFault != "delivered" || healthySendsBefore != 2 {
		t.Errorf("healthy delivery must complete while old receipt queries still fail: state=%s sends=%d, want delivered and exactly two parts", healthyStateWithFault, healthySendsBefore)
	}
}
