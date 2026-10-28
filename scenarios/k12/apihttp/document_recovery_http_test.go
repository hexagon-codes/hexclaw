package apihttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/apihttp"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestPrintableArtifactReexportHTTPPreservesOriginalAndIdempotency(t *testing.T) {
	h := newServer(t)
	rec, out := do(t, h, http.MethodPost, "/print-artifacts", printableArtifactV45Body)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	original := out["artifact"].(map[string]any)["artifact_id"].(string)
	var revision string
	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/print-artifacts/"+original+"/reexport?agent=mingming", nil)
		req.Header.Set("Idempotency-Key", "explicit-export-1")
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		want := http.StatusCreated
		if attempt > 0 {
			want = http.StatusOK
		}
		if recorder.Code != want {
			t.Fatalf("revision status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var result struct {
			Artifact struct {
				ArtifactID string `json:"artifact_id"`
			}
			Replayed              bool
			ModelInvocationsAdded int `json:"model_invocations_added"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Artifact.ArtifactID == original || result.Artifact.ArtifactID == "" || result.ModelInvocationsAdded != 0 {
			t.Fatalf("revision identity invalid: %+v", result)
		}
		if attempt == 0 {
			revision = result.Artifact.ArtifactID
		} else if revision != result.Artifact.ArtifactID || !result.Replayed {
			t.Fatal("same revision key produced another artifact")
		}
	}
	for _, id := range []string{original, revision} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/print-artifacts/"+id+"/content?agent=mingming", nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != "%PDF-1.7\nfixed-http-render" {
			t.Fatalf("frozen file missing: %d %q", recorder.Code, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/print-artifacts/"+original+"/reexport?agent=other", nil)
	request.Header.Set("Idempotency-Key", "different-owner")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-agent revision accepted: %d", recorder.Code)
	}
}

type recoveryHTTPRenderer struct{ fail bool }

func (r *recoveryHTTPRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	if r.fail {
		return nil, "", errors.New("renderer unavailable")
	}
	return []byte("%PDF-1.7\nrecovered-http-document"), "application/pdf", nil
}

func TestFinalReplyRecoveryHTTPUsesOriginalSourceAndOwner(t *testing.T) {
	transport := &httpBatchTransport{}
	renderer := &recoveryHTTPRenderer{fail: true}
	runtime, _ := newCreativeWorkDeliveryHTTPFixture(t, transport, assembly.WithRenderer(renderer))
	runtime.Deps.TextbookOwnerID = "service-owner"
	proof := k12.AcceptedPrivateReplySource{AgentName: "default", Platform: "dingtalk", InstanceID: "recovery-bot-id", SenderID: "sender-1", MessageID: "real-event", AssistantMessageID: "saved-final"}
	req := usecase.FinalReplyDeliveryRequest{AgentName: "default", ObjectKind: "im_final_reply", SourceKind: k12.PrintSourceIMFinalReply, SourceRef: proof.AssistantMessageID, PrivateEvent: &proof, Producer: messagecontent.ProducerK12, Locale: "zh-CN", Message: usecase.DeliveryMessage{Content: strings.Repeat("完整正文", 350)}}
	req.ObjectID, _ = k12.PrivateReplyObjectID(proof)
	authority, _ := k12.PrivateReplyAuthorityID(proof)
	request := storage.IMRequestIDForEvent(proof.Platform, proof.InstanceID, proof.MessageID)
	metadata, _ := json.Marshal(map[string]string{"agent_name": "default", "producer_kind": "k12", "locale": "zh-CN"})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO platform_instances(id,provider,name,created_at,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{proof.InstanceID, proof.Platform, "saved-bot"}},
		{`INSERT INTO sessions(id,user_id,platform,instance_id,chat_id,created_at,updated_at) VALUES('recovery-session',?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, []any{proof.SenderID, proof.Platform, "saved-bot", proof.SenderID}},
		{`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES('original-user','recovery-session','user','original input',?,CURRENT_TIMESTAMP)`, []any{request}},
		{`INSERT INTO messages(id,session_id,role,content,request_id,metadata,created_at) VALUES(?,'recovery-session','assistant',?,?,?,CURRENT_TIMESTAMP)`, []any{proof.AssistantMessageID, req.Message.Content, request, string(metadata)}},
	} {
		if _, err := runtime.Records.DB().ExecContext(t.Context(), stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := runtime.Deps.PrepareAndSendFinalReply(t.Context(), req, []usecase.ResolvedDeliveryTarget{{BindingID: authority, Target: k12.DeliveryTarget{Platform: proof.Platform, InstanceID: proof.InstanceID, ChatID: proof.SenderID}}})
	if err == nil {
		t.Fatal("preparation failure not observed")
	}
	owner := "service-owner"
	h := apihttp.NewHandler(apihttp.Runtime{Views: runtime.Registry.Views, Records: runtime.Records, Deps: runtime.Deps, PrincipalMode: "remote", AuthenticatedOwnerScope: func(context.Context) (string, error) { return owner, nil }})
	rec, out := do(t, h, http.MethodGet, "/final-replies/saved-final?agent=default", "")
	if rec.Code != http.StatusOK || out["state"] != "pending" {
		t.Fatalf("pending state: %d %s", rec.Code, rec.Body.String())
	}
	renderer.fail = false
	for attempt := 0; attempt < 2; attempt++ {
		rec, out = do(t, h, http.MethodPost, "/final-replies/saved-final/resume?agent=default", "")
		if rec.Code != http.StatusOK || out["batch"].(map[string]any)["status"] != "delivered" || len(transport.envelopeSends) != 1 {
			t.Fatalf("resume repeated send or lost PDF: %d %s", rec.Code, rec.Body.String())
		}
	}
	reexport := httptest.NewRequest(http.MethodPost, "/final-replies/saved-final/reexport?agent=default", nil)
	reexport.Header.Set("Idempotency-Key", "explicit-api-reexport")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, reexport)
	if recorder.Code != http.StatusOK || len(transport.envelopeSends) != 2 {
		t.Fatalf("explicit re-export did not deliver: %d %s", recorder.Code, recorder.Body.String())
	}
	owner = "unrelated-owner"
	rec, _ = do(t, h, http.MethodGet, "/final-replies/saved-final?agent=default", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner read accepted: %d", rec.Code)
	}
}
