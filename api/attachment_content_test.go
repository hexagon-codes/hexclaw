package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestAttachmentContentReadsSameOwnedReceiptAndExpires(t *testing.T) {
	srv := NewServer(config.DefaultConfig(), nil, nil, nil)
	srv.SetSidecarCapabilityToken("native-sidecar-token-012345678901")
	t.Cleanup(func() { _ = srv.attachmentStaging.Close() })
	payload := []byte("\x89PNG\r\n\x1a\nimage-content")
	h := srv.routes()
	upload := httptest.NewRecorder()
	h.ServeHTTP(upload, stagedAttachmentRequest(t, "/api/v1/attachments", "content-read", "content.png", "image/png", payload))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", upload.Code, upload.Body.String())
	}
	var receipt AttachmentReceipt
	if err := json.Unmarshal(upload.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	read := func(id, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/attachments/"+id+"/content", nil)
		req.RemoteAddr = "127.0.0.1:42001"
		req.Header.Set("Authorization", "Bearer native-sidecar-token-012345678901")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	first := read(receipt.AttachmentID, "")
	if first.Code != 200 || first.Header().Get("Content-Type") != "image/png" || !bytes.Equal(first.Body.Bytes(), payload) {
		t.Fatalf("read: %d %s", first.Code, first.Body.String())
	}
	if next := read(receipt.AttachmentID, first.Header().Get("ETag")); next.Code != 304 || next.Body.Len() != 0 {
		t.Fatalf("conditional read: %d", next.Code)
	}
	if _, err := srv.attachmentStaging.Resolve(defaultDesktopUserID, receipt.AttachmentID); err != nil {
		t.Fatalf("preview consumed model receipt: %v", err)
	}
	other, _, err := srv.attachmentStaging.Stage(context.Background(), "another-owner", "other-content", "other.png", "image/png", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(other.AttachmentID, ""); got.Code != 404 {
		t.Fatalf("cross-owner read: %d", got.Code)
	}
	srv.attachmentStaging.now = func() time.Time { return receipt.ExpiresAt.Add(time.Second) }
	if got := read(receipt.AttachmentID, ""); got.Code != 404 {
		t.Fatalf("expired read: %d", got.Code)
	}
}

func TestMessageImageSurvivesReceiptExpiryAndServerRecreation(t *testing.T) {
	store := newTestStoreForAPI(t)
	ctx := context.Background()
	for _, owner := range []string{defaultDesktopUserID, "another-owner"} {
		if err := store.CreateSession(ctx, &storage.Session{ID: owner, UserID: owner, Platform: "web"}); err != nil {
			t.Fatal(err)
		}
	}
	server := func() *Server {
		srv := NewServer(config.DefaultConfig(), nil, nil, store)
		srv.SetSidecarCapabilityToken("native-sidecar-token-012345678901")
		t.Cleanup(func() { _ = srv.attachmentStaging.Close() })
		return srv
	}
	srv := server()
	payload := []byte("\x89PNG\r\n\x1a\nowned-original-image")
	receipt, _, err := srv.attachmentStaging.Stage(ctx, defaultDesktopUserID, "saved-image", "original.png", "image/png", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	read := func(srv *Server, session, message, index, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sessions/"+session+"/messages/"+message+"/attachments/"+index+"/content?pending="+receipt.AttachmentID, nil)
		req.RemoteAddr = "127.0.0.1:42001"
		req.Header.Set("Authorization", "Bearer native-sidecar-token-012345678901")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		res := httptest.NewRecorder()
		srv.routes().ServeHTTP(res, req)
		return res
	}
	before := read(srv, defaultDesktopUserID, "saved-message", "0", "")
	if before.Code != 200 || !bytes.Equal(before.Body.Bytes(), payload) {
		t.Fatalf("pending image: HTTP %d", before.Code)
	}
	metadata, err := json.Marshal(map[string]any{"attachments": []adapter.Attachment{{Type: "image", Name: "original.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(payload)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: "saved-message", SessionID: defaultDesktopUserID, Role: "user", Attachments: string(metadata), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	srv.attachmentStaging.now = func() time.Time { return receipt.ExpiresAt.Add(time.Second) }
	if _, err := srv.attachmentStaging.Resolve(defaultDesktopUserID, receipt.AttachmentID); err == nil {
		t.Fatal("fixture upload receipt did not expire")
	}
	for _, current := range []*Server{srv, server()} {
		got := read(current, defaultDesktopUserID, "saved-message", "0", "")
		if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), payload) {
			t.Fatalf("saved image: HTTP %d %s", got.Code, got.Body.String())
		}
		if got.Header().Get("ETag") != before.Header().Get("ETag") {
			t.Fatal("persisted image differs from uploaded source")
		}
		if got := read(current, defaultDesktopUserID, "saved-message", "0", before.Header().Get("ETag")); got.Code != 304 {
			t.Fatalf("conditional image: %d", got.Code)
		}
		for _, query := range [][3]string{{"another-owner", "saved-message", "0"}, {defaultDesktopUserID, "saved-message", "1"}, {defaultDesktopUserID, "saved-message", "-1"}} {
			if got := read(current, query[0], query[1], query[2], ""); got.Code != 404 {
				t.Fatalf("invalid ownership/index %v: %d", query, got.Code)
			}
		}
	}
	if err := store.CreateSession(ctx, &storage.Session{ID: "other-owned-session", UserID: defaultDesktopUserID, Platform: "web"}); err != nil {
		t.Fatal(err)
	}
	if got := read(srv, "other-owned-session", "saved-message", "0", ""); got.Code != 404 {
		t.Fatalf("wrong owned session: %d", got.Code)
	}
}

func TestMessageImageURLHistoryKeepsLargeOriginalOutOfJSON(t *testing.T) {
	store := newTestStoreForAPI(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, &storage.Session{ID: "large-image-session", UserID: defaultDesktopUserID, Platform: "web"}); err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 3000, 3000))); err != nil {
		t.Fatal(err)
	}
	if original.Len() <= 32<<20 {
		t.Fatal("fixture must exceed the ordinary JSON IPC boundary")
	}
	metadata, err := json.Marshal(map[string]any{"attachments": []adapter.Attachment{{Type: "image", Name: "large.png", Mime: "image/png", Data: base64.StdEncoding.EncodeToString(original.Bytes())}}})
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := store.SaveMessage(ctx, &storage.MessageRecord{ID: "large-image", SessionID: "large-image-session", Role: "user", Content: "large image reference", Attachments: string(metadata), CreatedAt: time.Now()}); saveErr != nil {
		t.Fatal(saveErr)
	}
	srv := NewServer(config.DefaultConfig(), nil, nil, store)
	srv.SetSidecarCapabilityToken("native-sidecar-token-012345678901")
	t.Cleanup(func() { _ = srv.attachmentStaging.Close() })
	read := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.RemoteAddr = "127.0.0.1:42001"
		req.Header.Set("Authorization", "Bearer native-sidecar-token-012345678901")
		res := httptest.NewRecorder()
		srv.routes().ServeHTTP(res, req)
		return res
	}
	history := read("/api/v1/sessions/large-image-session/messages?attachment_mode=url")
	if history.Code != 200 || history.Body.Len() > 4096 {
		t.Fatalf("image bytes leaked into history JSON: HTTP %d, bytes=%d", history.Code, history.Body.Len())
	}
	var page struct {
		Messages []storage.MessageRecord `json:"messages"`
	}
	if decodeErr := json.Unmarshal(history.Body.Bytes(), &page); decodeErr != nil || len(page.Messages) != 1 {
		t.Fatalf("history decode: %v", decodeErr)
	}
	var projected struct {
		Attachments []adapter.Attachment `json:"attachments"`
	}
	if decodeErr := json.Unmarshal([]byte(page.Messages[0].Metadata), &projected); decodeErr != nil || len(projected.Attachments) != 1 {
		t.Fatalf("attachment projection: %v; response=%s", decodeErr, history.Body.String())
	}
	if projected.Attachments[0].Data != "" {
		t.Fatal("history still contains inline image bytes")
	}
	content := read(projected.Attachments[0].URL)
	if content.Code != 200 || !bytes.Equal(content.Body.Bytes(), original.Bytes()) {
		t.Fatalf("large original changed: HTTP %d", content.Code)
	}
	record, err := store.GetMessage(ctx, "large-image")
	if err != nil || record.Attachments != string(metadata) {
		t.Fatal("history projection changed stored image")
	}
}
