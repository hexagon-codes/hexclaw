package engine

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
)

func TestStandaloneVerificationPreservesOnlyCurrentMessageSourceImages(t *testing.T) {
	for _, withImage := range []bool{true, false} {
		t.Run(map[bool]string{true: "source_image", false: "text_only"}[withImage], func(t *testing.T) {
			provider := &standaloneVerifierTestProvider{response: "not a verification program"}
			eng := newEngineWithProvider(t, provider)
			last := llm.Message{Role: llm.RoleUser, Content: "original solve prompt"}
			const source = "data:image/png;base64,iVBORw0KGgo="
			if withImage {
				last.MultiContent = []llm.ContentPart{{Type: "text", Text: last.Content},
					{Type: "image_url", ImageURL: &llm.ImageURL{URL: source}}}
			}
			req := hexagon.CompletionRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "independent verification"}, last}}
			_, err := eng.completeStandaloneVerification(t.Context(), "source-verification", &adapter.Message{},
				provider, provider.Name(), "fixture", req, "", &verificationInput{Problem: "求涂色面积（ ）cm²。", Candidate: "15.7cm²"})
			if err == nil {
				t.Fatal("controlled invalid program was unexpectedly accepted")
			}
			if len(provider.requests) != 1 {
				t.Fatalf("provider calls=%d", len(provider.requests))
			}
			got := provider.requests[0].Messages[len(req.Messages)-1]
			if !strings.Contains(got.Content, "15.7cm²") || got.Content == last.Content {
				t.Fatal("independent verification did not replace its task text")
			}
			images := 0
			for _, part := range got.MultiContent {
				if part.Type == "image_url" && part.ImageURL != nil {
					images++
					if part.ImageURL.URL != source {
						t.Fatal("verification source image changed")
					}
				}
				if part.Type == "text" && part.Text != got.Content {
					t.Fatal("multimodal text retained the old solver prompt")
				}
			}
			want := 0
			if withImage {
				want = 1
			}
			if images != want {
				t.Fatalf("current source images=%d want=%d", images, want)
			}
			if req.Messages[len(req.Messages)-1].Content != "original solve prompt" {
				t.Fatal("verification rewrote the caller's message")
			}
		})
	}
}
