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

type correctedAssetFixture struct {
	o               *GradingOrchestrator
	solver          *assetReuseSolver
	grader          *assetReuseGrader
	original        k12.GradingAssessmentItem
	effective       k12.EffectiveGradingAssessment
	originalVersion k12.ProblemAssetVersion
	consumer        ProblemAssetConsumer
	event           k12storage.OutboxEvent
}

// 文件 SQLite 中保留真实作答、调用回执、纠正和 Outbox；仅求解及评分边界可控。
func newCorrectedAssetFixture(t *testing.T, model, unknown, blank bool, grounded ...bool) correctedAssetFixture {
	t.Helper()
	ctx := context.Background()
	seedSolver, grader := &feedbackReassessmentSolver{answer: "5"}, &assetReuseGrader{}
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
	o.deps.Solver, o.deps.Grader, o.deps.VerifiedGrader = seedSolver, grader, nil
	o.deps.TextbookOwnerID = DefaultLocalOwnerScope
	question := RecognizedQuestion{Question: "2+2=", Subject: "数学", StudentAnswer: "5", AnswerState: AnswerStatePresent}
	if blank {
		question.StudentAnswer, question.AnswerState = "", AnswerStateBlank
	}
	o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{question}}
	assets := ProblemAssetConsumer{Records: o.deps.Records}
	dispatcher := k12storage.NewDispatcher(o.deps.Records, assets)
	seed := runItemResumeJobToAssessing(t, o, "replacement-seed")
	if _, err := o.ConfirmAndRun(ctx, seed, nil); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	job := runItemResumeJobToAssessing(t, o, "replacement-target")
	if _, err := o.ConfirmAndRun(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
	items, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", job)
	if err != nil || len(items) != 1 || items[0].AnswerSource == nil {
		t.Fatalf("missing adoption: %+v %v", items, err)
	}
	original := items[0]
	version, err := o.deps.Records.GetProblemAssetVersion(ctx, DefaultLocalOwnerScope, original.AnswerSource.AssetID, original.AnswerSource.AssetVersion)
	if err != nil {
		t.Fatal(err)
	}
	feedback, created, err := o.deps.Records.AcceptProblemAssetFeedback(ctx, k12storage.ProblemAssetFeedbackCommand{
		RequestID: "replacement-feedback", OwnerID: DefaultLocalOwnerScope, AgentName: "mingming", DispatchID: "replacement-dispatch", JobID: job, ProblemID: original.ProblemID,
		InputRevision: original.InputRevision, ResultDigest: original.ResultDigest, Kind: "answer_error", Reason: "Verify the adopted answer against the original work."})
	if err != nil || !created {
		t.Fatalf("feedback: %v %v", created, err)
	}
	o.ReleaseGradingRun(job)
	solver := &assetReuseSolver{model: model}
	o.deps.Solver = solver
	if unknown {
		grader.failure = context.DeadlineExceeded
	}
	restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver))
	correctionConsumer := NewProblemAssetFeedbackConsumer(o.deps.Records)
	correctionConsumer.SetGrading(restarted)
	if len(grounded) > 0 && grounded[0] {
		receipt := groundingRecoveryReceipt()
		snapshot := GroundingSnapshot{AgentName: "mingming", LearnerID: "learner", Subject: "数学",
			TextbookBindingID: receipt.TextbookBindingID, TextbookManifestID: receipt.TextbookManifestID,
			DocumentID: receipt.DocumentID, DocumentGeneration: receipt.DocumentGeneration,
			SourceDigest: receipt.SourceDigest, Edition: "人教版", Volume: "下册", VectorRevisionID: receipt.VectorRevisionID,
			SegmentRefs: []string{receipt.ChunkID}, PageRefs: []k12.TextbookGroundingPageRef{{LogicalPage: receipt.LogicalPage,
				PDFPage: receipt.PDFPage, SegmentRefs: []string{receipt.ChunkID}}}}
		ctx, err = WithVerifiedGradingGrounding(ctx, snapshot, GroundingSnapshotResult{
			Text: "本次批改实际消费的教材命中", Found: true, Receipts: []GroundingEvidenceReceipt{receipt}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = k12storage.NewDispatcher(o.deps.Records, correctionConsumer).ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	accepted, err := o.deps.Records.GetProblemAssetFeedback(ctx, DefaultLocalOwnerScope, feedback.FeedbackID)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := o.deps.Records.GetEffectiveGradingAssessment(ctx, "mingming", job, original.ProblemID)
	if err != nil || !reflect.DeepEqual(original, effective.Original) {
		t.Fatalf("historical assessment: %+v %v", effective, err)
	}
	fixture := correctedAssetFixture{o: o, solver: solver, grader: grader, original: original, effective: effective, originalVersion: version, consumer: assets}
	if unknown {
		if accepted.Status != "outcome_unknown" || effective.Correction != nil {
			t.Fatalf("unknown applied: %+v", accepted)
		}
	} else {
		if accepted.Status != "completed" || effective.Correction == nil {
			t.Fatalf("correction missing: %+v", accepted)
		}
		fixture.event = correctedAssetEvent(t, o.deps.Records, effective.Correction.CorrectionID)
	}
	return fixture
}

func correctedAssetEvent(t *testing.T, s *k12storage.Store, id string) k12storage.OutboxEvent {
	t.Helper()
	var ev k12storage.OutboxEvent
	if err := s.DB().QueryRow(`SELECT event_id,agent_name,aggregate_id,event_type,payload_version,payload_json FROM outbox_events WHERE event_id=?`, "assessment-corrected:"+id).
		Scan(&ev.EventID, &ev.AgentName, &ev.AggregateID, &ev.EventType, &ev.PayloadVersion, &ev.Payload); err != nil {
		t.Fatal(err)
	}
	return ev
}

func (f correctedAssetFixture) publication(t *testing.T) k12.ProblemAssetPublication {
	t.Helper()
	source, err := f.o.deps.Records.GetProblemAssetCorrectionPublicationSource(context.Background(), "mingming", f.effective.Correction.CorrectionID)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := f.consumer.correctedAssetPublication(context.Background(), source)
	if err != nil || publication == nil {
		t.Fatalf("publication missing: %v", err)
	}
	return *publication
}

// 发布只增加资产版本，不能把同一作答的纠正计作一次新学习或更改原终稿。
func (f correctedAssetFixture) learningSnapshot(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	s := f.o.deps.Records
	effective, err := s.GetEffectiveGradingAssessment(ctx, "mingming", f.original.JobID, f.original.ProblemID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := s.GetGradingFinalArtifactByJob(ctx, "mingming", f.original.JobID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.o.deps.GetGradingJob(ctx, "mingming", f.original.JobID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := s.GetProblemAttemptSnapshot(ctx, "mingming", job.Fields.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []any{effective, artifact, input}
	if effective.Current.ProjectionRecordID != "" {
		mistake, err := s.Get(ctx, effective.Current.ProjectionRecordID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, mistake)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProblemAssetCorrectionPublication(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		model, blank, grounded bool
	}{{name: "local_receipt"}, {name: "model_execution_receipts", model: true}, {name: "blank_solve_receipt", blank: true},
		{name: "grounded_local_receipt", grounded: true}, {name: "grounded_blank_solve_receipt", blank: true, grounded: true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCorrectedAssetFixture(t, tc.model, false, tc.blank, tc.grounded)
			ctx := context.Background()
			s := f.o.deps.Records
			before := f.learningSnapshot(t)
			calls, grades := f.solver.calls, len(f.grader.answers)
			publication := f.publication(t)
			if publication.Verification.InvocationID != f.effective.Current.SolveInvocationID {
				t.Fatal("publication lost fresh solve identity")
			}
			invocation, err := s.GetGradingItemInvocation(ctx, "mingming", publication.Verification.InvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.grounded {
				payload, _, enveloped, err := decodeGroundedPhysicalPayload(invocation.ResultJSON, nil)
				var answer SolveHomeworkResult
				if err != nil || !enveloped || publication.AnswerResultJSON != payload ||
					json.Unmarshal([]byte(publication.AnswerResultJSON), &answer) != nil || answer.Solution != publication.Answer ||
					answer.Evidence.Verdict != VerdictAgree || answer.Evidence.EvidenceType != EvidenceNumericExec {
					t.Fatalf("grounded receipt did not export its original flat solve payload: enveloped=%v error=%v", enveloped, err)
				}
				if publication.Verification.ResultDigest != invocation.ResultDigest || publication.Verification.InputDigest != invocation.InputDigest {
					t.Fatal("flat projection changed the original receipt proof")
				}
			}
			if err := k12storage.NewDispatcher(s, f.consumer).ProcessPending(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := s.FindExactProblemAsset(ctx, DefaultLocalOwnerScope, f.originalVersion.Facts)
			if err != nil || got.Version != 2 || got.Answer != "4" {
				t.Fatalf("replacement: %+v %v", got, err)
			}
			old, err := s.GetProblemAssetVersion(ctx, DefaultLocalOwnerScope, got.AssetID, 1)
			if err != nil || !reflect.DeepEqual(old, f.originalVersion) {
				t.Fatalf("old version changed: %+v %v", old, err)
			}
			if before != f.learningSnapshot(t) {
				t.Fatal("asset publication changed assessment, final artifact, original input or learning evidence")
			}
			if err = f.consumer.Handle(ctx, f.event); err != nil {
				t.Fatal(err)
			}
			replay, created, err := s.PublishCorrectedProblemAsset(ctx, publication, f.original.JobID, f.effective.Correction.CorrectionID)
			if err != nil || created || !reflect.DeepEqual(replay, got) {
				t.Fatalf("replay: %+v %v %v", replay, created, err)
			}
			var versions, events int
			if err = s.DB().QueryRow(`SELECT COUNT(*) FROM k12_problem_asset_versions WHERE asset_id=?`, got.AssetID).Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if err = s.DB().QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE event_type='k12.problem_asset.published' AND aggregate_id=?`, got.AssetID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if versions != 2 || events != 2 || f.solver.calls != calls || len(f.grader.answers) != grades {
				t.Fatalf("replay added work: versions=%d events=%d solve=%d grade=%d", versions, events, f.solver.calls, len(f.grader.answers))
			}
			after, err := s.GetGradingItemInvocation(ctx, "mingming", invocation.InvocationID)
			if err != nil || !reflect.DeepEqual(after, invocation) || got.AnswerResultJSON != publication.AnswerResultJSON {
				t.Fatalf("publication or replay changed the successful receipt or flat asset payload: %v", err)
			}
		})
	}
	t.Run("newer_asset_kept", func(t *testing.T) {
		f := newCorrectedAssetFixture(t, false, false, false)
		p := f.publication(t)
		p.PublicationID = "independent-replacement"
		expected, created, err := f.o.deps.Records.PublishProblemAsset(context.Background(), p)
		if err != nil || !created {
			t.Fatalf("newer asset: %v %v", created, err)
		}
		if err = f.consumer.Handle(context.Background(), f.event); err != nil {
			t.Fatal(err)
		}
		got, err := f.o.deps.Records.FindExactProblemAsset(context.Background(), DefaultLocalOwnerScope, f.originalVersion.Facts)
		if err != nil || !reflect.DeepEqual(expected, got) {
			t.Fatalf("late correction replaced newer version: %+v %v", got, err)
		}
	})
	t.Run("latest_correction_rechecked_in_transaction", func(t *testing.T) {
		f := newCorrectedAssetFixture(t, false, false, false)
		p := f.publication(t)
		s, ctx := f.o.deps.Records, context.Background()
		grade, err := s.GetGradingItemInvocation(ctx, "mingming", f.effective.Current.GradeInvocationID)
		if err != nil {
			t.Fatal(err)
		}
		grade.InvocationID = "newer-correction-grade"
		grade.OperationAttempt++
		grade.RequestDigest = "newer-correction-request"
		if _, _, err = s.PrepareGradingItemInvocation(ctx, grade); err != nil {
			t.Fatal(err)
		}
		if _, err = s.MarkGradingItemInvocationSent(ctx, "mingming", grade.InvocationID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.MarkGradingItemInvocationSucceeded(ctx, "mingming", grade.InvocationID, grade.ResultDigest, grade.ResultJSON); err != nil {
			t.Fatal(err)
		}
		next := *f.effective.Correction
		next.PreviousCorrectionID, next.CorrectionID = next.CorrectionID, "newer-answer-correction"
		next.Revision, next.CreatedAt = 0, 0
		next.Assessment.GradeInvocationID = grade.InvocationID
		current, created, err := s.AppendGradingAssessmentCorrection(ctx, next, k12storage.GradingAssessmentEffects{})
		if err != nil || !created {
			t.Fatalf("newer correction: %v %v", created, err)
		}
		if _, _, err = s.PublishCorrectedProblemAsset(ctx, p, f.original.JobID, f.effective.Correction.CorrectionID); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("stale publication accepted: %v", err)
		}
		if err = f.consumer.Handle(ctx, f.event); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FindExactProblemAsset(ctx, DefaultLocalOwnerScope, f.originalVersion.Facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("old correction activated asset: %v", err)
		}
		if err = f.consumer.Handle(ctx, correctedAssetEvent(t, s, current.CorrectionID)); err != nil {
			t.Fatal(err)
		}
		if got, err := s.FindExactProblemAsset(ctx, DefaultLocalOwnerScope, f.originalVersion.Facts); err != nil || got.Version != 2 {
			t.Fatalf("latest correction not published: %+v %v", got, err)
		}
	})
	t.Run("old_proof_rejected", func(t *testing.T) {
		f := newCorrectedAssetFixture(t, false, false, false)
		p := f.publication(t)
		s := f.o.deps.Records
		var oldProof string
		if err := s.DB().QueryRow(`SELECT verification_json FROM k12_problem_asset_publications WHERE asset_id=? AND asset_version=1`, f.originalVersion.AssetID).Scan(&oldProof); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(oldProof), &p.Verification); err != nil {
			t.Fatal(err)
		}
		p.Answer, p.AnswerResultJSON = f.originalVersion.Answer, f.originalVersion.AnswerResultJSON
		if _, _, err := s.PublishCorrectedProblemAsset(context.Background(), p, f.original.JobID, f.effective.Correction.CorrectionID); !errors.Is(err, k12storage.ErrProblemAssetEvidence) {
			t.Fatalf("old proof accepted: %v", err)
		}
		if _, err := s.FindExactProblemAsset(context.Background(), DefaultLocalOwnerScope, f.originalVersion.Facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("old proof activated asset: %v", err)
		}
	})
	t.Run("unknown_never_publishes_or_resends", func(t *testing.T) {
		f := newCorrectedAssetFixture(t, false, true, false)
		calls, grades := f.solver.calls, len(f.grader.answers)
		s := f.o.deps.Records
		before := f.learningSnapshot(t)
		if err := k12storage.NewDispatcher(s, f.consumer).ProcessPending(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FindExactProblemAsset(context.Background(), DefaultLocalOwnerScope, f.originalVersion.Facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("unknown activated asset: %v", err)
		}
		if f.learningSnapshot(t) != before || f.solver.calls != calls || len(f.grader.answers) != grades {
			t.Fatal("unknown changed learning or resent")
		}
	})
}
