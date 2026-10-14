package usecase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

const StageRecoverySourceText = "0.25x+0.5x=18。先合并为0.75x=18，解得x=24，代回左边等于18。"

type StageRecoveryGenerator struct {
	resolveCalls  atomic.Int32
	generateCalls atomic.Int32
	unknown       atomic.Bool
}

func (g *StageRecoveryGenerator) ResolveUnitSummary(_ context.Context, input UnitSummaryResolveInput) (UnitSummaryResolution, error) {
	g.resolveCalls.Add(1)
	intent := "generate"
	if input.Request.ActiveRevisionID != "" {
		intent = "revise"
	}
	return UnitSummaryResolution{Subject: "math", Intent: intent, UnitTitle: "分配律与方程", Format: "standard", ProvidedMaterialText: StageRecoverySourceText}, nil
}

func (g *StageRecoveryGenerator) GenerateUnitSummary(_ context.Context, input UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.generateCalls.Add(1)
	if g.unknown.Load() {
		return k12.UnitSummaryContentV1{}, context.DeadlineExceeded
	}
	if len(input.Context.Sources) != 1 {
		return k12.UnitSummaryContentV1{}, errors.New("expected one actually frozen material")
	}
	body := "先合并同类项，0.75x=18，得到x=24。"
	if strings.Contains(input.FinalUserText, "补充代回检验") {
		body = "先合并同类项，0.75x=18，得到x=24；代回原式左右都是18。"
	}
	ref := input.Context.Sources[0].Ref
	return k12.UnitSummaryContentV1{
		SchemaVersion: 1, Subject: "math", Title: "分配律与方程复习",
		ParentPlan: []string{"先合并同类项，再求未知数，最后代回检查。"},
		Goals:      []k12.UnitSummaryGoal{{ID: "goal", Text: "理解等式变形并能代回检查", SourceRefs: []string{ref}}},
		Knowledge:  []k12.UnitSummaryKnowledge{{ID: "knowledge", Kind: "method", Title: "合并与检验", BodyMD: body, SourceRefs: []string{ref}}},
		Examples: []k12.UnitSummaryExample{{ID: "example", GoalIDs: []string{"goal"}, Origin: "ai_created", PromptMD: "解方程：0.25x+0.5x=18", DemonstrationMD: body,
			ParentGuide: k12.UnitSummaryParentGuide{Ask: "一共有多少个x？", Explain: "相同未知数的系数相加。", Hint: "把0.25与0.5相加。"}}},
		TransferChecks:        []k12.UnitSummaryTransferCheck{{ID: "check", ExampleID: "example", QuestionMD: "解方程：0.5x+0.5x=6"}},
		ReferenceExplanations: []k12.UnitSummaryReferenceExplanation{{CheckID: "check", ExplanationMD: "合并得到x=6，代回3+3=6。"}},
		Coverage:              input.Context.Coverage,
	}, nil
}

type StageRecoveryRenderer struct {
	calls    atomic.Int32
	failNext atomic.Bool
}

func (r *StageRecoveryRenderer) Render(_ context.Context, markdown, format string) ([]byte, string, error) {
	r.calls.Add(1)
	if format != "pdf" {
		return nil, "", fmt.Errorf("unexpected render format %q", format)
	}
	if r.failNext.Swap(false) {
		return nil, "", errors.New("controlled PDF preparation failure")
	}
	// 这里只验证冻结字节与阶段复用，不把可控载荷当真实PDF版式证据。
	return []byte("%PDF-1.7\n" + markdown + "\n%%EOF\n"), "application/pdf", nil
}

type StageRecoveryFixture struct {
	ctx         context.Context
	db          *sql.DB
	store       *k12storage.Store
	generator   *StageRecoveryGenerator
	renderer    *StageRecoveryRenderer
	coordinator *UnitSummaryCoordinator
	request     UnitSummaryRequest
	old         k12.UnitSummaryRevision
	oldRender   k12.PrintArtifactRender
}

func StageRecoveryNewFixture(t *testing.T) *StageRecoveryFixture {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "stage-recovery.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO agents(name,display_name) VALUES('stage-recovery-child','Stage recovery child')`,
		`INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES('stage-recovery-session','stage-recovery-owner',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	f := &StageRecoveryFixture{ctx: ctx, db: db, store: k12storage.NewStore(db, nil), generator: &StageRecoveryGenerator{}, renderer: &StageRecoveryRenderer{}}
	f.coordinator = StageRecoveryCoordinator(f)
	initial := StageRecoveryRequest("stage-recovery-original", "整理数学材料："+StageRecoverySourceText)
	StageRecoveryInsertMessage(t, f, initial)
	view, replayed, err := f.coordinator.StartAndWait(ctx, initial)
	if err != nil || replayed {
		t.Fatalf("initial material: error=%v replayed=%v", err, replayed)
	}
	StageRecoveryWaitIdle(t, f.coordinator)
	if view.Job.State != "succeeded" || !view.Delivery.Complete || view.Material == nil || view.Material.Version != 1 {
		t.Fatalf("initial public material was not fully published: %+v", view)
	}
	f.old = view.Material.UnitSummaryRevision
	f.oldRender, err = f.store.GetPrintArtifactRender(ctx, initial.AgentName, f.old.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.oldRender.Payload) == 0 || f.generator.generateCalls.Load() != 1 || f.renderer.calls.Load() != 1 {
		t.Fatal("initial real storage publication did not produce one frozen byte payload")
	}
	f.request = StageRecoveryRequest("stage-recovery-edit", "补充代回检验，保留原范围："+StageRecoverySourceText)
	f.request.ActiveRevisionID = f.old.RevisionID
	StageRecoveryInsertMessage(t, f, f.request)
	return f
}

func StageRecoveryCoordinator(f *StageRecoveryFixture) *UnitSummaryCoordinator {
	c := NewUnitSummaryCoordinator(Deps{Records: f.store, Renderer: f.renderer}, f.generator,
		func(_ context.Context, route k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
			return route, nil
		})
	c.SetBaseContext(f.ctx)
	return c
}

func StageRecoveryRequest(message, text string) UnitSummaryRequest {
	return UnitSummaryRequest{OwnerID: "stage-recovery-owner", AgentName: "stage-recovery-child", SessionID: "stage-recovery-session",
		SourceMessageID: message, IdempotencyKey: message, FinalUserText: text,
		ModelSnapshot: k12.GradingModelSnapshot{Provider: "controlled", Model: "stage-recovery", Route: "controlled/stage-recovery"}}
}

func StageRecoveryInsertMessage(t *testing.T, f *StageRecoveryFixture, request UnitSummaryRequest) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO messages(id,session_id,role,content,created_at) VALUES(?,?,'user',?,CURRENT_TIMESTAMP)`, request.SourceMessageID, request.SessionID, request.FinalUserText); err != nil {
		t.Fatal(err)
	}
}

func StageRecoveryWaitIdle(t *testing.T, c *UnitSummaryCoordinator) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		active := len(c.active)
		c.mu.Unlock()
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("coordinator did not release its single local worker")
		}
		time.Sleep(time.Millisecond)
	}
}

func StageRecoveryStartTarget(t *testing.T, f *StageRecoveryFixture) UnitSummaryView {
	t.Helper()
	view, replayed, err := f.coordinator.Start(context.Background(), f.request)
	if err != nil || replayed {
		t.Fatalf("start independent revision attempt: error=%v replayed=%v", err, replayed)
	}
	StageRecoveryWaitIdle(t, f.coordinator)
	view, err = f.coordinator.Get(context.Background(), f.request.OwnerID, f.request.AgentName, view.Job.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Job.DocumentID != f.old.DocumentID || view.Job.AttemptID == f.old.AttemptID {
		t.Fatalf("revision did not stay on the existing document: old=%+v new=%+v", f.old, view.Job)
	}
	return view
}

func StageRecoveryOldUnchanged(t *testing.T, f *StageRecoveryFixture, keepHead bool) {
	t.Helper()
	ctx := context.Background()
	old, err := f.store.GetUnitSummaryRevision(ctx, f.request.OwnerID, f.request.AgentName, f.old.DocumentID, f.old.RevisionID)
	if err != nil || !reflect.DeepEqual(old, f.old) {
		t.Fatalf("old immutable v1 changed: error=%v", err)
	}
	render, err := f.store.GetPrintArtifactRender(ctx, f.request.AgentName, f.old.ArtifactID)
	if err != nil || !reflect.DeepEqual(render, f.oldRender) {
		t.Fatalf("old frozen PDF bytes or metadata changed: error=%v", err)
	}
	if keepHead {
		document, err := f.store.GetUnitSummaryDocument(ctx, f.request.OwnerID, f.request.AgentName, f.old.DocumentID)
		if err != nil || document.CurrentRevisionID != f.old.RevisionID || document.HeadVersion != 1 {
			t.Fatalf("incomplete candidate displaced original head: document=%+v error=%v", document, err)
		}
	}
}

func StageRecoveryInvocations(t *testing.T, f *StageRecoveryFixture, attempt string) []k12.ModelInvocation {
	t.Helper()
	invocations, err := f.store.ListUnitSummaryModelInvocations(context.Background(), f.request.AgentName, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 2 {
		t.Fatalf("expected exactly one resolve and one generate receipt, got %d", len(invocations))
	}
	return invocations
}

func StageRecoverySuccessfulReceipts(t *testing.T, f *StageRecoveryFixture, attempt string) []k12.ModelInvocation {
	t.Helper()
	invocations := StageRecoveryInvocations(t, f, attempt)
	stages := map[string]bool{}
	for _, invocation := range invocations {
		if invocation.Status != k12.ModelInvocationSucceeded || invocation.Attempt != 1 || invocation.ResultJSON == "" {
			t.Fatalf("successful original model receipt missing: %+v", invocation)
		}
		stages[invocation.Stage] = true
	}
	if !stages["unit_summary_resolve"] || !stages["unit_summary_generate"] {
		t.Fatal("one of the two real coordinator stage receipts is missing")
	}
	return invocations
}

func StageRecoveryAssertPublished(t *testing.T, f *StageRecoveryFixture, view UnitSummaryView, before []k12.ModelInvocation) {
	t.Helper()
	if view.Job.State != "succeeded" || !view.Delivery.Complete || view.Material == nil || view.Material.Version != 2 || view.Material.DocumentID != f.old.DocumentID {
		t.Fatalf("same-task revision did not publish one complete v2: %+v", view)
	}
	document, err := f.store.GetUnitSummaryDocument(context.Background(), f.request.OwnerID, f.request.AgentName, f.old.DocumentID)
	if err != nil || document.CurrentRevisionID != view.Material.RevisionID || document.HeadVersion != 2 {
		t.Fatalf("completed v2 was not the only new head: %+v error=%v", document, err)
	}
	if f.generator.resolveCalls.Load() != 2 || f.generator.generateCalls.Load() != 2 {
		t.Fatalf("recovery repeated successful model work: resolve=%d generate=%d", f.generator.resolveCalls.Load(), f.generator.generateCalls.Load())
	}
	if after := StageRecoverySuccessfulReceipts(t, f, view.Job.AttemptID); !reflect.DeepEqual(before, after) {
		t.Fatal("successful original model receipts changed during local stage recovery")
	}
	StageRecoveryOldUnchanged(t, f, false)
}

func StageRecoveryResume(t *testing.T, f *StageRecoveryFixture, failed UnitSummaryView) UnitSummaryView {
	t.Helper()
	ctx := f.ctx
	_, _, err := f.coordinator.Resume(ctx, f.request.OwnerID, f.request.AgentName, failed.Job.AttemptID,
		UnitSummaryResumeRequest{ExpectedRevision: failed.Job.Revision - 1, IdempotencyKey: "stage-recovery-stale"})
	if !errors.Is(err, k12storage.ErrUnitSummaryCAS) {
		t.Fatalf("stale expected revision accepted: %v", err)
	}
	still, err := f.store.GetUnitSummaryAttempt(ctx, f.request.OwnerID, f.request.AgentName, failed.Job.AttemptID)
	if err != nil || !reflect.DeepEqual(still, failed.Job) {
		t.Fatalf("rejected stale resume changed the original task: %v", err)
	}
	resume := UnitSummaryResumeRequest{ExpectedRevision: failed.Job.Revision, IdempotencyKey: "stage-recovery-valid"}
	view, replayed, err := f.coordinator.ResumeAndWait(ctx, f.request.OwnerID, f.request.AgentName, failed.Job.AttemptID, resume)
	if err != nil || replayed || view.Job.AttemptID != failed.Job.AttemptID {
		t.Fatalf("same-task resume: error=%v replayed=%v view=%+v", err, replayed, view.Job)
	}
	StageRecoveryWaitIdle(t, f.coordinator)
	renderCalls := f.renderer.calls.Load()
	again, replayed, err := f.coordinator.ResumeAndWait(ctx, f.request.OwnerID, f.request.AgentName, failed.Job.AttemptID, resume)
	if err != nil || !replayed || again.Job.ResultRevisionID != view.Job.ResultRevisionID || f.renderer.calls.Load() != renderCalls {
		t.Fatalf("same resume command was applied twice: error=%v replayed=%v", err, replayed)
	}
	return view
}

func TestUnitSummaryStageRecoverySQLite(t *testing.T) {
	for _, phase := range []string{"generated_content_checkpoint", "pdf_render", "publication_transaction"} {
		t.Run(phase, func(t *testing.T) {
			f := StageRecoveryNewFixture(t)
			ctx := f.ctx
			var trigger string
			switch phase {
			case "generated_content_checkpoint":
				trigger = "StageRecoveryContentWriteFailure"
				_, err := f.db.Exec(`CREATE TRIGGER StageRecoveryContentWriteFailure BEFORE UPDATE ON k12_unit_summary_attempts
					WHEN NEW.source_message_id='stage-recovery-edit' AND OLD.stage='generate' AND NEW.stage='render'
					BEGIN SELECT RAISE(ABORT,'controlled generated content checkpoint failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			case "pdf_render":
				f.renderer.failNext.Store(true)
			case "publication_transaction":
				trigger = "StageRecoveryPublicationFailure"
				_, err := f.db.Exec(`CREATE TRIGGER StageRecoveryPublicationFailure BEFORE INSERT ON k12_unit_summary_revisions
					WHEN (SELECT source_message_id FROM k12_unit_summary_attempts WHERE attempt_id=NEW.attempt_id)='stage-recovery-edit'
					BEGIN SELECT RAISE(ABORT,'controlled revision publication failure'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			failed := StageRecoveryStartTarget(t, f)
			if failed.Delivery.Complete || failed.Material != nil {
				t.Fatal("failed candidate was exposed as a saved material")
			}
			StageRecoveryOldUnchanged(t, f, true)
			receipts := StageRecoverySuccessfulReceipts(t, f, failed.Job.AttemptID)
			if f.generator.generateCalls.Load() != 2 || f.generator.resolveCalls.Load() != 2 {
				t.Fatal("fixture did not perform exactly one successful model call per stage and material")
			}
			var beforePublicationRender k12.PrintArtifactRender
			switch phase {
			case "generated_content_checkpoint":
				if failed.Job.State != "generating" || failed.Job.Stage != "generate" || failed.Job.Content != nil || failed.Job.GeneratedContentJSON != "" || f.renderer.calls.Load() != 1 {
					t.Fatalf("content checkpoint failure lost its actual recovery boundary: %+v", failed.Job)
				}
			case "pdf_render":
				if failed.Job.State != "failed" || failed.Job.Stage != "render" || failed.Job.FailureKind != "pdf_render_failed" || failed.Job.Content == nil || failed.Job.GeneratedContentJSON == "" || failed.Job.Candidate == nil || failed.Job.Candidate.ArtifactID != "" || f.renderer.calls.Load() != 2 {
					t.Fatalf("PDF failure did not retain successful content: %+v", failed.Job)
				}
			case "publication_transaction":
				if failed.Job.State != "failed" || failed.Job.Stage != "publish" || failed.Job.FailureKind != "persist_failed" || failed.Job.Candidate == nil || failed.Job.Candidate.ArtifactID == "" || f.renderer.calls.Load() != 2 {
					t.Fatalf("publication failure did not retain frozen PDF: %+v", failed.Job)
				}
				var err error
				beforePublicationRender, err = f.store.GetPrintArtifactRender(ctx, f.request.AgentName, failed.Job.Candidate.ArtifactID)
				if err != nil {
					t.Fatal(err)
				}
				var versions int
				if err := f.db.QueryRow(`SELECT count(*) FROM k12_unit_summary_revisions WHERE document_id=?`, f.old.DocumentID).Scan(&versions); err != nil || versions != 1 {
					t.Fatalf("failed publication left a partial v2 row: count=%d error=%v", versions, err)
				}
			}
			if trigger != "" {
				if _, err := f.db.Exec("DROP TRIGGER " + trigger); err != nil {
					t.Fatal(err)
				}
			}
			var recovered UnitSummaryView
			if phase == "generated_content_checkpoint" {
				// 已知本地写入失败尚未落为终态；沿正常worker交接释放租约，再以原键恢复。
				if err := f.store.ReleaseUnitSummaryWorkerLeases(ctx, f.coordinator.workerID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.CheckpointUnitSummaryAttempt(ctx, failed.Job, failed.Job); !errors.Is(err, k12storage.ErrUnitSummaryCAS) {
					t.Fatalf("old worker checkpoint survived lease handoff: %v", err)
				}
				f.coordinator = StageRecoveryCoordinator(f)
				var replayed bool
				var err error
				recovered, replayed, err = f.coordinator.StartAndWait(ctx, f.request)
				if err != nil || !replayed || recovered.Job.AttemptID != failed.Job.AttemptID {
					t.Fatalf("cold same-key recovery changed task: error=%v replayed=%v", err, replayed)
				}
				StageRecoveryWaitIdle(t, f.coordinator)
			} else {
				recovered = StageRecoveryResume(t, f, failed)
			}
			StageRecoveryAssertPublished(t, f, recovered, receipts)
			wantRenderCalls := int32(2)
			if phase == "pdf_render" {
				wantRenderCalls = 3
			}
			if f.renderer.calls.Load() != wantRenderCalls {
				t.Fatalf("recovery repeated successful PDF work: got=%d want=%d", f.renderer.calls.Load(), wantRenderCalls)
			}
			if phase == "publication_transaction" {
				render, err := f.store.GetPrintArtifactRender(ctx, f.request.AgentName, recovered.Material.ArtifactID)
				if err != nil || !reflect.DeepEqual(render, beforePublicationRender) {
					t.Fatalf("publication recovery replaced the already frozen PDF: %v", err)
				}
			}
		})
	}
}

func TestUnitSummaryStageRecoveryUnknownDoesNotResendSQLite(t *testing.T) {
	f := StageRecoveryNewFixture(t)
	f.generator.unknown.Store(true)
	unknown := StageRecoveryStartTarget(t, f)
	if unknown.Job.State != "reconciling" || unknown.Job.Stage != "generate" || unknown.Delivery.Complete || unknown.Material != nil {
		t.Fatalf("unknown model outcome was not retained: %+v", unknown.Job)
	}
	StageRecoveryOldUnchanged(t, f, true)
	before := StageRecoveryInvocations(t, f, unknown.Job.AttemptID)
	var pending int
	for _, invocation := range before {
		if invocation.Stage == "unit_summary_generate" && invocation.Status == "outcome_unknown" && invocation.Attempt == 1 && invocation.ResultJSON == "" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatal("unknown generation receipt is missing or was fabricated as a success")
	}
	resume := UnitSummaryResumeRequest{ExpectedRevision: unknown.Job.Revision, IdempotencyKey: "stage-recovery-unknown-observe"}
	view, replayed, err := f.coordinator.ResumeAndWait(context.Background(), f.request.OwnerID, f.request.AgentName, unknown.Job.AttemptID, resume)
	if err != nil || replayed || view.Job.AttemptID != unknown.Job.AttemptID || view.Job.State != "reconciling" || view.Delivery.Complete {
		t.Fatalf("unknown observation changed original task: error=%v replayed=%v view=%+v", err, replayed, view.Job)
	}
	StageRecoveryWaitIdle(t, f.coordinator)
	if after := StageRecoveryInvocations(t, f, unknown.Job.AttemptID); !reflect.DeepEqual(before, after) {
		t.Fatal("unknown observation changed physical model receipts")
	}
	if f.generator.generateCalls.Load() != 2 || f.generator.resolveCalls.Load() != 2 || f.renderer.calls.Load() != 1 {
		t.Fatalf("unknown observation resent work: resolve=%d generate=%d render=%d", f.generator.resolveCalls.Load(), f.generator.generateCalls.Load(), f.renderer.calls.Load())
	}
	StageRecoveryOldUnchanged(t, f, true)
}
