package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/hexclaw/storage"
)

type documentHTTPTextRenderer struct{ path string }

func (r documentHTTPTextRenderer) Render(_ context.Context, content string, format render.Format, _ render.RenderOptions) (*render.RenderResult, error) {
	if err := os.WriteFile(r.path, []byte(content), 0600); err != nil {
		return nil, err
	}
	return &render.RenderResult{Path: r.path, Size: int64(len(content)), ContentType: format.MIME()}, nil
}

func TestDocumentExportToolArtifactDownloadsThroughAuthenticatedRoute(t *testing.T) {
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateSession(ctx, &storage.Session{ID: "document-api-session", UserID: "api-user", Platform: "api", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: "document-api-user", SessionID: "document-api-session", Role: "user", RequestID: "document-api-request", Content: "Export the meeting notes"}); err != nil {
		t.Fatal(err)
	}
	invocation := skill.WithInvocationContext(ctx, skill.InvocationContext{AuthenticatedUserID: "api-user", SessionID: "document-api-session", RequestID: "document-api-request", ToolCallID: "document-api-tool", RoutedAgentName: "default", Platform: "api"})
	renderer := documentHTTPTextRenderer{path: filepath.Join(root, "result.txt")}
	flow := documentflow.NewService(renderer, store.DocumentRepository())
	exporter := builtin.NewExportDocumentSkill(renderer)
	exporter.SetArtifactPublisher(flow)
	const body = "Meeting notes\nThe review starts at 10:00."
	result, err := exporter.Execute(invocation, map[string]any{"content": body, "format": "txt", "filename": "meeting-notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	var refs []documentflow.Reference
	if err := json.Unmarshal([]byte(result.Metadata["artifacts"]), &refs); err != nil || len(refs) != 1 {
		t.Fatalf("persistent artifact reference missing: %v", err)
	}
	if result.Metadata["path"] != "" {
		t.Fatal("local path is not an attachment authority")
	}
	srv := &Server{cfg: &config.Config{}}
	srv.cfg.Server.APIToken = "document-route-fixture-token"
	srv.SetDocumentFlow(flow)
	request := httptest.NewRequest(http.MethodGet, refs[0].URL, nil)
	request.Header.Set("Authorization", "Bearer "+srv.cfg.Server.APIToken)
	recorder := httptest.NewRecorder()
	srv.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != body {
		t.Fatalf("download status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("ETag") != `"`+refs[0].ByteDigest+`"` {
		t.Fatal("download digest differs from the registered artifact")
	}
	if _, err := os.Stat(renderer.path); !os.IsNotExist(err) {
		t.Fatal("download must not depend on a render temp file")
	}
}
