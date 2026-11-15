package main

import (
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestK12SolveSourceAttachmentsStayInCurrentGradingCall(t *testing.T) {
	image := skill.InputImage{ID: "source#region=0,0,5,5&scope=current", Name: "source.png", MIME: "image/png", Data: "cGl4ZWxz"}
	plain := skill.WithInputImages(t.Context(), []skill.InputImage{image})
	ctx := k12.WithGradingModelSnapshot(plain, k12.GradingModelSnapshot{Provider: "test", Model: "test"})
	want := []adapter.Attachment{{Name: image.Name, Type: "image", Mime: image.MIME, Data: image.Data}}
	for _, agent := range []string{"solver", "verifier"} {
		got := k12SolveSourceAttachments(ctx, engine.SubAgentSpec{Agent: agent, Source: "solve"})
		if err := adapter.ValidateAttachments(got); err != nil {
			t.Fatalf("%s source image cannot enter the shared engine: %v", agent, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s lost current source image: %+v", agent, got)
		}
	}
	for _, spec := range []engine.SubAgentSpec{{Agent: "solver", Source: "spawn"}, {Agent: "grader", Source: "solve"}, {Agent: "verifier", Source: "solve", ProgramOnly: true}} {
		if got := k12SolveSourceAttachments(ctx, spec); len(got) != 0 {
			t.Fatalf("unrelated subagent inherited image: %+v", spec)
		}
	}
	if got := k12SolveSourceAttachments(plain, engine.SubAgentSpec{Agent: "solver", Source: "solve"}); len(got) != 0 {
		t.Fatal("ordinary solve inherited grading images")
	}
}
