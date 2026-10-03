package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

// 只替换外部模型响应；任务、回执、作品与恢复均使用真实持久化链路。
type writingReviewStub struct {
	imageTaskOCRStub
	review         []k12.CreativeWorkOCRReviewSegment
	reviewErr      error
	reviewCalls    int
	reviewRisks    []k12.CreativeWorkIntakeOCRRisk
	crashAfterSend bool
}

func (s *writingReviewStub) ReviewImageTaskWriting(_ context.Context, _ []byte, risks []k12.CreativeWorkIntakeOCRRisk) ([]k12.CreativeWorkOCRReviewSegment, error) {
	s.reviewCalls++
	s.reviewRisks = append([]k12.CreativeWorkIntakeOCRRisk(nil), risks...)
	if s.crashAfterSend {
		panic("simulated crash after local review send")
	}
	return s.review, s.reviewErr
}

func newWritingReviewCoordinator(t *testing.T, content string, risks []k12.CreativeWorkIntakeOCRRisk) (*ImageTaskCoordinator, *writingReviewStub, *imageTaskFeedbackSolver) {
	t.Helper()
	c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{result: ImageTaskClassification{
		Intent: k12.ImageTaskIntentWriting, IntentEvidence: []string{"handwritten essay"}, Confidence: .98,
	}})
	ocr := &writingReviewStub{imageTaskOCRStub: imageTaskOCRStub{result: ImageTaskWritingOCRResult{
		Raw: content, CanonicalContent: content, Confidence: .72, RiskSegments: risks,
	}}}
	c.WritingOCR = ocr
	return c, ocr, c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver)
}

func TestImageTaskWritingLocalReviewBoundsReliableContent(t *testing.T) {
	localRisk := k12.CreativeWorkIntakeOCRRisk{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}}
	for _, tc := range []struct {
		name, content, canonical, outcome string
		risks                             []k12.CreativeWorkIntakeOCRRisk
		review                            []k12.CreativeWorkOCRReviewSegment
		calls                             int
	}{
		{
			name: "unique_resolved", content: "春天到了。〔模糊片段〕小鸟在唱歌。",
			risks: []k12.CreativeWorkIntakeOCRRisk{localRisk}, calls: 1,
			review:    []k12.CreativeWorkOCRReviewSegment{{SegmentID: "line-2", Readable: true, Text: "柳树发芽了。", VisualEvidence: "该行字形清晰"}},
			canonical: "春天到了。柳树发芽了。小鸟在唱歌。", outcome: "complete",
		},
		{
			name: "unresolved_excluded", content: "春天到了。〔模糊片段〕小鸟在唱歌。",
			risks: []k12.CreativeWorkIntakeOCRRisk{localRisk}, calls: 1,
			review:    []k12.CreativeWorkOCRReviewSegment{{SegmentID: "line-2", Readable: false, Text: "不能进入点评的猜测"}},
			canonical: "春天到了。[无法识别]小鸟在唱歌。", outcome: "partial",
		},
		{
			name: "duplicate_text_not_locatable", content: "〔模糊片段〕春天到了。〔模糊片段〕",
			risks: []k12.CreativeWorkIntakeOCRRisk{localRisk}, outcome: "unreadable",
		},
		{
			name: "overlapping_spans_not_reviewed", content: "春天到了。",
			risks: []k12.CreativeWorkIntakeOCRRisk{
				{SegmentID: "line-1", RawText: "春天", Reasons: []string{"illegible"}},
				{SegmentID: "line-2", RawText: "天到了", Reasons: []string{"illegible"}},
			}, outcome: "unreadable",
		},
		{
			name: "document_unreadable", content: "〔模糊片段〕",
			risks:   []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "document", RawText: "〔模糊片段〕", Reasons: []string{"document_unreadable"}}},
			outcome: "unreadable",
		},
		{
			name: "no_reliable_body_after_review", content: "〔模糊片段〕",
			risks: []k12.CreativeWorkIntakeOCRRisk{localRisk}, calls: 1, outcome: "unreadable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ocr, solver := newWritingReviewCoordinator(t, tc.content, tc.risks)
			ocr.review = tc.review
			view, created, err := createAndRunImageTask(t, c, testCreateImageTaskInput())
			if err != nil || !created || view.Creative == nil {
				t.Fatalf("run: created=%v view=%+v err=%v", created, view, err)
			}
			if ocr.calls != 1 || ocr.reviewCalls != tc.calls {
				t.Fatalf("provider calls: OCR=%d review=%d, want 1/%d", ocr.calls, ocr.reviewCalls, tc.calls)
			}
			if tc.calls == 1 && !reflect.DeepEqual(ocr.reviewRisks, tc.risks) {
				t.Fatalf("review escaped declared risk segments: %+v", ocr.reviewRisks)
			}
			evidence := view.Creative.OCREvidence
			if evidence == nil || evidence.Raw != tc.content || evidence.OriginalCanonical != tc.content ||
				evidence.CanonicalContent != tc.canonical || evidence.Outcome != tc.outcome ||
				!reflect.DeepEqual(evidence.RiskSegments, tc.risks) {
				t.Fatalf("immutable evidence or reliable scope drift: %+v", evidence)
			}
			first, err := c.Records.GetImageTaskInvocation(context.Background(), "mingming", "writing-ocr-1")
			var firstEvidence k12.CreativeWorkIntakeOCREvidence
			if err != nil || json.Unmarshal([]byte(first.ResultJSON), &firstEvidence) != nil ||
				firstEvidence.CanonicalContent != tc.content || firstEvidence.Raw != tc.content {
				t.Fatalf("first OCR receipt was overwritten: %+v err=%v", firstEvidence, err)
			}
			if tc.outcome == "unreadable" {
				if view.Creative.Status != k12.CreativeWorkIntakeUnreadable || view.Creative.PromotedWorkID != "" || solver.calls != 0 {
					t.Fatalf("unreadable input created an empty work or feedback: intake=%+v calls=%d", view.Creative, solver.calls)
				}
			} else {
				_, state := assertCurrentCreativeIntakePromotion(t, c, view.Creative)
				if view.CreativeFeedback != "feedback_ready" || solver.calls != 1 || len(solver.requests) != 1 ||
					solver.requests[0].ContentMarkdown != tc.canonical || state.Initial.Source.ContentMarkdown != tc.canonical {
					t.Fatalf("feedback did not use exactly the reliable content: requests=%+v state=%+v", solver.requests, state)
				}
				if solver.requests[0].PartialContent != (tc.outcome == "partial") {
					t.Fatalf("feedback scope drift: %+v", solver.requests[0])
				}
				if tc.outcome == "partial" && (solver.requests[0].ContentLimitations == "" ||
					strings.Contains(solver.requests[0].ContentMarkdown, "不能进入点评的猜测") ||
					!strings.Contains(state.Initial.Feedback.ProjectionMarkdown, solver.requests[0].ContentLimitations)) {
					t.Fatalf("unreadable text leaked or persisted limitation missing: request=%+v state=%+v", solver.requests[0], state)
				}
			}
			result, err := c.Result(context.Background(), "mingming", view.Dispatch.DispatchID)
			if err != nil || result.Kind != "creative" || ocr.calls != 1 || ocr.reviewCalls != tc.calls {
				t.Fatalf("result read must be terminal and pure: kind=%s err=%v", result.Kind, err)
			}
		})
	}
}

// 关闭连接后重新打开同一数据库，并重建控制器，避免内存对象掩盖恢复缺陷。
func reopenWritingCoordinator(t *testing.T, c *ImageTaskCoordinator) *ImageTaskCoordinator {
	t.Helper()
	var path string
	if err := c.Records.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := c.Records.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	registry := scenario.NewRegistry()
	if err := registry.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
		t.Fatal(err)
	}
	restarted := restartImageTaskCoordinator(c, c.Classifier)
	restarted.Records = k12storage.NewStore(db, registry.Records)
	feedback := *c.WorkFeedback.(*Deps)
	feedback.Records = restarted.Records
	restarted.WorkFeedback = &feedback
	return restarted
}

func TestImageTaskWritingReviewDurableRecovery(t *testing.T) {
	t.Run("legacy_migration_preserves_result_and_receipts", func(t *testing.T) {
		c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{result: ImageTaskClassification{
			Intent: k12.ImageTaskIntentWriting, IntentEvidence: []string{"handwritten essay"}, Confidence: .98,
		}})
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db")+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		var prior []migrate.Migration
		for _, migration := range migrate.All {
			if migration.Version < 103 {
				prior = append(prior, migration)
			}
		}
		ctx := context.Background()
		if err := migrate.Run(ctx, db, prior); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO agents(name,metadata) VALUES('mingming','{"k12.grade_term":"五年级上"}')`); err != nil {
			t.Fatal(err)
		}
		registry := scenario.NewRegistry()
		if err := registry.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
			t.Fatal(err)
		}
		c.Records = k12storage.NewStore(db, registry.Records)
		c.WorkFeedback.(*Deps).Records = c.Records
		before, _, err := createAndRunImageTask(t, c, testCreateImageTaskInput())
		if err != nil || before.CreativeFeedback != "feedback_ready" || before.feedbackInvocation == nil {
			t.Fatalf("legacy result: %+v err=%v", before, err)
		}
		ids := []string{"classification-1", "writing-ocr-1", before.feedbackInvocation.InvocationID}
		var receipts []k12.ImageTaskInvocation
		for _, id := range ids {
			receipt, err := c.Records.GetImageTaskInvocation(ctx, "mingming", id)
			if err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, receipt)
		}
		if err := migrate.Run(ctx, db, migrate.All); err != nil {
			t.Fatalf("upgrade existing writing result: %v", err)
		}
		var foreignKeys int
		if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatalf("migration did not restore foreign keys: %d %v", foreignKeys, err)
		}
		if _, err := db.Exec(`UPDATE k12_creative_work_intakes SET source_digest='changed'`); err == nil {
			t.Fatal("migration dropped immutable source trigger")
		}
		if _, err := db.Exec(`UPDATE k12_creative_work_intakes SET promotion_policy='explicit_commit'`); err == nil {
			t.Fatal("migration dropped immutable manual-entry trigger")
		}
		restarted := reopenWritingCoordinator(t, c)
		after, created, err := createAndRunImageTask(t, restarted, testCreateImageTaskInput())
		if err != nil || created || !reflect.DeepEqual(before.Creative, after.Creative) ||
			!reflect.DeepEqual(before.CreativeWork, after.CreativeWork) ||
			c.WritingOCR.(*imageTaskOCRStub).calls != 1 || c.WorkFeedback.(*Deps).Solver.(*imageTaskFeedbackSolver).calls != 1 {
			t.Fatalf("migration/restart changed completed work or resent request: created=%v err=%v", created, err)
		}
		for i, id := range ids {
			receipt, err := restarted.Records.GetImageTaskInvocation(ctx, "mingming", id)
			if err != nil || !reflect.DeepEqual(receipt, receipts[i]) {
				t.Fatalf("migration removed or changed receipt %s: %+v err=%v", id, receipt, err)
			}
		}
	})
	for _, mode := range []string{"sent_crash", "outcome_unknown", "frozen_before_feedback", "completed"} {
		t.Run(mode, func(t *testing.T) {
			c, ocr, solver := newWritingReviewCoordinator(t, "春天到了。〔模糊片段〕", []k12.CreativeWorkIntakeOCRRisk{
				{SegmentID: "line-2", RawText: "〔模糊片段〕", Reasons: []string{"illegible"}},
			})
			ocr.review = []k12.CreativeWorkOCRReviewSegment{{SegmentID: "line-2", Readable: true, Text: "小鸟在唱歌。", VisualEvidence: "该行字形可见"}}
			if mode == "sent_crash" {
				ocr.crashAfterSend = true
			}
			if mode == "outcome_unknown" {
				ocr.reviewErr = context.DeadlineExceeded
			}
			if mode == "frozen_before_feedback" {
				c.ResolveWorkFeedbackRoute = func(context.Context, string, k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
					panic("simulated crash after review freeze before feedback")
				}
			}
			var runErr error
			crashed := false
			func() {
				defer func() { crashed = recover() != nil }()
				_, _, runErr = createAndRunImageTask(t, c, testCreateImageTaskInput())
			}()
			wantCrash := mode == "sent_crash" || mode == "frozen_before_feedback"
			if crashed != wantCrash || (runErr != nil) != (mode == "outcome_unknown") {
				t.Fatalf("checkpoint not reached: crash=%v err=%v", crashed, runErr)
			}
			before, err := c.Get(context.Background(), "mingming", "dispatch-1")
			if err != nil || before.Creative == nil || ocr.calls != 1 || ocr.reviewCalls != 1 {
				t.Fatalf("missing durable checkpoint: view=%+v err=%v calls=%d/%d", before, err, ocr.calls, ocr.reviewCalls)
			}
			invocation, err := c.Records.GetImageTaskInvocation(context.Background(), "mingming", "writing-review-1")
			wantStatus := k12.ImageTaskInvocationSucceeded
			if mode == "sent_crash" {
				wantStatus = k12.ImageTaskInvocationSent
			} else if mode == "outcome_unknown" {
				wantStatus = k12.ImageTaskInvocationOutcomeUnknown
			}
			if err != nil || invocation.Status != wantStatus {
				t.Fatalf("review receipt: %+v err=%v, want %s", invocation, err, wantStatus)
			}
			restarted := reopenWritingCoordinator(t, c)
			restarted.ResolveWorkFeedbackRoute = imageTaskWorkFeedbackRouteForTest
			ocr.crashAfterSend, ocr.reviewErr = false, nil
			recovered, err := restarted.Recover(context.Background(), []string{"mingming"})
			wantRecovered := 0
			if mode == "frozen_before_feedback" {
				wantRecovered = 1
			}
			if err != nil || recovered != wantRecovered {
				t.Fatalf("recovery: count=%d want=%d err=%v", recovered, wantRecovered, err)
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := restarted.Wait(waitCtx); err != nil {
				t.Fatal(err)
			}
			replayed, created, err := createAndRunImageTask(t, restarted, testCreateImageTaskInput())
			if err != nil || created || replayed.Dispatch.DispatchID != before.Dispatch.DispatchID ||
				replayed.Creative == nil || replayed.Creative.IntakeID != before.Creative.IntakeID ||
				ocr.calls != 1 || ocr.reviewCalls != 1 {
				t.Fatalf("restart/replay duplicated task or provider call: created=%v err=%v calls=%d/%d view=%+v", created, err, ocr.calls, ocr.reviewCalls, replayed)
			}
			if mode == "sent_crash" || mode == "outcome_unknown" {
				if solver.calls != 0 || replayed.Creative.PromotedWorkID != "" || replayed.Creative.Status == k12.CreativeWorkIntakeUnreadable {
					t.Fatalf("unknown technical outcome became a content result: %+v feedback_calls=%d", replayed.Creative, solver.calls)
				}
				restarted.Now = func() int64 { return before.Dispatch.AutomaticDeadlineAt + 1 }
				expired, err := restarted.Run(context.Background(), "mingming", "dispatch-1")
				if err != nil || expired.Creative == nil || expired.Creative.Status == k12.CreativeWorkIntakeUnreadable ||
					expired.Creative.PromotedWorkID != "" || solver.calls != 0 || ocr.reviewCalls != 1 {
					t.Fatalf("deadline changed unknown review into a content result: %+v err=%v", expired.Creative, err)
				}
			} else {
				if solver.calls != 1 || replayed.CreativeFeedback != "feedback_ready" ||
					replayed.Creative.PromotedWorkID != before.Creative.PromotedWorkID ||
					replayed.Creative.PromotedGenerationID != before.Creative.PromotedGenerationID ||
					replayed.Creative.OCREvidence.CanonicalContent != "春天到了。小鸟在唱歌。" {
					t.Fatalf("recovery failed to reuse frozen content/work/result: %+v feedback_calls=%d", replayed, solver.calls)
				}
				if mode == "completed" && !reflect.DeepEqual(before.CreativeWork.GenerationState, replayed.CreativeWork.GenerationState) {
					t.Fatal("completed feedback changed after restart")
				}
			}
			var reviewCount int
			if err := restarted.Records.DB().QueryRow(`SELECT count(*) FROM k12_image_task_invocations WHERE operation_key=?`,
				"intake:"+before.Creative.IntakeID+":writing-ocr-review").Scan(&reviewCount); err != nil || reviewCount != 1 {
				t.Fatalf("review receipt duplicated after reopen: count=%d err=%v", reviewCount, err)
			}
		})
	}
}
