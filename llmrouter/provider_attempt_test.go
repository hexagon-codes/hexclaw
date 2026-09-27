package llmrouter

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
)

func TestProviderAttemptDistinguishesInitialDenialFromLaterDenial(t *testing.T) {
	for _, prior := range []string{"none", "cloud", "local"} {
		t.Run(prior, func(t *testing.T) {
			ctx, attempt := egress.WithProviderAttempt(context.Background())
			p := &egressCaptureProvider{}
			cloud := newEgressSelector("https://llm.example/v1", p).Default()
			if prior != "none" {
				provider := cloud
				if prior == "local" {
					provider = newEgressSelectorWithLocality("http://127.0.0.1:11434/v1", config.ProviderLocalityLocal, p).Default()
				}
				allowed := egress.WithRequest(ctx, egress.PurposeSolveVerify, "", egress.ClassDocument)
				if _, err := provider.Complete(allowed, hexagon.CompletionRequest{}); err != nil {
					t.Fatal(err)
				}
			}
			denied := egress.WithRequest(ctx, egress.PurposeSolveVerify, "", egress.ClassMemory)
			_, err := cloud.Complete(denied, hexagon.CompletionRequest{})
			err = attempt.Reconcile(err)
			if !errors.Is(err, egress.ErrDenied) || errors.Is(err, egress.ErrProviderNotSent) != (prior == "none") {
				t.Fatalf("wrong send evidence: %v", err)
			}
			want := 1
			if prior == "none" {
				want = 0
			}
			if p.completeCalls != want {
				t.Fatalf("actual calls=%d want=%d", p.completeCalls, want)
			}
			if errors.Is(attempt.Reconcile(context.DeadlineExceeded), egress.ErrProviderNotSent) {
				t.Fatal("deadline without a definitive denial cannot prove not sent")
			}
		})
	}
}
