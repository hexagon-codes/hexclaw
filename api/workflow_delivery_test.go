package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/storage"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
	"github.com/hexagon-codes/toolkit/blobstore"
)

// 只替换真正的钉钉网络边界，运行持久化与文件存储保持真实。
type workflowDeliveryTestTransport struct {
	sends    map[int]int
	prepares map[int]int
	failOnce int
	queries  int
	accepted bool
	payloads []adapter.DeliveryPart
}

func (transport *workflowDeliveryTestTransport) PrepareDeliveryPartResource(_ context.Context, _ string, part adapter.DeliveryPart) (string, error) {
	transport.prepares[part.Ordinal]++
	return "prepared-media", nil
}

func (transport *workflowDeliveryTestTransport) SendPreparedPartWithReceipt(_ context.Context, _, _ string, part adapter.DeliveryPart) (adapter.DeliveryAck, error) {
	transport.sends[part.Ordinal]++
	transport.payloads = append(transport.payloads, part)
	if transport.failOnce == part.Ordinal && transport.sends[part.Ordinal] == 1 {
		return adapter.DeliveryAck{Status: adapter.DeliveryFailed}, errors.New("definite provider rejection")
	}
	if transport.accepted {
		return adapter.DeliveryAck{ExternalMessageID: "provider-id", Status: adapter.DeliveryAccepted}, nil
	}
	return adapter.DeliveryAck{ExternalMessageID: "provider-id", Status: adapter.DeliveryDelivered}, nil
}

func (transport *workflowDeliveryTestTransport) QueryReceipt(_ context.Context, _, externalID string) (adapter.DeliveryAck, error) {
	transport.queries++
	return adapter.DeliveryAck{ExternalMessageID: externalID, Status: adapter.DeliveryDelivered}, nil
}

func newWorkflowDeliveryTestTransport() *workflowDeliveryTestTransport {
	return &workflowDeliveryTestTransport{sends: map[int]int{}, prepares: map[int]int{}}
}

func workflowDeliveryServer(t *testing.T) *Server {
	t.Helper()
	s := newWorkflowTestServer()
	s.workflowStore.runsFilePath = filepath.Join(t.TempDir(), "runs.json")
	store, err := sqlitestore.New(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.store = store
	s.genStore, err = blobstore.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.genStore.Close() })
	return s
}

func workflowDeliveryRun(id string, config map[string]any) *WorkflowRun {
	wf := contractWorkflow(contractNode("out", "output", map[string]any{"delivery": config}))
	return &WorkflowRun{ID: id, WorkflowID: wf.ID, Status: "completed", Output: "final answer", Definition: wf,
		Request: &RunWorkflowRequest{UserID: "owner", SessionID: "source"}, StartedAt: time.Now(), FinishedAt: time.Now(),
		NodeResults: []WorkflowNodeRun{{NodeID: "out", Type: "output", Status: nodeStatusCompleted, Output: "final answer"}}}
}

func TestWorkflowDeliveryConversationPersistsAndRecoveryIsIdempotent(t *testing.T) {
	for _, mode := range []string{"source", "workflow"} {
		t.Run(mode, func(t *testing.T) {
			s := workflowDeliveryServer(t)
			if mode == "source" {
				if err := s.store.CreateSession(t.Context(), &storage.Session{ID: "source", UserID: "owner", Platform: "desktop", Title: "source", Status: 1}); err != nil {
					t.Fatal(err)
				}
			}
			run := workflowDeliveryRun("run-conversation", map[string]any{"session": map[string]any{"mode": mode}})
			run.Deliveries = prepareWorkflowDeliveries(run)
			if err := s.workflowStore.addRun(run); err != nil {
				t.Fatal(err)
			}
			s.dispatchWorkflowDelivery(t.Context(), run.ID)
			final, delivery := s.workflowDeliverySnapshot(run.ID, run.Deliveries[0].ID)
			if final.Status != "completed" || delivery.Status != "delivered" {
				t.Fatalf("calculation or delivery status: %+v", delivery)
			}
			messages, err := s.store.ListMessages(t.Context(), delivery.SessionID, 10, 0)
			if err != nil || len(messages) != 1 || messages[0].Content != "final answer" {
				t.Fatalf("persisted conversation output: %+v %v", messages, err)
			}
			// 模拟消息提交后、投递回执提交前中断，再从真实运行文件重载。
			if err = s.updateWorkflowDelivery(run.ID, delivery.ID, func(current *WorkflowDelivery) error { current.Status = "sending"; return nil }); err != nil {
				t.Fatal(err)
			}
			restarted := newWorkflowTestServer()
			restarted.store, restarted.genStore = s.store, s.genStore
			restarted.workflowStore.runsFilePath = s.workflowStore.runsFilePath
			restarted.workflowStore.loadRunsFromFile()
			if err = restarted.RecoverWorkflowDeliveries(t.Context()); err != nil {
				t.Fatal(err)
			}
			_, recovered := restarted.workflowDeliverySnapshot(run.ID, delivery.ID)
			sess, err := s.store.GetSession(t.Context(), delivery.SessionID)
			if err != nil || recovered.Status != "delivered" || sess.MessageCount != 1 {
				t.Fatalf("recovery duplicated or lost output: %+v %+v %v", recovered, sess, err)
			}
			if mode == "workflow" && (sess.Title != run.Definition.Name || sess.Platform != "desktop") {
				t.Fatalf("workflow conversation is not visible with its own title: %+v", sess)
			}
		})
	}
}

func TestWorkflowDeliverySourceConversationOwnership(t *testing.T) {
	s := workflowDeliveryServer(t)
	if err := s.store.CreateSession(t.Context(), &storage.Session{ID: "source", UserID: "another-owner", Platform: "desktop", Status: 1}); err != nil {
		t.Fatal(err)
	}
	run := workflowDeliveryRun("run-ownership", map[string]any{"session": map[string]any{"mode": "source"}})
	run.Deliveries = prepareWorkflowDeliveries(run)
	if err := s.workflowStore.addRun(run); err != nil {
		t.Fatal(err)
	}
	s.dispatchWorkflowDelivery(t.Context(), run.ID)
	final, delivery := s.workflowDeliverySnapshot(run.ID, run.Deliveries[0].ID)
	messages, err := s.store.ListMessages(t.Context(), "source", 10, 0)
	if err != nil || len(messages) != 0 || delivery.Status != "failed" || final.Status != "completed" {
		t.Fatalf("source conversation ownership was bypassed: %+v %+v %v", delivery, messages, err)
	}
}

func workflowArtifactFixture(t *testing.T, name, mimeType string, raw []byte) (messagecontent.AttachmentRef, adapter.Attachment) {
	t.Helper()
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	return messagecontent.AttachmentRef{AssetID: "attachment:" + digest, Name: name, MIME: mimeType, Digest: "sha256:" + digest, AltText: name},
		adapter.Attachment{Type: "file", Name: name, Mime: mimeType, Data: base64.StdEncoding.EncodeToString(raw)}
}

func TestWorkflowDeliveryPartsKeepArtifactsAndRetryOnlyFailedPart(t *testing.T) {
	s := workflowDeliveryServer(t)
	imageRef, image := workflowArtifactFixture(t, "result.png", "image/png", []byte("image-result-bytes"))
	pdfRef, pdf := workflowArtifactFixture(t, "report.pdf", "application/pdf", []byte("%PDF-1.7\nreport-result"))
	canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", "final answer", []messagecontent.AttachmentRef{imageRef, pdfRef})
	if err != nil {
		t.Fatal(err)
	}
	run := workflowDeliveryRun("run-parts", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}, "session": map[string]any{"mode": "workflow"}})
	run.NodeResults[0].MessageContent = &canonical
	run.ArtifactSnapshots = s.freezeWorkflowArtifacts(t.Context(), &adapter.Reply{MessageContent: &canonical, Attachments: []adapter.Attachment{image, pdf}})
	for _, snapshot := range run.ArtifactSnapshots {
		if snapshot.Error != "" || snapshot.FilePath == "" {
			t.Fatalf("artifact was not durably frozen: %+v", snapshot)
		}
	}
	run.Deliveries = prepareWorkflowDeliveries(run)
	if err = s.workflowStore.addRun(run); err != nil {
		t.Fatal(err)
	}
	transport := newWorkflowDeliveryTestTransport()
	transport.failOnce = 3
	s.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, "", false, transport)
	var dingID, sessionID string
	for _, delivery := range run.Deliveries {
		if delivery.Kind == "dingtalk" {
			dingID = delivery.ID
		} else {
			sessionID = delivery.SessionID
		}
	}
	final, delivery := s.workflowDeliverySnapshot(run.ID, dingID)
	if final.Status != "completed" || delivery.Status != "failed" || len(delivery.Parts) != 3 || transport.sends[1] != 1 || transport.sends[2] != 1 || transport.sends[3] != 1 {
		t.Fatalf("first delivery did not preserve all parts: %+v sends=%v", delivery, transport.sends)
	}
	s.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, dingID, true, transport)
	_, delivery = s.workflowDeliverySnapshot(run.ID, dingID)
	if delivery.Status != "delivered" || transport.sends[1] != 1 || transport.sends[2] != 1 || transport.sends[3] != 2 || transport.prepares[2] != 1 || transport.prepares[3] != 1 {
		t.Fatalf("retry resent completed parts or reuploaded durable resources: %+v sends=%v prepares=%v", delivery, transport.sends, transport.prepares)
	}
	messages, err := s.store.ListMessages(t.Context(), sessionID, 10, 0)
	var metadata struct {
		Attachments []adapter.Attachment `json:"attachments"`
	}
	if err != nil || len(messages) != 1 || json.Unmarshal([]byte(messages[0].Metadata), &metadata) != nil || len(metadata.Attachments) != 2 {
		t.Fatalf("conversation lost artifact references: %+v %v", messages, err)
	}
	for _, attachment := range metadata.Attachments {
		if attachment.Data != "" || attachment.URL == "" {
			t.Fatalf("conversation did not reuse generated file storage: %+v", attachment)
		}
	}
}

func TestWorkflowDeliveryUnknownIsQueryOnlyAndNoLedgerDoesNotSend(t *testing.T) {
	s := workflowDeliveryServer(t)
	transport := newWorkflowDeliveryTestTransport()
	for _, externalID := range []string{"provider-key", ""} {
		id := "run-unknown-" + externalID
		run := workflowDeliveryRun(id, map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
		run.Deliveries = prepareWorkflowDeliveries(run)
		run.Deliveries[0].Status, run.Deliveries[0].Parts[0].Status = "sending", "sending"
		run.Deliveries[0].Parts[0].ExternalMessageID = externalID
		if err := s.workflowStore.addRun(run); err != nil {
			t.Fatal(err)
		}
	}
	old := workflowDeliveryRun("run-old-no-ledger", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
	if err := s.workflowStore.addRun(old); err != nil {
		t.Fatal(err)
	}
	restarted := newWorkflowTestServer()
	restarted.store, restarted.genStore = s.store, s.genStore
	restarted.workflowStore.runsFilePath = s.workflowStore.runsFilePath
	restarted.workflowStore.loadRunsFromFile()
	if err := restarted.RecoverWorkflowDeliveries(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run-unknown-provider-key", "run-unknown-", "run-old-no-ledger"} {
		restarted.dispatchWorkflowDeliveryWithTransport(t.Context(), id, "", false, transport)
	}
	if len(transport.sends) != 0 || transport.queries != 1 {
		t.Fatalf("unknown or legacy run was resent: sends=%v queries=%d", transport.sends, transport.queries)
	}
	_, unresolved := restarted.workflowDeliverySnapshot("run-unknown-", s.workflowStore.runs["run-unknown-"].Deliveries[0].ID)
	if unresolved.Status != "outcome_unknown" || unresolved.Error == "" {
		t.Fatalf("unqueryable delivery was hidden: %+v", unresolved)
	}
}

func TestWorkflowDeliveryAcceptedConvergesWithoutResending(t *testing.T) {
	s := workflowDeliveryServer(t)
	run := workflowDeliveryRun("run-accepted", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
	run.Deliveries = prepareWorkflowDeliveries(run)
	if err := s.workflowStore.addRun(run); err != nil {
		t.Fatal(err)
	}
	transport := newWorkflowDeliveryTestTransport()
	transport.accepted = true
	s.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, "", false, transport)
	_, delivery := s.workflowDeliverySnapshot(run.ID, run.Deliveries[0].ID)
	if delivery.Status != "delivered" || transport.sends[1] != 1 || transport.queries != 1 {
		t.Fatalf("provider acceptance did not converge by query: %+v sends=%v queries=%d", delivery, transport.sends, transport.queries)
	}
}

func TestWorkflowDeliveryWriteFailurePreventsExternalSend(t *testing.T) {
	s := workflowDeliveryServer(t)
	run := workflowDeliveryRun("run-write-failure", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
	run.Deliveries = prepareWorkflowDeliveries(run)
	if err := s.workflowStore.addRun(run); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(s.workflowStore.runsFilePath)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := s.workflowStore.runsFilePath
	s.workflowStore.runsFilePath = t.TempDir()
	transport := newWorkflowDeliveryTestTransport()
	s.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, "", false, transport)
	_, delivery := s.workflowDeliverySnapshot(run.ID, run.Deliveries[0].ID)
	current, err := os.ReadFile(originalPath)
	if err != nil || len(transport.sends) != 0 || delivery.Status != "pending" || string(current) != string(original) {
		t.Fatalf("send crossed an uncommitted ledger: %+v sends=%v err=%v", delivery, transport.sends, err)
	}
}

func TestWorkflowDeliveryUnsupportedAndMissingArtifactsDoNotDisappear(t *testing.T) {
	for _, test := range []struct {
		name string
		mime string
		data bool
	}{{"unsupported", "application/zip", true}, {"missing", "application/pdf", false}} {
		t.Run(test.name, func(t *testing.T) {
			s := workflowDeliveryServer(t)
			ref, attachment := workflowArtifactFixture(t, "result.bin", test.mime, []byte("result-bytes"))
			canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, "und", "final answer", []messagecontent.AttachmentRef{ref})
			if err != nil {
				t.Fatal(err)
			}
			run := workflowDeliveryRun("run-artifact-failure", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
			run.NodeResults[0].MessageContent = &canonical
			if test.data {
				run.ArtifactSnapshots = s.freezeWorkflowArtifacts(t.Context(), &adapter.Reply{MessageContent: &canonical, Attachments: []adapter.Attachment{attachment}})
			}
			run.Deliveries = prepareWorkflowDeliveries(run)
			if err = s.workflowStore.addRun(run); err != nil {
				t.Fatal(err)
			}
			transport := newWorkflowDeliveryTestTransport()
			s.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, "", false, transport)
			final, delivery := s.workflowDeliverySnapshot(run.ID, run.Deliveries[0].ID)
			if final.Status != "completed" || delivery.Status != "failed" || delivery.Error == "" || len(delivery.Parts) != 2 || len(transport.sends) != 0 {
				t.Fatalf("attachment failure was hidden by text success: %+v sends=%v", delivery, transport.sends)
			}
		})
	}
}

func TestWorkflowDeliveryPureArtifactsKeepManifestIdentity(t *testing.T) {
	s := workflowDeliveryServer(t)
	imageRef, image := workflowArtifactFixture(t, "image.png", "image/png", []byte("pure-image-result"))
	pdfRef, pdf := workflowArtifactFixture(t, "report.pdf", "application/pdf", []byte("%PDF-1.7\npure-report"))
	canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, "zh-CN", "", []messagecontent.AttachmentRef{imageRef, pdfRef})
	if err != nil {
		t.Fatal(err)
	}
	run := workflowDeliveryRun("run-pure-artifacts", map[string]any{"dingtalk": map[string]any{"enabled": true, "instance_id": "bot-id", "chat_id": "staff-id"}})
	run.Output, run.NodeResults[0].Output, run.NodeResults[0].MessageContent = "", "", &canonical
	run.ArtifactSnapshots = s.freezeWorkflowArtifacts(t.Context(), &adapter.Reply{MessageContent: &canonical, Attachments: []adapter.Attachment{image, pdf}})
	run.Deliveries = prepareWorkflowDeliveries(run)
	if err = s.workflowStore.addRun(run); err != nil {
		t.Fatal(err)
	}
	transport := newWorkflowDeliveryTestTransport()
	s.dispatchWorkflowDeliveryWithTransport(t.Context(), run.ID, "", false, transport)
	_, delivery := s.workflowDeliverySnapshot(run.ID, run.Deliveries[0].ID)
	if delivery.Status != "delivered" || len(delivery.Parts) != 2 || len(transport.payloads) != 2 {
		t.Fatalf("pure artifact result was lost: %+v payloads=%+v", delivery, transport.payloads)
	}
	for index, part := range transport.payloads {
		if part.Ordinal != index+1 || part.Kind != messagecontent.PartArtifact || part.Text != "" || part.Attachment == nil || part.Attachment.Data == "" || part.MessageContent.Markdown != "" {
			t.Fatalf("artifact part changed identity or fabricated a text part: %+v", part)
		}
		if err = part.RenderManifest.ValidateFor(*part.MessageContent); err != nil {
			t.Fatal(err)
		}
	}
}
