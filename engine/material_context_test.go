package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/memory"
)

func TestMaterialSolveDoesNotInjectAmbientMemoryOrKnowledge(t *testing.T) {
	fm, err := memory.New(memory.Options{Enabled: true, Dir: t.TempDir(), MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	const marker = "MATERIAL_UNRELATED_PARENT_PREFERENCE"
	if err = fm.SaveStructuredEntry(marker, "preference", "manual", "", memory.EntryMeta{Pinned: true}); err != nil {
		t.Fatal(err)
	}
	p := &egressCaptureProvider{}
	e := newEgressGuardedCloudEngine(t, p, fm)
	msg := &adapter.Message{ID: "material-independent", Platform: adapter.PlatformAPI, UserID: "system", Content: "Find the area of a rectangle with sides 7 cm and 6 cm", Metadata: map[string]string{"memory": "off", "knowledge": "off", "provider": "cloud-openai", "model": "gpt-x"}}
	if e.shouldAutoInjectKB(msg) {
		t.Fatal("material solve must not retrieve unrelated documents")
	}
	ctx := egress.WithRequest(context.Background(), egress.PurposeSolveVerify, "material", egress.ClassDocument)
	if _, err = e.Process(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if len(p.messages) != 1 {
		t.Fatalf("model calls=%d", len(p.messages))
	}
	for _, m := range p.messages[0] {
		if strings.Contains(m.Content, marker) {
			t.Fatal("resident memory contaminated independent solve")
		}
	}
	requireNoEgressClass(t, p.last(t), egress.ClassMemory)
	if !errors.Is(friendlyLLMError(egress.ErrDenied), egress.ErrDenied) {
		t.Fatal("friendly error lost definite denial")
	}
}
