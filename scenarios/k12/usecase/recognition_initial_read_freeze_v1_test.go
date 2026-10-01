package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestRecognitionInitialReadModeFrozenBeforeSendAndReload(t *testing.T) {
	for _, tc := range []struct {
		name       string
		callerMode string
		dense      bool
		wantMode   string
	}{
		{"new_dense_default", "", true, k12.RecognitionLayoutManifestWithContentV1},
		{"new_dense_caller_cannot_select_legacy", "legacy", true, k12.RecognitionLayoutManifestWithContentV1},
		{"new_dense_caller_cannot_select_unknown", "caller-selected-mode", true, k12.RecognitionLayoutManifestWithContentV1},
		{"new_v1_caller_cannot_select_content", k12.RecognitionLayoutManifestWithContentV1, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			deps, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
			deps.Now = func() int64 { return 1_800_000_000 }
			deps.GradingBudgetSnapshot = recognitionLayoutInitialV2Budget()
			page := recognitionLayoutInitialV2PagePNG(t)
			if !tc.dense {
				page = recognitionPhysicalExecutorV2PagePNG(t, 40, 20)
			}
			route := k12.GradingModelSnapshot{
				Provider: "hexclaw-gpt", Model: "gpt-5.6-luna",
				Route: "hexclaw-gpt/gpt-5.6-luna", Capability: "vision",
			}
			runDir := t.TempDir()
			orchestrator := trackGradingOrchestrator(t, NewGradingOrchestrator(
				deps,
				func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return route, nil },
				WithGradingRunDir(runDir),
			))
			in := StartPhotoGradingInput{
				Photo: PhotoGradeRequest{
					AgentName: "mingming", Grade: "五年级上",
					SourceSession: "initial-read-freeze", Image: page,
					InitialReadMode: tc.callerMode,
				},
				SourceKind: "desktop", SourceKey: tc.name,
			}
			job, created, err := orchestrator.StartPhotoGradingJob(ctx, in)
			if err != nil || !created {
				t.Fatalf("create: created=%v err=%v", created, err)
			}
			before := assertRecognitionInitialReadRunMode(t, orchestrator, job.Record.RecordID, tc.wantMode)
			parents, err := deps.Records.ListModelInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
			if err != nil || len(parents) != 0 {
				t.Fatalf("mode must be persisted before model preparation: parents=%d err=%v", len(parents), err)
			}
			probe := &recognitionLayoutInitialV2Probe{
				records: deps.Records, agentName: job.Record.AgentName, jobID: job.Record.RecordID,
			}
			deps.Recognizer = probe
			reloaded := trackGradingOrchestrator(t, NewGradingOrchestrator(
				deps,
				func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
					t.Fatal("idempotent reload must not resolve a replacement route")
					return route, nil
				},
				WithGradingRunDir(runDir),
			))
			in.Photo.InitialReadMode = k12.RecognitionLayoutManifestWithContentV1
			if tc.wantMode != "" {
				in.Photo.InitialReadMode = "legacy"
			}
			replayed, created, err := reloaded.StartPhotoGradingJob(ctx, in)
			if err != nil || created || replayed.Record.RecordID != job.Record.RecordID {
				t.Fatalf("reload: created=%v job=%q err=%v", created, replayed.Record.RecordID, err)
			}
			after := assertRecognitionInitialReadRunMode(t, reloaded, job.Record.RecordID, tc.wantMode)
			if !bytes.Equal(before, after) || reloaded.lookup(job.Record.RecordID).req.InitialReadMode != tc.wantMode {
				t.Fatal("caller changed the frozen run contract during reload")
			}
			if tc.wantMode == "" {
				if replayed.Fields.BudgetSnapshot.RecognitionPlanVersion != k12.RecognitionPlanVersionV1 {
					t.Fatal("small-page fixture did not preserve V1 recognition")
				}
				return
			}
			_, err = reloaded.RunGradingJob(ctx, job.Record.RecordID)
			if !errors.Is(err, errRecognitionLayoutInitialV2ProbeComplete) || probe.entryErr != nil {
				t.Fatalf("entry probe: err=%v entry=%v", err, probe.entryErr)
			}
			if probe.calls != 1 || probe.initialReadMode != tc.wantMode || probe.runtime.Header.InitialReadMode != tc.wantMode ||
				probe.parent.RequestDigest == recognizingInvocationDigest(page, probe.parent.RouteSnapshot, probe.parent.RequestPolicySnapshot) ||
				probe.parent.RequestDigest != recognizingInvocationDigest(page, probe.parent.RouteSnapshot, probe.parent.RequestPolicySnapshot, tc.wantMode) ||
				probe.runtime.Header.ParentRequestDigest != probe.parent.RequestDigest ||
				probe.manifest.PlanDigest != probe.runtime.HeaderDigest || probe.manifest.CandidateExactSetDigest != "" {
				t.Fatal("provider entry did not preserve the frozen mode, parent, and initial manifest bindings")
			}
		})
	}
}

func TestRecognitionInitialReadModeLegacyRunCannotBeUpgradedByCaller(t *testing.T) {
	ctx := context.Background()
	deps, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
	deps.Now = func() int64 { return 1_800_000_000 }
	deps.GradingBudgetSnapshot = recognitionLayoutInitialV2Budget()
	page := recognitionLayoutInitialV2PagePNG(t)
	route := k12.GradingModelSnapshot{
		Provider: "hexclaw-gpt", Model: "gpt-5.6-luna",
		Route: "hexclaw-gpt/gpt-5.6-luna", Capability: "vision",
	}
	runDir := t.TempDir()
	orchestrator := trackGradingOrchestrator(t, NewGradingOrchestrator(
		deps, func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return route, nil },
		WithGradingRunDir(runDir),
	))
	in := StartPhotoGradingInput{
		Photo:      PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", SourceSession: "legacy-initial-read", Image: page},
		SourceKind: "desktop", SourceKey: "legacy-initial-read",
	}
	job, created, err := orchestrator.StartPhotoGradingJob(ctx, in)
	if err != nil || !created {
		t.Fatalf("create historical fixture: created=%v err=%v", created, err)
	}
	// 历史版本的 run.json 没有首读模式字段，V2 预算不应使恢复升级协议。
	orchestrator.lookup(job.Record.RecordID).req.InitialReadMode = ""
	if err := orchestrator.persistRun(job.Record.RecordID, orchestrator.lookup(job.Record.RecordID)); err != nil {
		t.Fatal(err)
	}
	before := assertRecognitionInitialReadRunMode(t, orchestrator, job.Record.RecordID, "")
	probe := &recognitionLayoutInitialV2Probe{records: deps.Records, agentName: job.Record.AgentName, jobID: job.Record.RecordID}
	deps.Recognizer = probe
	reloaded := trackGradingOrchestrator(t, NewGradingOrchestrator(deps, nil, WithGradingRunDir(runDir)))
	in.Photo.InitialReadMode = k12.RecognitionLayoutManifestWithContentV1
	replayed, created, err := reloaded.StartPhotoGradingJob(ctx, in)
	if err != nil || created || replayed.Record.RecordID != job.Record.RecordID {
		t.Fatalf("legacy reload: created=%v err=%v", created, err)
	}
	if after := assertRecognitionInitialReadRunMode(t, reloaded, job.Record.RecordID, ""); !bytes.Equal(before, after) {
		t.Fatal("caller rewrote the legacy run metadata")
	}
	_, err = reloaded.RunGradingJob(ctx, job.Record.RecordID)
	if !errors.Is(err, errRecognitionLayoutInitialV2ProbeComplete) || probe.entryErr != nil {
		t.Fatalf("legacy entry probe: err=%v entry=%v", err, probe.entryErr)
	}
	if probe.calls != 1 || probe.initialReadMode != "" || probe.runtime.Header.InitialReadMode != "" ||
		probe.parent.RequestDigest != recognizingInvocationDigest(page, probe.parent.RouteSnapshot, probe.parent.RequestPolicySnapshot) {
		t.Fatalf("legacy protocol calls=%d context_mode=%q header_mode=%q parent_digest=%q want_digest=%q",
			probe.calls, probe.initialReadMode, probe.runtime.Header.InitialReadMode, probe.parent.RequestDigest,
			recognizingInvocationDigest(page, probe.parent.RouteSnapshot, probe.parent.RequestPolicySnapshot))
	}
	headerJSON, err := json.Marshal(probe.runtime.Header)
	if err != nil || bytes.Contains(headerJSON, []byte(`"initial_read_mode"`)) {
		t.Fatalf("legacy header gained a new canonical field: err=%v", err)
	}
}

func assertRecognitionInitialReadRunMode(t *testing.T, o *GradingOrchestrator, jobID, want string) []byte {
	t.Helper()
	raw, err := os.ReadFile(o.runPath(jobID, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	modeJSON, present := meta["initial_read_mode"]
	if want == "" {
		if present {
			t.Fatal("legacy run gained an initial-read mode field")
		}
	} else {
		var got string
		if !present || json.Unmarshal(modeJSON, &got) != nil || got != want {
			t.Fatalf("persisted initial-read mode=%q want=%q", got, want)
		}
	}
	return raw
}
