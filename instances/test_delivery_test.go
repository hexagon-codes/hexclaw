package instances

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

type testMessageAdapter struct {
	stubAdapter
	targets []string
	sendErr error
}

func (a *testMessageAdapter) Send(_ context.Context, target string, reply *adapter.Reply) error {
	if !strings.Contains(reply.Content, "HexClaw connection test") {
		panic("unexpected test message")
	}
	a.targets = append(a.targets, target)
	return a.sendErr
}

type testReceiptAdapter struct {
	testMessageAdapter
	ack adapter.DeliveryAck
}

func (a *testReceiptAdapter) SendWithReceipt(ctx context.Context, target string, reply *adapter.Reply) (adapter.DeliveryAck, error) {
	return a.ack, a.Send(ctx, target, reply)
}
func (a *testReceiptAdapter) QueryReceipt(context.Context, string) (adapter.DeliveryAck, error) {
	return a.ack, nil
}

func attachTestMessageAdapter(t *testing.T, mgr *Manager, adp adapter.Adapter) {
	t.Helper()
	inst := &Instance{ID: "test-instance", Provider: "slack", Name: "test-main", Enabled: true, Config: []byte(`{}`)}
	if err := mgr.Upsert(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	mgr.running[inst.Name] = adp
	mgr.metadata[inst.Name] = inst
}

func TestBoundTestDeliveryDeduplicatesAndPersistsReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := sqlitestore.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store.DB())
	if err = mgr.Init(ctx); err != nil {
		t.Fatal(err)
	}
	adp := &testMessageAdapter{}
	attachTestMessageAdapter(t, mgr, adp)
	result, err := mgr.SendBoundTest(ctx, "test-instance", "request-one", []string{"chat-b", "chat-a", "chat-a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(adp.targets) != 2 || adp.targets[0] != "chat-a" || adp.targets[1] != "chat-b" {
		t.Fatalf("wrong targets: %v", adp.targets)
	}
	if !result.Success || result.Pending || len(result.Deliveries) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, delivery := range result.Deliveries {
		if delivery.Status != "accepted" {
			t.Fatalf("basic send must not claim delivery: %+v", delivery)
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlitestore.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mgr = NewManager(store.DB())
	if err = mgr.Init(ctx); err != nil {
		t.Fatal(err)
	}
	replayAdapter := &testMessageAdapter{}
	attachTestMessageAdapter(t, mgr, replayAdapter)
	replay, err := mgr.SendBoundTest(ctx, "test-instance", "request-one", []string{"new-chat"}, "")
	if err != nil || len(replayAdapter.targets) != 0 || len(replay.Deliveries) != 2 {
		t.Fatalf("replay resent or changed snapshot: %+v %v %v", replay, err, replayAdapter.targets)
	}
}

func TestBoundTestDeliveryUnknownDoesNotResend(t *testing.T) {
	mgr, cleanup := newTestManager(t)
	defer cleanup()
	adp := &testMessageAdapter{sendErr: context.DeadlineExceeded}
	attachTestMessageAdapter(t, mgr, adp)
	ctx := context.Background()
	result, err := mgr.SendBoundTest(ctx, "test-instance", "timeout", []string{"chat"}, "")
	if err != nil || result.Success || !result.Pending || result.Deliveries[0].Status != "outcome_unknown" {
		t.Fatalf("unknown lost: %+v %v", result, err)
	}
	restarted := NewManager(mgr.db)
	if err = restarted.Init(ctx); err != nil {
		t.Fatal(err)
	}
	attachTestMessageAdapter(t, restarted, adp)
	replay, err := restarted.SendBoundTest(ctx, "test-instance", "timeout", []string{"chat"}, "")
	if err != nil || len(adp.targets) != 1 || replay.Success || !replay.Pending {
		t.Fatalf("unknown replay resent: %+v %v", replay, err)
	}
	_, err = mgr.db.Exec(`INSERT INTO platform_test_requests VALUES(?,?,?,?)`, "test-instance", "interrupted", `[{"chat_id":"chat","status":"sending"},{"chat_id":"second","status":"not_sent"}]`, time.Now().Add(-time.Second).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	replay, err = restarted.SendBoundTest(ctx, "test-instance", "interrupted", []string{"chat", "second"}, "")
	if err != nil || len(adp.targets) != 1 || replay.Deliveries[0].Status != "outcome_unknown" || replay.Deliveries[1].Status != "not_sent" {
		t.Fatalf("interrupted command resent: %+v %v", replay, err)
	}
}

func TestBoundTestDeliveryNoBindingAndActualReceipt(t *testing.T) {
	mgr, cleanup := newTestManager(t)
	defer cleanup()
	ctx := context.Background()
	adp := &testReceiptAdapter{ack: adapter.DeliveryAck{Status: adapter.DeliveryDelivered, ExternalMessageID: "platform-message"}}
	attachTestMessageAdapter(t, mgr, adp)
	empty, err := mgr.SendBoundTest(ctx, "test-instance", "unbound", nil, "")
	if err != nil || !empty.Success || len(adp.targets) != 0 || !strings.Contains(empty.Message, "No bound conversation") {
		t.Fatalf("unbound sent: %+v %v", empty, err)
	}
	result, err := mgr.SendBoundTest(ctx, "test-instance", "receipt", []string{"chat"}, "")
	if err != nil || !result.Success || result.Deliveries[0].Status != "delivered" || result.Deliveries[0].ExternalMessageID != "platform-message" {
		t.Fatalf("real receipt lost: %+v %v", result, err)
	}
}
