package instances

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type photoRuntimeTransport struct {
	stubAdapter
	intake    adapter.PhotoIntake
	notices   atomic.Int32
	finals    atomic.Int32
	queries   atomic.Int32
	recalls   atomic.Int32
	noticeAck adapter.DeliveryAck
}

func (p *photoRuntimeTransport) SetPhotoIntake(in adapter.PhotoIntake) { p.intake = in }
func (p *photoRuntimeTransport) Platform() adapter.Platform            { return adapter.PlatformDingtalk }
func (p *photoRuntimeTransport) SendWithReceipt(_ context.Context, chat string, msg *adapter.Reply) (adapter.DeliveryAck, error) {
	if chat != "family" {
		return adapter.DeliveryAck{Status: adapter.DeliveryFailed}, errors.New("wrong recipient")
	}
	if msg.Content == "收件" {
		p.notices.Add(1)
		return p.noticeAck, nil
	}
	p.finals.Add(1)
	return adapter.DeliveryAck{Status: adapter.DeliveryAccepted, ExternalMessageID: "final"}, nil
}
func (p *photoRuntimeTransport) QueryReceipt(_ context.Context, id string) (adapter.DeliveryAck, error) {
	if id != "notice" && id != "final" {
		return adapter.DeliveryAck{}, errors.New("unknown receipt")
	}
	p.queries.Add(1)
	return adapter.DeliveryAck{Status: adapter.DeliveryDelivered, ExternalMessageID: id}, nil
}
func (p *photoRuntimeTransport) SendPhotoReplyWithReceipts(ctx context.Context, chat string, reply *adapter.Reply) ([]adapter.DeliveryAck, error) {
	ack, err := p.SendWithReceipt(ctx, chat, reply)
	return []adapter.DeliveryAck{ack}, err
}
func (p *photoRuntimeTransport) RecallMessage(_ context.Context, id string) error {
	if id != "notice" {
		return errors.New("wrong notice")
	}
	p.recalls.Add(1)
	return nil
}

func photoRuntimeSetup(t *testing.T) (*Manager, *Instance) {
	t.Helper()
	m, closeDB := newTestManager(t)
	t.Cleanup(closeDB)
	if err := migrate.Run(t.Context(), m.db, []migrate.Migration{migrate.All[0], migrate.IMPhotoIntakesV152}); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Provider: "dingtalk", ID: "stable", Name: "family-bot", Enabled: true, Config: []byte(`{"app_key":"qa-only","app_secret":"qa-only","robot_code":"qa-only"}`)}
	if err := m.Upsert(t.Context(), inst); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background(), inst.Name) })
	return m, inst
}

func runtimePhoto(t *testing.T, m *Manager, inst *Instance, routes PhotoRoutes, h adapter.MessageHandler, ack adapter.DeliveryAck) *photoRuntimeTransport {
	t.Helper()
	port := &photoRuntimeTransport{noticeAck: ack}
	m.buildAdapter = func(*Instance) (adapter.Adapter, error) { return port, nil }
	m.SetPhotoRoutes(routes)
	m.SetHandler(h)
	if err := m.Start(t.Context(), inst.Name); err != nil {
		t.Fatal(err)
	}
	if port.intake == nil {
		t.Fatal("shared intake was not attached to the physical instance")
	}
	return port
}

func runtimeArrival(inst *Instance, event string) (PhotoIntakeKey, adapter.PhotoArrival) {
	m := &adapter.Message{Platform: adapter.PlatformDingtalk, InstanceID: inst.Name, ChatID: "family", UserID: "family", ID: event, Content: "看图", Metadata: map[string]string{"conversation_type": "1", "conversation_id": "dialog"}}
	return PhotoIntakeKey{Platform: "dingtalk", InstanceID: inst.ID, ChatID: "family", ProviderMessageID: event},
		adapter.PhotoArrival{Message: m, PageCount: 1, ReceivedAt: time.Now(), FrameDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(event)))}
}

func waitPhotoRuntime(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("photo receipt did not reach the expected state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSharedPhotoFirstNoticePrecedesBytesAndSourceSelectsOneConsumer(t *testing.T) {
	for _, kind := range []string{"ordinary", "k12"} {
		t.Run(kind, func(t *testing.T) {
			m, inst := photoRuntimeSetup(t)
			var admissions, consumptions atomic.Int32
			key, arrival := runtimeArrival(inst, "two-lanes")
			routes := PhotoRoutes{Prepare: func(_ context.Context, original, canonical *adapter.Message) (PhotoIntakeRoute, error) {
				if original.InstanceID != inst.Name || canonical.InstanceID != inst.ID {
					return PhotoIntakeRoute{}, errors.New("lost original/physical instance")
				}
				admissions.Add(1)
				return PhotoIntakeRoute{Kind: kind, AgentName: "qa", NoticeText: "收件", Locale: "zh"}, nil
			}, CommitK12: func(_ context.Context, msg *adapter.Message, _ PhotoIntakeRoute) error {
				if msg.InstanceID != inst.ID || len(msg.Attachments) != 1 {
					return errors.New("lost photo or physical identity")
				}
				consumptions.Add(1)
				return nil
			}, K12Outcome: func(context.Context, *adapter.Message, PhotoIntakeRoute) (adapter.DeliveryStatus, error) {
				return adapter.DeliveryDelivered, nil
			}}
			h := func(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
				if _, ok := AdmittedPhotoRoute(ctx, msg); !ok || msg.InstanceID != inst.Name || len(msg.Attachments) != 1 {
					return nil, errors.New("lost trusted ordinary input or previous session scope")
				}
				old := msg.Content
				msg.Content = "other"
				_, bad := AdmittedPhotoRoute(ctx, msg)
				msg.Content = old
				if bad {
					return nil, errors.New("modified input retained original authority")
				}
				consumptions.Add(1)
				return &adapter.Reply{Content: "原助手的完整回复"}, nil
			}
			transport := runtimePhoto(t, m, inst, routes, h, adapter.DeliveryAck{Status: adapter.DeliveryAccepted, ExternalMessageID: "notice"})
			started, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				_, err := transport.intake.ReceivePhotos(t.Context(), arrival, func(ctx context.Context, _ int) (adapter.Attachment, error) {
					close(started)
					select {
					case <-ctx.Done():
						return adapter.Attachment{}, ctx.Err()
					case <-release:
					}
					return adapter.Attachment{Type: "image", Mime: "image/png", Name: "sample.png", Data: base64.StdEncoding.EncodeToString([]byte("fixture image"))}, nil
				})
				result <- err
			}()
			select {
			case <-started:
			case err := <-result:
				t.Fatalf("before download: %v", err)
			case <-time.After(4 * time.Second):
				t.Fatal("download never started")
			}
			waitPhotoRuntime(t, func() bool { return transport.notices.Load() == 1 })
			if consumptions.Load() != 0 {
				t.Fatal("business ran before bytes")
			}
			select {
			case <-result:
				t.Fatal("premature platform ACK")
			default:
			}
			close(release)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			store := NewPhotoIntakeStore(m.db)
			waitPhotoRuntime(t, func() bool {
				row, err := store.Get(t.Context(), key)
				return err == nil && row.Final.Status == "delivered" && row.Notice.Status == "recalled"
			})
			handled, err := transport.intake.ReceivePhotos(t.Context(), arrival, func(context.Context, int) (adapter.Attachment, error) {
				return adapter.Attachment{}, errors.New("same callback downloaded twice")
			})
			if !handled || err != nil || consumptions.Load() != 1 || admissions.Load() != 1 || transport.notices.Load() != 1 || transport.recalls.Load() != 1 {
				t.Fatalf("duplicate or missing stage: %v %v, admissions=%d work=%d notices=%d recall=%d", handled, err, admissions.Load(), consumptions.Load(), transport.notices.Load(), transport.recalls.Load())
			}
			if kind == "ordinary" && transport.finals.Load() != 1 {
				t.Fatalf("ordinary final count %d", transport.finals.Load())
			}
		})
	}
}

func TestSharedPhotoUnknownNoticeAndUnknownBusinessDoNotResendAfterRestart(t *testing.T) {
	m, inst := photoRuntimeSetup(t)
	key, arrival := runtimeArrival(inst, "unknown")
	var modelCalls, lookups atomic.Int32
	routes := PhotoRoutes{Prepare: func(context.Context, *adapter.Message, *adapter.Message) (PhotoIntakeRoute, error) {
		return PhotoIntakeRoute{Kind: "ordinary", NoticeText: "收件"}, nil
	},
		RecoverOrdinary: func(context.Context, *adapter.Message, PhotoIntakeRoute) (*adapter.Reply, string, error) {
			lookups.Add(1)
			return nil, "", nil
		}}
	handler := func(context.Context, *adapter.Message) (*adapter.Reply, error) {
		modelCalls.Add(1)
		return nil, context.DeadlineExceeded
	}
	first := runtimePhoto(t, m, inst, routes, handler, adapter.DeliveryAck{Status: adapter.DeliveryOutcomeUnknown})
	_, err := first.intake.ReceivePhotos(t.Context(), arrival, func(context.Context, int) (adapter.Attachment, error) {
		return adapter.Attachment{Type: "image", Mime: "image/png", Data: "eA=="}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewPhotoIntakeStore(m.db)
	waitPhotoRuntime(t, func() bool {
		x, e := store.Get(t.Context(), key)
		return e == nil && x.Notice.Status == "unknown" && x.Work.Status == "unknown"
	})
	if err = m.Stop(t.Context(), inst.Name); err != nil {
		t.Fatal(err)
	}
	lookups.Store(0)
	second := runtimePhoto(t, m, inst, routes, handler, adapter.DeliveryAck{Status: adapter.DeliveryDelivered})
	if err = m.RecoverPhotos(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitPhotoRuntime(t, func() bool { return lookups.Load() > 0 })
	if first.notices.Load() != 1 || second.notices.Load() != 0 || modelCalls.Load() != 1 || second.finals.Load() != 0 {
		t.Fatalf("unknown operations were replayed: %d %d %d %d", first.notices.Load(), second.notices.Load(), modelCalls.Load(), second.finals.Load())
	}
}

func TestSharedPhotoRecoverDelegatedDeliveryBeforeSendingSavedReply(t *testing.T) {
	m, inst := photoRuntimeSetup(t)
	key, arrival := runtimeArrival(inst, "delegated")
	var actual atomic.Int32
	routes := PhotoRoutes{Prepare: func(context.Context, *adapter.Message, *adapter.Message) (PhotoIntakeRoute, error) {
		return PhotoIntakeRoute{Kind: "ordinary", AgentName: "qa", NoticeText: "收件"}, nil
	},
		RecoverOrdinary: func(ctx context.Context, msg *adapter.Message, route PhotoIntakeRoute) (*adapter.Reply, string, error) {
			if actual.Load() == 0 {
				return nil, "", nil
			}
			source, bound := adapter.PlatformInstanceIdentityFromContext(ctx)
			if !bound || source.ID != inst.ID || msg.InstanceID != inst.Name || route.AgentName != "qa" {
				return nil, "", errors.New("lost frozen scope")
			}
			return &adapter.Reply{Content: "已保存的答复"}, "batch:original", nil
		}, OrdinaryOutcome: func(_ context.Context, _ *adapter.Message, _ PhotoIntakeRoute, ref string) (adapter.DeliveryStatus, error) {
			if ref != "batch:original" {
				return "", errors.New("wrong domain batch")
			}
			return adapter.DeliveryFailed, nil
		}}
	var calls atomic.Int32
	h := func(context.Context, *adapter.Message) (*adapter.Reply, error) {
		calls.Add(1)
		return nil, context.DeadlineExceeded
	}
	first := runtimePhoto(t, m, inst, routes, h, adapter.DeliveryAck{Status: adapter.DeliveryAccepted, ExternalMessageID: "notice"})
	_, err := first.intake.ReceivePhotos(t.Context(), arrival, func(context.Context, int) (adapter.Attachment, error) {
		return adapter.Attachment{Type: "image", Mime: "image/png", Data: "eA=="}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewPhotoIntakeStore(m.db)
	waitPhotoRuntime(t, func() bool {
		row, e := s.Get(t.Context(), key)
		return e == nil && row.Work.Status == "unknown" && row.Notice.Status == "delivered"
	})
	if err = m.Stop(t.Context(), inst.Name); err != nil {
		t.Fatal(err)
	}
	actual.Store(1)
	second := runtimePhoto(t, m, inst, routes, h, adapter.DeliveryAck{Status: adapter.DeliveryDelivered})
	if err = m.RecoverPhotos(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitPhotoRuntime(t, func() bool {
		row, e := s.Get(t.Context(), key)
		return e == nil && row.Final.Status == "failed" && row.Notice.Status == "recalled"
	})
	if calls.Load() != 1 || second.finals.Load() != 0 || second.notices.Load() != 0 || second.recalls.Load() != 1 {
		t.Fatalf("recovery dispatched new work or bypassed original batch: %d %d %d %d", calls.Load(), second.finals.Load(), second.notices.Load(), second.recalls.Load())
	}
}

func TestSharedPhotoClaimsOriginalPartialDocumentsOnce(t *testing.T) {
	m, inst := photoRuntimeSetup(t)
	var calls atomic.Int32
	routes := PhotoRoutes{Prepare: func(context.Context, *adapter.Message, *adapter.Message) (PhotoIntakeRoute, error) {
		return PhotoIntakeRoute{Kind: "ordinary", NoticeText: "收件"}, nil
	}}
	h := func(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		calls.Add(1)
		msg.SessionID = "created-by-engine"
		reply := &adapter.Reply{AssistantMessageID: "assistant", Attachments: []adapter.Attachment{{ID: "document-1", Name: "结果.pdf", Mime: "application/pdf", Type: "file"}}}
		prepared, err := m.PrepareReplyDelivery(ctx, msg)
		if !prepared || err != nil {
			return nil, errors.New("missing original delivery claim")
		}
		reply.MarkDeliveryAttempted(msg)
		if err = m.Send(ctx, inst.ID, msg.ChatID, reply); err != nil {
			return nil, err
		}
		return reply, errors.New("later model response failed")
	}
	first := runtimePhoto(t, m, inst, routes, h, adapter.DeliveryAck{Status: adapter.DeliveryOutcomeUnknown})
	key, arrival := runtimeArrival(inst, "partial-document")
	_, err := first.intake.ReceivePhotos(t.Context(), arrival, func(context.Context, int) (adapter.Attachment, error) {
		return adapter.Attachment{Type: "image", Mime: "image/png", Data: "eA=="}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := NewPhotoIntakeStore(m.db)
	waitPhotoRuntime(t, func() bool {
		row, e := s.Get(t.Context(), key)
		return e == nil && row.Work.Status == "completed" && row.Final.Status == "delivered"
	})
	row, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	var output ordinaryPhotoOutput
	if err = json.Unmarshal([]byte(row.Work.ResultRef), &output); err != nil || len(output.DocumentIDs) != 1 || output.DocumentIDs[0] != "document-1" || first.finals.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("partial document was changed or resent: %#v sends=%d calls=%d err=%v", output, first.finals.Load(), calls.Load(), err)
	}
}
