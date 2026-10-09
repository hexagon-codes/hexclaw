package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
)

func TestK12PDFUploadClassifiesKnownAndUnknownWithoutRepeating(t *testing.T) {
	for _, mode := range []string{"400", "429", "503", "reset", "truncated", "invalid_json", "provider_rejection"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "400":
					w.WriteHeader(400)
				case "429":
					w.WriteHeader(429)
				case "503":
					w.WriteHeader(503)
				case "reset":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					_ = conn.Close()
				case "truncated":
					w.Header().Set("Content-Length", "200")
					_, _ = w.Write([]byte(`{"errcode":0`))
				case "invalid_json":
					_, _ = w.Write([]byte("invalid private-token-secret"))
				case "provider_rejection":
					_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40001, "errmsg": "private-token-secret https://private.example.invalid/key"})
				}
			}))
			defer server.Close()
			api := &fakeDingTalkFileMediaAPI{fakeDingtalkOpenAPI: newFakeDingtalkOpenAPI("private-token-secret"), httpClient: server.Client(), endpoint: server.URL + "/upload"}
			a := newTestAdapter()
			a.queue = nil
			a.openAPI = api
			part := canonicalDingTalkDeliveryPartsForTest(t, "完整讲解", validPDFReplyAttachment())[1]
			_, err := a.PrepareDeliveryPartResource(context.Background(), part)
			var outcome *adapter.ResourcePreparationError
			if !errors.As(err, &outcome) {
				t.Fatalf("missing typed preparation outcome: %v", err)
			}
			wantKnown := mode == "400" || mode == "429" || mode == "provider_rejection"
			if outcome.Known != wantKnown || calls != 1 || len(api.SendCalls()) != 0 {
				t.Fatalf("known=%v want=%v physical_calls=%d visible_sends=%d", outcome.Known, wantKnown, calls, len(api.SendCalls()))
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				if strings.Contains(e.Error(), "private-token-secret") || strings.Contains(e.Error(), server.URL) || strings.Contains(e.Error(), "private.example") {
					t.Fatal("preparation error leaked request or response secrets")
				}
			}
		})
	}
}
