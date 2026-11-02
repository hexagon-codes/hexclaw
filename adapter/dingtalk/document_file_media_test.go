package dingtalk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
)

type documentFileMediaAPI struct{ *fakeDingTalkFileMediaAPI }

func (f *documentFileMediaAPI) UploadFile(ctx context.Context, token string, attachment adapter.Attachment) (string, error) {
	return uploadDingtalkDocumentFile(ctx, f.httpClient, f.endpoint, token, attachment)
}

func TestDingTalkExportedDocumentPreparationAndSend(t *testing.T) {
	for _, test := range []struct{ extension, mime string }{
		{"docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"odt", "application/vnd.oasis.opendocument.text"}, {"epub", "application/epub+zip"},
		{"rtf", "application/rtf"}, {"md", "text/markdown"}, {"html", "text/html"}, {"txt", "text/plain"},
	} {
		t.Run(test.extension, func(t *testing.T) {
			raw := []byte("frozen document bytes")
			filename := "document." + test.extension
			uploads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uploads++
				if r.URL.Query().Get("type") != "file" || r.URL.Query().Get("access_token") != "bound-token" {
					t.Error("upload lost bound file protocol")
				}
				file, header, err := r.FormFile("media")
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				defer file.Close()
				body, err := io.ReadAll(file)
				if err != nil || header.Filename != filename || !bytes.Equal(body, raw) {
					t.Error("upload changed frozen file")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "media_id": "@document-file"})
			}))
			defer server.Close()
			api := &documentFileMediaAPI{&fakeDingTalkFileMediaAPI{fakeDingtalkOpenAPI: newFakeDingtalkOpenAPI("bound-token"), httpClient: server.Client(), endpoint: server.URL}}
			a := newTestAdapter()
			a.queue = nil
			a.openAPI = api
			digest := fmt.Sprintf("%x", sha256.Sum256(raw))
			attachment := adapter.Attachment{Type: "file", Name: filename, Mime: test.mime, Data: base64.StdEncoding.EncodeToString(raw)}
			attachment.ID = "document-" + digest
			attachment.ContentDigest = "sha256:" + digest
			attachment.URL = "/api/v1/document-artifacts/" + attachment.ID + "/content"
			attachment.Source = &adapter.AttachmentSource{OpenReader: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }}
			reply := &adapter.Reply{Content: "[Download document](" + attachment.URL + ")", Attachments: []adapter.Attachment{attachment}}
			parts, err := ProjectDeliveryParts(reply)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(reply.MessageContent.Markdown, attachment.URL) || strings.Contains(parts[0].Text, attachment.URL) || !strings.Contains(parts[0].Text, "Download document") {
				t.Fatal("document link projection lost canonical source or exposed internal URL")
			}
			part := parts[1]
			resource, err := a.PrepareDeliveryPartResource(t.Context(), part)
			if err != nil {
				t.Fatal(err)
			}
			if resource != "@document-file" || len(api.SendCalls()) != 0 {
				t.Fatal("preparation sent visible content or lost resource")
			}
			part.PreparedResourceID = resource
			ack, err := a.SendPreparedPartWithReceipt(t.Context(), "bound-user", part)
			if err != nil || ack.Status != adapter.DeliveryAccepted {
				t.Fatalf("send: ack=%+v error=%v", ack, err)
			}
			calls := api.SendCalls()
			if uploads != 1 || len(calls) != 1 || calls[0].MsgKey != "sampleFile" {
				t.Fatalf("uploads=%d calls=%+v", uploads, calls)
			}
			var payload struct {
				MediaID  string `json:"mediaId"`
				FileType string `json:"fileType"`
				FileName string `json:"fileName"`
			}
			if err := json.Unmarshal([]byte(calls[0].MsgParam), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.MediaID != resource || payload.FileType != test.extension || payload.FileName != filename {
				t.Fatalf("payload=%+v", payload)
			}
		})
	}
}
