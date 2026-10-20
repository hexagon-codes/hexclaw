package usecase

import (
	"context"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageScopeRuntimeSurvivesColdRestore(t *testing.T) {
	o := newOrchestrator(t, &countingRecognizer{}, nil, nil)
	o.runDir = t.TempDir()
	job := startOrchestratorJob(t, o, "image-scope-persistence")
	run := o.runs[job.Record.RecordID]
	run.req.ImageScope = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, MessageIntent: "只做第1题"}
	run.questions = []RecognizedQuestion{{Question: "visible question", Subject: "语文", ImageSourceContext: &k12.RecognitionImageSourceContextV1{ScopeDigest: "frozen-scope", TargetIDs: []string{"target-1"}, Materials: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1}, Text: "shared source material", Region: k12.SourcePixelRegion{Width: 10, Height: 10}}}, Recoveries: []k12.RecognitionImageSourceRecoveryV1{{Gap: k12.RecognitionImageSourceGapV1{Order: 1, Kind: "instruction", Before: "visible before"}, State: "same_exercise_source_required"}}}}}
	if err := o.persistRun(job.Record.RecordID, run); err != nil {
		t.Fatal(err)
	}
	delete(o.runs, job.Record.RecordID)
	restored, err := o.ensureRun(context.Background(), job.Record.RecordID)
	if err != nil || !reflect.DeepEqual(restored.req.ImageScope, run.req.ImageScope) || !reflect.DeepEqual(restored.questions, run.questions) {
		t.Fatalf("scope/material restoration changed: %+v error=%v", restored, err)
	}
	old := recognizingInvocationDigest(run.req.Image, job.Fields.ModelSnapshot, k12.ModelRequestPolicySnapshot{}, run.req.InitialReadMode)
	scoped := recognizingInvocationDigest(run.req.Image, job.Fields.ModelSnapshot, k12.ModelRequestPolicySnapshot{}, run.req.InitialReadMode, k12.RecognitionImageScopeInputIdentityV1(run.req.ImageScope))
	if old == scoped {
		t.Fatal("scope is absent from recognition identity")
	}
}
