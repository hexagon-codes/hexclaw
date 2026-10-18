package usecase

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestRecognitionExecutionPolicySurvivesRestartReplayAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  string
		unknown bool
	}{
		{"current_final_answer", k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2, false},
		{"old_final_answer", k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV1, false},
		{"current_final_answer_unknown", k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2, true},
		{"new", k12.RecognitionExecutionPolicyCompactDAGV1, false},
		{"legacy", "", false},
		{"new_unknown", k12.RecognitionExecutionPolicyCompactDAGV1, true},
		{"legacy_unknown", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			deps, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
			deps.Now = func() int64 { return 1_800_000_000 }
			deps.GradingBudgetSnapshot = recognitionLayoutInitialV2Budget()
			probe := &recognitionLayoutInitialV2Probe{records: deps.Records}
			deps.Recognizer = probe
			route := k12.GradingModelSnapshot{Provider: "cloud-gpt", Model: "gpt-6.1-sol", Route: "cloud-gpt/gpt-6.1-sol", Capability: "vision"}
			resolve := func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return route, nil }
			dir := t.TempDir()
			o := trackGradingOrchestrator(t, NewGradingOrchestrator(deps, resolve, WithGradingRunDir(dir)))
			in := StartPhotoGradingInput{
				Photo:      PhotoGradeRequest{AgentName: "mingming", Grade: "六年级上", SourceSession: "execution-policy", Image: recognitionLayoutInitialV2PagePNG(t), RecognitionExecutionPolicy: "caller-policy"},
				SourceKind: "desktop", SourceKey: "execution-policy",
			}
			job, created, err := o.StartPhotoGradingJob(ctx, in)
			if err != nil || !created {
				t.Fatalf("create new task: created=%v err=%v", created, err)
			}
			run := o.lookup(job.Record.RecordID)
			if run.req.RecognitionExecutionPolicy != k12.RecognitionExecutionPolicyCompactDAGFinalAnswerV2 {
				t.Fatal("new task did not freeze the trusted execution policy")
			}
			// 旧文件缺少该字段，恢复时不得按当前发布策略补写。
			run.req.RecognitionExecutionPolicy = tc.policy
			if err := o.persistRun(job.Record.RecordID, run); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(o.runPath(job.Record.RecordID, "run.json"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.policy == "" && bytes.Contains(before, []byte(`"recognition_execution_policy"`)) {
				t.Fatal("empty policy changed the legacy runtime file")
			}
			for _, advance := range []AdvanceGradingInput{{Outcome: GradingOutcomeOK}, {Outcome: GradingOutcomeOK, ArtifactDigest: "normalized:execution-policy"}} {
				job, err = deps.AdvanceGradingStage(ctx, job.Record.AgentName, job.Record.RecordID, advance)
				if err != nil {
					t.Fatal(err)
				}
			}
			contract := initialRecognitionLayoutContractV2{InitialReadMode: run.req.InitialReadMode, ExecutionPolicy: run.req.RecognitionExecutionPolicy}
			parent, err := o.beginRecognizingModelInvocationWithPolicy(ctx, job, run.req.Image, recognizingInvocationDigest(run.req.Image, route, route.RecognizingRequestPolicy, run.req.InitialReadMode), route.RecognizingRequestPolicy, contract)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, parent.AgentName, parent.InvocationID)
			if err != nil || runtime.Header.ExecutionPolicy != tc.policy {
				t.Fatalf("frozen header policy=%q err=%v", runtime.Header.ExecutionPolicy, err)
			}
			if tc.unknown {
				if _, claimed, err := deps.Records.ClaimModelPhysicalInvocationSent(ctx, parent.AgentName, runtime.ManifestPhysicalInvocationID); err != nil || !claimed {
					t.Fatalf("claim fixture request: claimed=%v err=%v", claimed, err)
				}
				if _, err := deps.Records.MarkModelPhysicalInvocationOutcomeUnknown(ctx, parent.AgentName, runtime.ManifestPhysicalInvocationID, "fixture_unknown"); err != nil {
					t.Fatal(err)
				}
				parent, err = deps.Records.MarkModelInvocationOutcomeUnknown(ctx, parent.AgentName, parent.InvocationID, "fixture_unknown")
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := o.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(deps, resolve, WithGradingRunDir(dir)))
			in.Photo.RecognitionExecutionPolicy = "different-caller-policy"
			replay, created, err := restarted.StartPhotoGradingJob(ctx, in)
			if err != nil || created || replay.Record.RecordID != job.Record.RecordID {
				t.Fatalf("replay created=%v err=%v", created, err)
			}
			restored := restarted.lookup(job.Record.RecordID)
			if restored.req.RecognitionExecutionPolicy != tc.policy {
				t.Fatalf("replay changed frozen policy to %q", restored.req.RecognitionExecutionPolicy)
			}
			after, err := os.ReadFile(restarted.runPath(job.Record.RecordID, "run.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("replay changed runtime bytes: %v", err)
			}
			loaded, err := restarted.loadInitialRecognitionLayoutRuntimeV2(ctx, job, parent, restored.req.Image, contract)
			if err != nil || loaded.HeaderDigest != runtime.HeaderDigest {
				t.Fatalf("replay changed frozen header: %v", err)
			}
			drift := contract
			if tc.policy == "" {
				drift.ExecutionPolicy = k12.RecognitionExecutionPolicyCompactDAGV1
			} else {
				drift.ExecutionPolicy = ""
			}
			if _, err := restarted.loadInitialRecognitionLayoutRuntimeV2(ctx, job, parent, restored.req.Image, drift); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
				t.Fatalf("header policy drift was not rejected: %v", err)
			}
			if tc.unknown {
				if _, err := restarted.RunGradingJob(ctx, job.Record.RecordID); err == nil {
					t.Fatal("unknown invocation unexpectedly resumed")
				}
				child, err := deps.Records.GetModelPhysicalInvocation(ctx, parent.AgentName, runtime.ManifestPhysicalInvocationID)
				if err != nil || child.Status != k12.ModelInvocationOutcomeUnknown {
					t.Fatalf("unknown physical call changed: status=%s err=%v", child.Status, err)
				}
			}
			current, err := deps.Records.GetModelInvocation(ctx, parent.AgentName, parent.InvocationID)
			if err != nil || current.Status != parent.Status || probe.calls != 0 {
				t.Fatalf("replay changed invocation or called provider: status=%s calls=%d err=%v", current.Status, probe.calls, err)
			}
		})
	}
}
