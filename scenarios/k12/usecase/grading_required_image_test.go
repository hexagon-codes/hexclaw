package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestRequiredAnnotatedImageCreationFreezesOnlyAnsweredImageTask(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		intent       PhotoTaskIntent
		required     bool
	}{
		{"answered image", "image_task", PhotoTaskCompletedHomework, true},
		{"blank image", "image_task", PhotoTaskBlankWorksheet, false},
		{"legacy direct", "desktop", PhotoTaskCompletedHomework, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrchestrator(t, nil, nil, nil)
			req := orchestratorPhotoRequest()
			req.TaskIntent = tc.intent
			req.Image = validPNGFixture(t, "required-creation")
			in := StartPhotoGradingInput{Photo: req, SourceKind: tc.source, SourceKey: "required-creation", ModelSnapshot: k12.GradingModelSnapshot{Provider: "school", Model: "reader", Capability: "vision", TimeoutMS: 30500}}
			if tc.source == "image_task" {
				in.BudgetSnapshot = orchestratorTestBudget()
				in.ParentAutomaticAttemptID = "parent-creation"
				in.ParentAutomaticDeadlineAt = time.Now().Unix() + 300
			}
			job, created, err := o.StartPhotoGradingJob(t.Context(), in)
			if err != nil || !created {
				t.Fatalf("create: %v", err)
			}
			if job.Fields.BudgetSnapshot.RequiresAnnotatedImage() != tc.required {
				t.Fatal("image contract applied to the wrong task")
			}
			want := int64(60)
			if tc.required {
				want = 31
			}
			if job.Fields.BudgetSnapshot.StageSeconds.Locating != want {
				t.Fatal("frozen locating budget ignored route cap or changed legacy budget")
			}
			o.deps.GradingBudgetSnapshot.StageSeconds.Locating = 5
			replay, fresh, err := o.StartPhotoGradingJob(t.Context(), in)
			if err != nil || fresh || !reflect.DeepEqual(job.Fields.BudgetSnapshot, replay.Fields.BudgetSnapshot) {
				t.Fatalf("creation replay changed frozen contract: %v", err)
			}
		})
	}
}

type requiredImageDeadlineAnchorer struct {
	remaining time.Duration
	calls     int
}

func (a *requiredImageDeadlineAnchorer) AnchorAnswers(ctx context.Context, _ []byte, q []RecognizedQuestion) ([]RecognizedQuestion, error) {
	a.calls++
	end, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("deadline missing")
	}
	a.remaining = time.Until(end)
	return cloneRecognizedQuestions(q), nil
}
func (a *requiredImageDeadlineAnchorer) AnchorAnswerGeometry(ctx context.Context, image []byte, q []RecognizedQuestion) ([]RecognizedQuestion, error) {
	return a.AnchorAnswers(ctx, image, q)
}

func TestRequiredAnnotatedImageLocatingClipsRouteAndParentBudget(t *testing.T) {
	for _, parentSeconds := range []int64{300, 10} {
		o := newOrchestrator(t, nil, nil, nil)
		a := &requiredImageDeadlineAnchorer{}
		o.deps.AnswerAnchorer = a
		o.deps.GradingBudgetSnapshot = requiredImageBudgetForTest(t)
		job, created, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{Photo: orchestratorPhotoRequest(), SourceKind: "im", SourceKey: "deadline", ParentAutomaticAttemptID: "parent", ParentAutomaticDeadlineAt: time.Now().Unix() + parentSeconds, ModelSnapshot: k12.GradingModelSnapshot{Provider: "school", Model: "reader", Capability: "vision", TimeoutMS: 30500}})
		if err != nil || !created {
			t.Fatal(err)
		}
		_, state, _, unknown := o.executeAnchor(job.Record.RecordID, job.Record.AgentName, []byte("image"), []RecognizedQuestion{{Question: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerState: AnswerStatePresent}}, job.Fields.ModelSnapshot)
		cap := min(time.Duration(parentSeconds)*time.Second, 30500*time.Millisecond)
		if unknown || state != k12.GradingAnchorLocated || a.calls != 1 || a.remaining <= 0 || a.remaining > cap {
			t.Fatalf("locating deadline or call drift: remaining=%v cap=%v calls=%d", a.remaining, cap, a.calls)
		}
	}
}

func TestRequiredAnnotatedImageAnchorFailureRetainsReceiptsAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "known failure resumes only locating", true: "unknown never resends"}[unknown], func(t *testing.T) {
			d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
			d.GradingBudgetSnapshot = requiredImageBudgetForTest(t)
			job, created, err := d.CreateGradingJob(t.Context(), "mingming", "required-anchor", CreateGradingJobInput{SubmissionID: "required-anchor", SourceKind: "desktop", SourceKey: "required-anchor", ModelSnapshot: orchestratorSnapshot(), MaterializesProblemAttempts: true})
			if err != nil || !created {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				job, err = d.AdvanceGradingStage(t.Context(), job.Record.AgentName, job.Record.RecordID, AdvanceGradingInput{Outcome: GradingOutcomeOK, ArtifactDigest: "successful-source"})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := append([]k12.GradingStageCheckpoint(nil), job.Fields.StageCheckpoints...)
			o := &GradingOrchestrator{deps: d}
			invocation, err := o.beginModelInvocation(t.Context(), job, k12.GradingStageLocating, modelInvocationDigest([]byte("required-anchor")))
			if err != nil {
				t.Fatal(err)
			}
			if unknown {
				_, err = d.Records.MarkModelInvocationOutcomeUnknown(t.Context(), job.Record.AgentName, invocation.InvocationID, "provider_outcome_unknown")
			} else {
				_, err = d.Records.MarkModelInvocationFailed(t.Context(), job.Record.AgentName, invocation.InvocationID, "known_response_failed")
			}
			if err != nil {
				t.Fatal(err)
			}
			failed, err := d.AdvanceGradingStage(t.Context(), job.Record.AgentName, job.Record.RecordID, AdvanceGradingInput{Outcome: GradingOutcomeAnchor, AnchorState: k12.GradingAnchorDegraded, ArtifactDigest: "anchor:failed"})
			if err != nil {
				t.Fatal(err)
			}
			want := k12.GradingStageFailedRetryable
			if unknown {
				want = k12.GradingStageOutcomeUnknown
			}
			if failed.Record.Status != want || failed.Fields.FailedStage != k12.GradingStageLocating || !reflect.DeepEqual(before, failed.Fields.StageCheckpoints) {
				t.Fatal("failed anchor became a success checkpoint or completed task")
			}
			queued, err := d.RetryGradingJob(t.Context(), job.Record.AgentName, job.Record.RecordID)
			if unknown {
				if err == nil {
					t.Fatal("unknown locating was retryable")
				}
				calls, e := d.Records.ListModelInvocations(t.Context(), job.Record.AgentName, job.Record.RecordID)
				if e != nil || len(calls) != 1 || calls[0].Status != k12.ModelInvocationOutcomeUnknown {
					t.Fatal("unknown ledger changed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := d.AdvanceGradingStage(t.Context(), job.Record.AgentName, queued.Record.RecordID, AdvanceGradingInput{Outcome: GradingOutcomeOK})
			if err != nil || resumed.Record.Status != k12.GradingStageAwaitingConfirmation || !reflect.DeepEqual(before, resumed.Fields.StageCheckpoints) {
				t.Fatalf("locating retry repeated a successful source phase: %v", err)
			}
			second, err := o.beginModelInvocation(t.Context(), resumed, k12.GradingStageLocating, modelInvocationDigest([]byte("required-anchor")))
			if err != nil {
				t.Fatal(err)
			}
			if second.Attempt != 2 {
				t.Fatal("known retry did not use exactly one next locating attempt")
			}
			if _, err = d.Records.MarkModelInvocationSucceeded(t.Context(), job.Record.AgentName, second.InvocationID, modelInvocationDigest([]byte("geometry")), ""); err != nil {
				t.Fatal(err)
			}
			joined, err := d.AdvanceGradingStage(t.Context(), job.Record.AgentName, job.Record.RecordID, AdvanceGradingInput{Outcome: GradingOutcomeAnchor, AnchorState: k12.GradingAnchorLocated, ArtifactDigest: "anchor:located"})
			if err != nil || joined.Record.Status != k12.GradingStageAwaitingConfirmation || checkpointStages(joined)[k12.GradingStageRecognizing] != 1 || checkpointStages(joined)[k12.GradingStageLocating] != 1 {
				t.Fatalf("successful retry lost prior source or geometry: %v", err)
			}
		})
	}
}

type requiredImageAnnotator struct{ calls int }

func (a *requiredImageAnnotator) Annotate(context.Context, []byte, []PhotoAnnotation) (RenderedPhoto, error) {
	a.calls++
	if a.calls == 1 {
		return RenderedPhoto{}, errors.New("compositor failed")
	}
	return RenderedPhoto{Data: []byte("controlled-render-result"), MIME: "image/png"}, nil
}

type requiredImageSolverCounter struct {
	Solver
	calls int
}

func (s *requiredImageSolverCounter) Solve(ctx context.Context, p, g, r string) (SolveResult, error) {
	s.calls++
	return s.Solver.Solve(ctx, p, g, r)
}

type requiredImageGraderCounter struct {
	Grader
	calls int
}

func (g *requiredImageGraderCounter) Grade(ctx context.Context, p, s, a string) (GradeOutcome, error) {
	g.calls++
	return g.Grader.Grade(ctx, p, s, a)
}

func TestRequiredAnnotatedImageRenderRetryUsesOnlyDurableAssessments(t *testing.T) {
	rec := &countingRecognizer{questions: []RecognizedQuestion{{Question: "1+1=", Subject: "数学", StudentAnswer: "3"}}}
	anchor := &photoAnchorerFake{boxes: map[int]BBox{0: {X: .2, Y: .3, W: .1, H: .05}}}
	renderer := &requiredImageAnnotator{}
	o := newOrchestrator(t, rec, anchor, renderer)
	o.deps.GradingBudgetSnapshot = requiredImageBudgetForTest(t)
	solver := &requiredImageSolverCounter{Solver: o.deps.Solver}
	grader := &requiredImageGraderCounter{Grader: o.deps.Grader}
	o.deps.Solver, o.deps.Grader = solver, grader
	job := startOrchestratorJob(t, o, "required-render")
	if _, err := o.RunGradingJob(t.Context(), job.Record.RecordID); err != nil {
		t.Fatal(err)
	}
	failed, err := o.ConfirmAndRun(t.Context(), job.Record.RecordID, nil)
	if err == nil || failed.Record.Status != k12.GradingStageFailedRetryable || failed.Fields.FailedStage != k12.GradingStageRendering || renderer.calls != 1 {
		t.Fatalf("render failure degraded to completion: stage=%s err=%v calls=%d", failed.Record.Status, err, renderer.calls)
	}
	beforeSolver, beforeGrader := solver.calls, grader.calls
	resumed, err := o.RetryAndRun(t.Context(), job.Record.RecordID)
	// 隔离夹具没有 ImageTask 原图归属，因此这里只验证渲染恢复，最终资产门禁仍须拒绝。
	if !errors.Is(err, ErrGradingFinalizationIncomplete) || resumed.Record.Status != k12.GradingStageProjecting || renderer.calls != 2 || solver.calls != beforeSolver || grader.calls != beforeGrader || rec.calls != 1 {
		t.Fatalf("retry did not isolate rendering or bypassed source proof: stage=%s err=%v renderer=%d solver=%d/%d grader=%d/%d", resumed.Record.Status, err, renderer.calls, solver.calls, beforeSolver, grader.calls, beforeGrader)
	}
}

func requiredImageBudgetForTest(t *testing.T) k12.GradingBudgetSnapshot {
	t.Helper()
	raw, err := json.Marshal(orchestratorTestBudget())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("invalid budget fixture")
	}
	fields["image_output_contract"] = json.RawMessage(`"annotated_homework_v1"`)
	fields["stage_seconds"] = json.RawMessage(`{"queued_seconds":60,"normalizing_seconds":60,"recognizing_seconds":120,"locating_seconds":120,"rendering_seconds":60,"projecting_seconds":60}`)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot k12.GradingBudgetSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRequiredAnnotatedImageCannotFinalizeMissingPicture(t *testing.T) {
	f := prepareFinalSummaryCrashFixture(t)
	f.job.Fields.BudgetSnapshot = requiredImageBudgetForTest(t)
	if _, err := f.orchestrator.finalizeGradingPage(t.Context(), f.run, f.job); !errors.Is(err, ErrGradingFinalizationIncomplete) {
		t.Fatalf("required image missing but page finalization accepted: %v", err)
	}
	if _, err := f.orchestrator.deps.Records.GetGradingFinalArtifactByJob(t.Context(), f.job.Record.AgentName, f.job.Record.RecordID); err == nil {
		t.Fatal("missing picture published a frozen final artifact")
	}
}
