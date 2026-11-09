package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestChatResponseCarriesCanonicalMessageContent(t *testing.T) {
	content := canonicalChatContent("$\\frac{3}{4}$", map[string]string{
		"producer_kind": "quick_chat",
		"locale":        "zh-CN",
	})
	if content == nil || content.ProducerKind != messagecontent.ProducerQuickChat {
		t.Fatalf("canonical content = %#v", content)
	}
	_, manifest := canonicalRenderProjection(content.ProducerKind, content.Locale, content.Markdown, messagecontent.SurfaceDesktop, "desktop-v1")
	payload, err := json.Marshal(ChatResponse{Reply: content.Markdown, SessionID: "s1", MessageContent: content, RenderManifest: manifest})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := got["message_content"]; !ok {
		t.Fatalf("response omitted message_content: %s", payload)
	}
	if _, ok := got["render_manifest"]; !ok {
		t.Fatalf("response omitted render_manifest: %s", payload)
	}
}

func TestHydrateHistoryMessageContentKeepsLegacyStorageReadable(t *testing.T) {
	messages := []*storage.MessageRecord{{
		ID:          "m1",
		Role:        "assistant",
		Content:     "## answer",
		ContentType: "text",
		Metadata:    `{"producer_kind":"rag","locale":"en","attachments":[{"attachment_id":"legacy-file","type":"file","name":"legacy.bin","mime":"application/octet-stream","url":"https://example.invalid/legacy.bin","size":128}]}`,
	}}
	originalMetadata := messages[0].Metadata
	(&Server{}).hydrateMessageContents(context.Background(), "api-user", messages)
	if messages[0].MessageContent == nil {
		t.Fatal("text history must be hydrated with canonical content")
	}
	if messages[0].MessageContent.ProducerKind != messagecontent.ProducerRAG {
		t.Fatalf("producer = %q", messages[0].MessageContent.ProducerKind)
	}
	if err := messages[0].MessageContent.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if messages[0].RenderManifest == nil {
		t.Fatal("text history must carry a traceable RenderManifest")
	}
	if messages[0].RenderManifest.Surface != messagecontent.SurfaceHistory {
		t.Fatalf("history surface = %q", messages[0].RenderManifest.Surface)
	}
	if err := messages[0].RenderManifest.ValidateFor(*messages[0].MessageContent); err != nil {
		t.Fatalf("manifest ValidateFor: %v", err)
	}
	if messages[0].Metadata != originalMetadata || len(messages[0].MessageContent.Attachments) != 0 {
		t.Fatal("legacy attachments must remain readable without invented canonical file facts")
	}
}

func TestHydrateHistoryKeepsExistingAttachmentRefsWithoutRegistry(t *testing.T) {
	ref := messagecontent.AttachmentRef{AssetID: "existing-asset", Name: "existing.bin", MIME: "application/octet-stream", Digest: "sha256:existing", AltText: "existing.bin"}
	content, err := messagecontent.New(messagecontent.ProducerChat, "und", "existing answer", []messagecontent.AttachmentRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	message := &storage.MessageRecord{ID: "existing", Role: "assistant", Content: content.Markdown, MessageContent: &content}
	(&Server{}).hydrateMessageContents(context.Background(), "api-user", []*storage.MessageRecord{message})
	if message.MessageContent.SourceDigest != content.SourceDigest || len(message.MessageContent.Attachments) != 1 || message.RenderManifest == nil {
		t.Fatalf("existing refs changed without a registry: %+v", message)
	}
	if err := message.RenderManifest.ValidateFor(*message.MessageContent); err != nil {
		t.Fatal(err)
	}
	if len(message.RenderManifest.Parts) != 2 || message.RenderManifest.Parts[1].ArtifactRef != ref.AssetID || message.RenderManifest.Parts[1].ArtifactDigest != ref.Digest {
		t.Fatalf("manifest omitted existing refs: %+v", message.RenderManifest)
	}
}

func TestHydrateHistorySkipsOpaqueMultimodalJSON(t *testing.T) {
	messages := []*storage.MessageRecord{{
		ID:          "m2",
		Content:     `[{"type":"image"}]`,
		ContentType: "multimodal_json",
	}}
	hydrateMessageContents(messages)
	if messages[0].MessageContent != nil {
		t.Fatal("opaque multimodal JSON must remain on the legacy compatibility path")
	}
}

func TestHistoryCanonicalAttachmentsSurviveSQLiteAndHTTP(t *testing.T) {
	store := newTestStoreForAPI(t)
	provider, ok := store.(interface {
		ExternalToolExecutionRepository() storage.ExternalToolExecutionRepository
	})
	if !ok {
		t.Fatal("SQLite fixture has no external execution repository")
	}
	repository := provider.ExternalToolExecutionRepository()
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "history-canonical-fixture"
	server := NewServer(cfg, &mockEngine{reply: &adapter.Reply{Content: "unused"}}, nil, store)
	// 只读取真实登记元数据；若历史投影打开文件来源，空 BlobStore 会暴露该错误。
	server.SetExternalTools(externaltools.New(t.Context(), repository, nil))
	app := httptest.NewServer(server.routes())
	defer app.Close()
	request := func(method, route, body string) *http.Response {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, app.URL+route, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
		r.Header.Set("Content-Type", "application/json")
		response, err := app.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode >= 300 {
			response.Body.Close()
			t.Fatalf("%s %s status=%d", method, route, response.StatusCode)
		}
		return response
	}
	created := request(http.MethodPost, "/api/v1/sessions", `{"id":"history-files","title":"History files"}`)
	created.Body.Close()
	owned, err := store.GetSession(t.Context(), "history-files")
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(store, cfg.Memory)
	const digest = "sha256:5b1d86ab71007b31c199837744f804da9206201ddde7c06d9b3ca5c677275803"
	wanted := map[string]*messagecontent.MessageContent{}
	wantedRequests := map[string]string{}
	for _, sample := range []struct {
		id, content string
		workflow    bool
		forwarded   bool
	}{{id: "partial", content: "partial answer"}, {id: "attachment-only"}, {id: "workflow", content: "partial workflow answer", workflow: true}, {id: "forwarded", content: "partial forwarded answer", forwarded: true}} {
		requestID := "request-" + sample.id
		savedRequestID, producer, locale := requestID, messagecontent.ProducerChat, "und"
		if sample.workflow {
			// 工作流历史保留空请求字段，附件身份仍来自原工具账本。
			savedRequestID, producer, locale = "", messagecontent.ProducerWorkflow, "en"
		}
		if sample.forwarded {
			// 后续消息转引同一已登记文件，不改变原工具请求身份。
			savedRequestID = "forwarded-request"
		}
		wantedRequests[sample.id] = savedRequestID
		artifact := externaltools.Artifact{ID: "asset-" + sample.id, Name: sample.id + ".bin", MIME: "application/octet-stream", Digest: digest, Path: "registered/" + sample.id + ".bin", Size: 128, Deliver: true}
		record, err := repository.Prepare(t.Context(), storage.ExternalToolExecution{
			ID: "execution-" + sample.id, OwnerID: owned.UserID, SessionID: owned.ID, RequestID: requestID,
			Kind: "tool", LogicalCallKey: requestID, InputDigest: requestID,
		})
		if err != nil {
			t.Fatal(err)
		}
		record, err = repository.Update(t.Context(), owned.UserID, record.ID, record.Revision, storage.ExternalToolExecutionUpdate{Phase: storage.ExternalExecutionDispatched})
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := json.Marshal(struct {
			Artifacts []externaltools.Artifact `json:"artifacts"`
		}{Artifacts: []externaltools.Artifact{artifact}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.Update(t.Context(), owned.UserID, record.ID, record.Revision, storage.ExternalToolExecutionUpdate{Phase: storage.ExternalExecutionObserved, Outcome: "success", ResultManifestJSON: manifest}); err != nil {
			t.Fatal(err)
		}
		attachment := adapter.Attachment{ID: artifact.ID, Name: artifact.Name, Type: "file", Mime: artifact.MIME, ContentDigest: artifact.Digest, Size: artifact.Size, URL: "/api/v1/files/generated/" + artifact.Path}
		ctx := storage.WithAssistantCompletion(t.Context(), storage.AssistantCompletion{Attachments: []adapter.Attachment{attachment}})
		var events []adapter.SequencedRuntimeEvent
		if err := json.Unmarshal([]byte(`[{"sequence":1,"event":{"version":1,"event_id":"terminal:failed","kind":"terminal","terminal_status":"failed"}}]`), &events); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.SaveAssistantReply(ctx, owned.ID, sample.content, session.AssistantMeta{MessageID: sample.id, RequestID: savedRequestID, ReplyMetadata: map[string]string{"producer_kind": string(producer), "locale": locale}, RuntimeEvents: events, LastSequence: 1}); err != nil {
			t.Fatal(err)
		}
		canonical, err := messagecontent.New(producer, locale, sample.content, []messagecontent.AttachmentRef{{AssetID: artifact.ID, Name: artifact.Name, MIME: artifact.MIME, Digest: artifact.Digest, AltText: artifact.Name}})
		if err != nil {
			t.Fatal(err)
		}
		wanted[sample.id] = &canonical
	}
	// 相同字节再次登记会共享路径，但不能替换旧转引保存的资产身份。
	newer, err := repository.Prepare(t.Context(), storage.ExternalToolExecution{
		ID: "execution-0-newer-forwarded", OwnerID: owned.UserID, SessionID: owned.ID, RequestID: "request-newer-forwarded",
		Kind: "tool", LogicalCallKey: "newer-forwarded", InputDigest: "newer-forwarded",
	})
	if err != nil {
		t.Fatal(err)
	}
	newerManifest, err := json.Marshal(struct {
		Artifacts []externaltools.Artifact `json:"artifacts"`
	}{Artifacts: []externaltools.Artifact{{ID: "asset-newer-forwarded", Name: "forwarded.bin", MIME: "application/octet-stream", Digest: digest, Path: "registered/forwarded.bin", Size: 128, Deliver: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Update(t.Context(), owned.UserID, newer.ID, newer.Revision, storage.ExternalToolExecutionUpdate{Phase: storage.ExternalExecutionObserved, Outcome: "success", ResultManifestJSON: newerManifest}); err != nil {
		t.Fatal(err)
	}
	checkMessage := func(message *storage.MessageRecord) {
		t.Helper()
		if message.RequestID != wantedRequests[message.ID] {
			t.Fatalf("history request identity changed: id=%s request=%s", message.ID, message.RequestID)
		}
		want := wanted[message.ID]
		if want == nil {
			t.Fatalf("unexpected history message %s", message.ID)
		}
		if message.MessageContent == nil || message.MessageContent.SourceDigest != want.SourceDigest || len(message.MessageContent.Attachments) != 1 {
			t.Fatalf("history canonical omitted original attachment: got=%+v want=%+v", message.MessageContent, want)
		}
		if message.RenderManifest == nil || message.RenderManifest.ValidateFor(*message.MessageContent) != nil {
			t.Fatalf("history manifest is invalid: %+v", message.RenderManifest)
		}
		artifactParts := 0
		for _, part := range message.RenderManifest.Parts {
			if part.Kind == messagecontent.PartArtifact {
				artifactParts++
				if part.ArtifactRef != want.Attachments[0].AssetID || part.ArtifactDigest != digest {
					t.Fatalf("history artifact differs: %+v", part)
				}
			}
		}
		if artifactParts != 1 || len(message.RuntimeEvents) != 1 || message.RuntimeEvents[0].Event.TerminalStatus != "failed" {
			t.Fatalf("history lost artifact or failed terminal: parts=%d events=%+v", artifactParts, message.RuntimeEvents)
		}
		var metadata struct {
			Attachments []adapter.Attachment `json:"attachments"`
		}
		if err := json.Unmarshal([]byte(message.Metadata), &metadata); err != nil || len(metadata.Attachments) != 1 || metadata.Attachments[0].Size != 128 || metadata.Attachments[0].ID != want.Attachments[0].AssetID {
			t.Fatalf("legacy attachment metadata changed: %+v err=%v", metadata, err)
		}
	}
	history := request(http.MethodGet, "/api/v1/sessions/history-files/messages", "")
	var body struct {
		Messages []*storage.MessageRecord `json:"messages"`
	}
	if err := json.NewDecoder(history.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	history.Body.Close()
	if len(body.Messages) != 4 {
		t.Fatalf("history messages=%d", len(body.Messages))
	}
	for _, message := range body.Messages {
		checkMessage(message)
	}
	search := request(http.MethodGet, "/api/v1/messages/search?q=partial", "")
	var matches struct {
		Results []*storage.SearchResult `json:"results"`
	}
	if err := json.NewDecoder(search.Body).Decode(&matches); err != nil {
		t.Fatal(err)
	}
	search.Body.Close()
	if len(matches.Results) != 3 {
		t.Fatalf("search results=%d", len(matches.Results))
	}
	for _, result := range matches.Results {
		checkMessage(result.Message)
	}
}
