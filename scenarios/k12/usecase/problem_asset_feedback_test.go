package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type feedbackReassessmentSolver struct {
	answer    string
	calls     int
	snapshots []k12.GradingModelSnapshot
	untrusted bool
}

func (s *feedbackReassessmentSolver) UsesGradingPhysicalCalls() bool { return !s.untrusted }

func (s *feedbackReassessmentSolver) Solve(ctx context.Context, _ string, _ string, _ string) (SolveResult, error) {
	s.calls++
	snapshot, _ := k12.GradingModelSnapshotFromContext(ctx)
	s.snapshots = append(s.snapshots, snapshot)
	if s.untrusted {
		return SolveResult{Solution: s.answer, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNone}}, nil
	}
	return SolveResult{Solution: s.answer, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, nil
}

// 历史反馈必须从已完成、运行文件已释放的真实 SQLite 任务恢复，未知不重发。
func TestProblemAssetFeedbackHistoricalRecovery(t *testing.T) {
	for _, failure := range []string{"", "unknown", "definitive", "blank_untrusted"} {
		t.Run(map[string]string{"": "completed", "unknown": "unknown", "definitive": "failed", "blank_untrusted": "blank_untrusted"}[failure], func(t *testing.T) {
			ctx := context.Background()
			solver, grader := &feedbackReassessmentSolver{answer: "5"}, &assetReuseGrader{}
			o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
			o.deps.Solver, o.deps.Grader, o.deps.VerifiedGrader = solver, grader, nil
			o.deps.TextbookOwnerID = DefaultLocalOwnerScope
			o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", StudentAnswer: "5", AnswerState: AnswerStatePresent}}}
			if failure == "blank_untrusted" {
				o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", AnswerState: AnswerStateBlank}}}
			}
			publicationDispatcher := k12storage.NewDispatcher(o.deps.Records, ProblemAssetConsumer{Records: o.deps.Records})
			seed := runItemResumeJobToAssessing(t, o, "feedback-seed")
			if _, err := o.ConfirmAndRun(ctx, seed, nil); err != nil {
				t.Fatal(err)
			}
			if err := publicationDispatcher.ProcessPending(ctx); err != nil {
				t.Fatal(err)
			}
			jobID := runItemResumeJobToAssessing(t, o, "feedback-target")
			if _, err := o.ConfirmAndRun(ctx, jobID, nil); err != nil {
				t.Fatal(err)
			}
			items, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", jobID)
			if err != nil || len(items) != 1 {
				t.Fatalf("items=%+v %v", items, err)
			}
			original := items[0]
			if original.AnswerSource == nil {
				t.Fatal("target did not adopt asset")
			}
			artifact, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, "mingming", jobID)
			if err != nil {
				t.Fatal(err)
			}
			job, err := o.deps.GetGradingJob(ctx, "mingming", jobID)
			if err != nil {
				t.Fatal(err)
			}
			input, err := o.deps.Records.GetProblemAttemptSnapshot(ctx, "mingming", job.Fields.SubmissionID)
			if err != nil {
				t.Fatal(err)
			}
			command := k12storage.ProblemAssetFeedbackCommand{RequestID: "feedback-one", OwnerID: DefaultLocalOwnerScope, AgentName: "mingming", DispatchID: "feedback-dispatch", JobID: jobID, ProblemID: original.ProblemID, InputRevision: original.InputRevision, ResultDigest: original.ResultDigest, Kind: "answer_error", Reason: "Please verify the adopted answer against the original work."}
			feedback, created, err := o.deps.Records.AcceptProblemAssetFeedback(ctx, command)
			if err != nil || !created {
				t.Fatalf("accept=%+v %v %v", feedback, created, err)
			}
			if err = o.deps.Records.ValidateProblemAssetAdoption(ctx, DefaultLocalOwnerScope, original.AnswerSource.AdoptionID); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
				t.Fatalf("asset still usable: %v", err)
			}
			o.ReleaseGradingRun(jobID)
			solver.answer = "4"
			if failure == "unknown" {
				grader.failure = context.DeadlineExceeded
			} else if failure == "definitive" {
				grader.failure = errItemResumeProvider503
			} else if failure == "blank_untrusted" {
				solver.untrusted = true
			}
			// 新编排器没有任何原进程的 run 状态。
			restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver))
			consumer := NewProblemAssetFeedbackConsumer(o.deps.Records)
			consumer.SetGrading(restarted)
			dispatcher := k12storage.NewDispatcher(o.deps.Records, consumer)
			if err = dispatcher.ProcessPending(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := o.deps.Records.GetProblemAssetFeedback(ctx, DefaultLocalOwnerScope, feedback.FeedbackID)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"": "completed", "unknown": "outcome_unknown", "definitive": "unresolved", "blank_untrusted": "unresolved"}[failure]
			if got.Status != want {
				t.Fatalf("status=%s want=%s error=%s outcomes=%+v", got.Status, want, got.LastError, got.Outcomes)
			}
			if len(solver.snapshots) == 0 || !reflect.DeepEqual(solver.snapshots[len(solver.snapshots)-1], job.Fields.ModelSnapshot) {
				t.Fatal("historical correction did not use the frozen model route")
			}
			view, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, "mingming", jobID, original.ProblemID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, view.Original) {
				t.Fatal("original assessment was overwritten")
			}
			if failure == "" && (view.Correction == nil || view.Current.GradeInvocationID == original.GradeInvocationID || len(got.Outcomes) != 1 || got.Outcomes[0].CorrectionID == "") {
				t.Fatalf("missing fresh correction: %+v", view)
			}
			if failure == "" && (view.Current.Status != k12.GradingAssessmentWrong || view.Current.ProjectionRecordID == "") {
				t.Fatalf("incorrect original answer did not update current learning: %+v", view.Current)
			}
			if failure != "" && (view.Correction != nil || got.LastError == "") {
				t.Fatalf("failed correction falsely applied: %+v", view)
			}
			afterArtifact, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, "mingming", jobID)
			if err != nil || !reflect.DeepEqual(artifact, afterArtifact) {
				t.Fatalf("historical artifact changed: %v", err)
			}
			afterInput, err := o.deps.Records.GetProblemAttemptSnapshot(ctx, "mingming", job.Fields.SubmissionID)
			if err != nil || !reflect.DeepEqual(input, afterInput) {
				t.Fatalf("original answer changed: %v", err)
			}
			beforeSolve, beforeGrade := solver.calls, len(grader.answers)
			if _, created, err = o.deps.Records.AcceptProblemAssetFeedback(ctx, command); err != nil || created {
				t.Fatalf("replay=%v %v", created, err)
			}
			payload, _ := json.Marshal(map[string]string{"feedback_id": feedback.FeedbackID, "owner_id": DefaultLocalOwnerScope})
			if err = consumer.Handle(ctx, k12storage.OutboxEvent{AgentName: "mingming", AggregateID: jobID, EventType: k12storage.EventProblemAssetFeedback, PayloadVersion: 1, Payload: string(payload)}); err != nil {
				t.Fatal(err)
			}
			if solver.calls != beforeSolve || len(grader.answers) != beforeGrade {
				t.Fatal("feedback replay sent another request")
			}
			if failure == "unknown" {
				another := command
				another.RequestID = "feedback-new-request"
				next, _, err := o.deps.Records.AcceptProblemAssetFeedback(ctx, another)
				if err != nil {
					t.Fatal(err)
				}
				payload, _ = json.Marshal(map[string]string{"feedback_id": next.FeedbackID, "owner_id": DefaultLocalOwnerScope})
				if err = consumer.Handle(ctx, k12storage.OutboxEvent{AgentName: "mingming", AggregateID: jobID, EventType: k12storage.EventProblemAssetFeedback, PayloadVersion: 1, Payload: string(payload)}); err != nil {
					t.Fatal(err)
				}
				next, err = o.deps.Records.GetProblemAssetFeedback(ctx, DefaultLocalOwnerScope, next.FeedbackID)
				if err != nil || next.Status != "outcome_unknown" || solver.calls != beforeSolve || len(grader.answers) != beforeGrade {
					t.Fatalf("new feedback bypassed unknown receipt: %+v %v", next, err)
				}
			}
			command.Reason = "different feedback under the same ID"
			if _, _, err = o.deps.Records.AcceptProblemAssetFeedback(ctx, command); !errors.Is(err, k12storage.ErrProblemAssetFeedbackConflict) {
				t.Fatalf("changed replay accepted: %v", err)
			}
		})
	}
}
