package usecase

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage"
)

func ordinaryFinalReplyFixture(t *testing.T) (Deps, *k12storage.Store, *finalReplyContractTransport, *finalReplyPDFRenderer, FinalReplyDeliveryRequest, []ResolvedDeliveryTarget) {
	t.Helper()
	d, store, transport, renderer, req, targets := newFinalReplyContractFixture(t)
	req.AgentName, req.ObjectKind, req.SourceKind, req.Producer = "default", "im_final_reply", k12.PrintSourceIMFinalReply, messagecontent.ProducerChat
	proof := k12.AcceptedPrivateReplySource{AgentName: req.AgentName, Platform: "dingtalk", InstanceID: targets[0].Target.InstanceID, SenderID: targets[0].Target.ChatID, MessageID: "real-inbound", AssistantMessageID: "real-assistant"}
	req.SourceRef, req.PrivateEvent = proof.AssistantMessageID, &proof
	var err error
	req.ObjectID, err = k12.PrivateReplyObjectID(proof)
	if err != nil {
		t.Fatal(err)
	}
	targets[0].BindingID, err = k12.PrivateReplyAuthorityID(proof)
	if err != nil {
		t.Fatal(err)
	}
	seedAcceptedFinalReply(t, store, req, "ordinary-session")
	return d, store, transport, renderer, req, targets
}

func seedAcceptedFinalReply(t *testing.T, store *k12storage.Store, req FinalReplyDeliveryRequest, session string) {
	t.Helper()
	p := req.PrivateEvent
	request := storage.IMRequestIDForEvent(p.Platform, p.InstanceID, p.MessageID)
	meta, _ := json.Marshal(map[string]string{"agent_name": req.AgentName, "producer_kind": string(req.Producer), "locale": req.Locale})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT OR IGNORE INTO platform_instances(id,provider,name,created_at,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{p.InstanceID, p.Platform, "ordinary-bot"}},
		{`INSERT INTO sessions(id,user_id,platform,instance_id,chat_id,created_at,updated_at) VALUES(?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{session, p.SenderID, p.Platform, "ordinary-bot", p.SenderID}},
		{`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES(?,?,'user','original input',?,CURRENT_TIMESTAMP)`, []any{request + ":user", session, request}},
		{`INSERT INTO messages(id,session_id,role,content,request_id,metadata,created_at) VALUES(?,?,'assistant',?,?,?,CURRENT_TIMESTAMP)`, []any{p.AssistantMessageID, session, req.Message.Content, request, string(meta)}},
	} {
		if _, err := store.DB().ExecContext(t.Context(), stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrdinaryPrivateFinalReplySourceAndReplaySQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := ordinaryFinalReplyFixture(t)
	batch, accepted, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || !accepted || batch.Status != k12.DeliveryBatchDelivered || len(batch.Receipts) != 2 {
		t.Fatalf("ordinary PDF was not delivered: %+v %v", batch, err)
	}
	again, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil || again.BatchID != batch.BatchID || renderer.calls != 1 || len(transport.sends) != 2 {
		t.Fatalf("replay repeated rendering or sending: %+v %v", again, err)
	}
	var count int
	if err := store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM agents WHERE name='default'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ordinary reply created a learner: %d %v", count, err)
	}
}

func TestOrdinaryPrivateFinalReplyRejectsMismatchedSourceSQLite(t *testing.T) {
	for _, field := range []string{"original", "assistant", "owner", "instance", "agent", "body"} {
		t.Run(field, func(t *testing.T) {
			d, store, transport, _, req, targets := ordinaryFinalReplyFixture(t)
			statements := map[string]string{
				"original":  `DELETE FROM messages WHERE role='user'`,
				"assistant": `UPDATE messages SET role='user' WHERE id='real-assistant'`,
				"owner":     `UPDATE sessions SET user_id='someone-else' WHERE id='ordinary-session'`,
				"instance":  `UPDATE sessions SET instance_id='another-bot' WHERE id='ordinary-session'`,
				"agent":     `UPDATE messages SET metadata='{"agent_name":"other"}' WHERE id='real-assistant'`,
				"body":      `UPDATE messages SET content='different result' WHERE id='real-assistant'`,
			}
			if _, err := store.DB().ExecContext(t.Context(), statements[field]); err != nil {
				t.Fatal(err)
			}
			batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
			if err == nil || batch.BatchID != "" || len(transport.sends) != 0 || len(transport.uploads) != 0 {
				t.Fatalf("mismatched %s source accepted: %+v %v", field, batch, err)
			}
		})
	}
}

func TestOrdinaryPrivateFinalReplyPreparationRecoverySQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := ordinaryFinalReplyFixture(t)
	renderer.err = errors.New("renderer temporarily unavailable")
	batch, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err == nil || batch.BatchID != "" || len(transport.sends) != 0 {
		t.Fatalf("render failure lost its boundary: %+v %v", batch, err)
	}
	event, err := store.GetFinalReplyPreparation(t.Context(), req.AgentName, req.SourceRef, "")
	if err != nil || event.Status != "pending" || event.LastError == "" {
		t.Fatalf("missing persistent preparation: %+v %v", event, err)
	}
	renderer.err = nil
	batch, _, err = d.ResumeFinalReplyPreparation(t.Context(), req.AgentName, req.SourceRef)
	if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(transport.sends) != 2 || renderer.calls != 2 {
		t.Fatalf("file-only recovery failed: %+v %v", batch, err)
	}
	if _, _, err = d.ResumeFinalReplyPreparation(t.Context(), req.AgentName, req.SourceRef); err != nil || len(transport.sends) != 2 || renderer.calls != 2 {
		t.Fatalf("recovery replay repeated work: %v", err)
	}
}

func TestOrdinaryPrivateFinalReplyRevisionKeepsOriginalSQLite(t *testing.T) {
	d, store, transport, renderer, req, targets := ordinaryFinalReplyFixture(t)
	original, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := d.ReexportFinalReply(t.Context(), req.AgentName, req.SourceRef, "trusted-reexport-call-1")
	if err != nil || revision.BatchID == original.BatchID || revision.ObjectID == original.ObjectID || revision.ContentDigest != original.ContentDigest || renderer.calls != 2 || len(transport.sends) != 4 {
		t.Fatalf("render revision replaced or regenerated original result: %+v %v", revision, err)
	}
	again, _, err := d.ReexportFinalReply(t.Context(), req.AgentName, req.SourceRef, "trusted-reexport-call-1")
	if err != nil || again.BatchID != revision.BatchID || renderer.calls != 2 || len(transport.sends) != 4 {
		t.Fatalf("revision replay duplicated work: %+v %v", again, err)
	}
	old, err := store.GetDeliveryBatch(t.Context(), req.AgentName, original.BatchID)
	if err != nil || old.ContentDigest != original.ContentDigest || old.Receipts[1].PayloadJSON != original.Receipts[1].PayloadJSON {
		t.Fatalf("original frozen bytes changed: %+v %v", old, err)
	}
}

func TestOrdinaryPrivateFinalReplyKnownUploadCanResumeUnknownCannotSQLite(t *testing.T) {
	for _, status := range []int{400, 503} {
		t.Run(string(rune(status)), func(t *testing.T) {
			d, store, transport, renderer, req, targets := ordinaryFinalReplyFixture(t)
			transport.uploadStatus = status
			first, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets)
			if err == nil || first.BatchID == "" || len(transport.sends) != 0 {
				t.Fatalf("upload failure crossed send boundary: %+v %v", first, err)
			}
			transport.uploadStatus = 200
			resumed, _, err := d.ResumeFinalReplyPreparation(t.Context(), req.AgentName, req.SourceRef)
			if status == 503 {
				if !errors.Is(err, ErrDeliveryQueryUnavailable) || resumed.Status != k12.DeliveryBatchOutcomeUnknown || len(transport.uploads) != 1 || len(transport.sends) != 0 {
					t.Fatalf("unknown preparation repeated external work: %+v %v", resumed, err)
				}
			} else if err != nil || resumed.BatchID != first.BatchID || resumed.Status != k12.DeliveryBatchDelivered || len(transport.uploads) != 2 || len(transport.sends) != 2 {
				t.Fatalf("known zero-send preparation did not resume: %+v %v", resumed, err)
			}
			if renderer.calls != 1 {
				t.Fatal("upload recovery rendered content again")
			}
			var count int
			if err := store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM k12_delivery_batches WHERE agent_name='default'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("recovery created another delivery plan: %d %v", count, err)
			}
		})
	}
}

type versionedFinalReplyRenderer struct {
	*finalReplyPDFRenderer
	style string
}

func (r *versionedFinalReplyRenderer) PDFStyleDigest() string { return r.style }

func TestOrdinaryPrivateFinalReplyDoesNotSilentlyChangeFrozenStyleSQLite(t *testing.T) {
	d, _, transport, renderer, req, targets := ordinaryFinalReplyFixture(t)
	versioned := &versionedFinalReplyRenderer{finalReplyPDFRenderer: renderer, style: "style-a"}
	d.Renderer = versioned
	renderer.err = errors.New("temporary render failure")
	if _, _, err := d.PrepareAndSendFinalReply(t.Context(), req, targets); err == nil {
		t.Fatal("initial preparation failure hidden")
	}
	versioned.style = "style-b"
	renderer.err = nil
	if _, _, err := d.ResumeFinalReplyPreparation(t.Context(), req.AgentName, req.SourceRef); !errors.Is(err, ErrDocumentStyleRevisionConflict) || renderer.calls != 1 || len(transport.sends) != 0 {
		t.Fatalf("resume silently adopted a new style: %v", err)
	}
	revision, _, err := d.ReexportFinalReply(t.Context(), req.AgentName, req.SourceRef, "explicit-new-style")
	if err != nil || revision.Status != k12.DeliveryBatchDelivered || renderer.calls != 2 || len(transport.sends) != 2 {
		t.Fatalf("explicit revision did not adopt current style: %+v %v", revision, err)
	}
}
