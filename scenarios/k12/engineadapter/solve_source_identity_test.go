package engineadapter

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

type solveSourceIdentityRecorder struct {
	specs []usecase.GradingPhysicalCallSpec
}

func (p *solveSourceIdentityRecorder) ExecuteGradingPhysicalCall(_ context.Context, spec usecase.GradingPhysicalCallSpec, _ func(context.Context) (string, error)) (usecase.GradingPhysicalCallResult, error) {
	p.specs = append(p.specs, spec)
	return usecase.GradingPhysicalCallResult{}, usecase.ErrModelInvocationRequiresReconciliation
}

func TestSolveAdapterVisualRequestIdentityKeepsLegacyTextDigest(t *testing.T) {
	request := func(image *skill.InputImage) usecase.GradingPhysicalCallSpec {
		t.Helper()
		recorder := &solveSourceIdentityRecorder{}
		ctx := usecase.WithGradingPhysicalCallExecutor(t.Context(), recorder)
		ctx = k12.WithGradingModelSnapshot(ctx, k12.GradingModelSnapshot{Provider: "test", Model: "test"})
		if image != nil {
			ctx = skill.WithInputImages(ctx, []skill.InputImage{*image})
		}
		solver := engine.NewSolveSkill(func(context.Context, engine.SubAgentSpec) (engine.SubAgentResult, error) {
			t.Fatal("identity inspection must not send a model request")
			return engine.SubAgentResult{}, nil
		}, engine.NewSubAgentRegistry(""))
		_, err := NewSolveAdapter(solver).Solve(ctx, "根据图①和图②的圆求涂色面积，并说明依据。", "", "")
		if !errors.Is(err, usecase.ErrModelInvocationRequiresReconciliation) || len(recorder.specs) != 1 {
			t.Fatalf("request identity was not inspected once: calls=%d err=%v", len(recorder.specs), err)
		}
		return recorder.specs[0]
	}
	plain := request(nil)
	image := skill.InputImage{ID: "page#region=0,0,5,5&scope=frozen", MIME: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("diagram-a"))}
	visual := request(&image)
	t.Logf("text request digest=%s, visual legacy digest=%s", plain.RequestDigest, visual.LegacyRequestDigest)
	if plain.LegacyRequestDigest != "" || visual.LegacyRequestDigest != plain.RequestDigest || visual.RequestDigest == plain.RequestDigest {
		t.Fatalf("visual request replaced the immutable text identity: plain=%+v visual=%+v", plain, visual)
	}
	if repeat := request(&image); repeat.RequestDigest != visual.RequestDigest {
		t.Fatal("same source image has unstable identity")
	}
	image.Data = base64.StdEncoding.EncodeToString([]byte("diagram-b"))
	if changed := request(&image); changed.RequestDigest == visual.RequestDigest || changed.LegacyRequestDigest != plain.RequestDigest {
		t.Fatal("changed image pixels reused the old physical identity")
	}
	image.Data = base64.StdEncoding.EncodeToString([]byte("diagram-a"))
	image.ID = "page#region=0,0,6,5&scope=frozen"
	if changed := request(&image); changed.RequestDigest == visual.RequestDigest {
		t.Fatal("changed crop scope reused the old physical identity")
	}
}
