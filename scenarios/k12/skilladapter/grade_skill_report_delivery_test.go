package skilladapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

type reportDeliveryRenderer struct {
	standaloneReportRenderer
	fail      bool
	failCalls map[int]bool
}

type reportDeliveryGrader struct{ calls int }

func (g *reportDeliveryGrader) Grade(context.Context, string, string, string) (usecase.GradeOutcome, error) {
	g.calls++
	return usecase.GradeOutcome{Verdict: usecase.VerdictDisagree, WrongStep: "把合作时间当成单独时间", ErrorCause: "没有区分两队效率", KnowledgePoint: "工程问题"}, nil
}

func TestGradeSkill_GradedReportRecoveryPreservesExactGradingReference(t *testing.T) {
	deps, ctx, solver, renderer := reportDeliveryFixture(t, "graded-report-recovery", false, true)
	grader := &reportDeliveryGrader{}
	deps.Grader = grader
	sk := skilladapter.NewGradeSkill(deps)
	args := reportDeliveryArgs()
	args["student_answer"] = "6天"
	initial, err := sk.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	var refs []struct {
		GradeReportRef *k12.TextGradeReportReference `json:"grade_report_ref"`
		PDFStatus      string                        `json:"pdf_status"`
	}
	if err = json.Unmarshal([]byte(initial.Metadata["text_solve_refs"]), &refs); err != nil || len(refs) != 1 || refs[0].GradeReportRef == nil || refs[0].GradeReportRef.InvocationID == "" || refs[0].GradeReportRef.ResultDigest == "" || refs[0].PDFStatus != "failed" {
		t.Fatalf("failed graded PDF lost its exact assessment receipt: %+v metadata=%v err=%v", refs, initial.Metadata, err)
	}
	gradeRef := *refs[0].GradeReportRef
	if !strings.Contains(initial.Content, "## 一、快速解答") || !strings.Contains(initial.Content, "## 二、深度讲解") || !strings.Contains(initial.Content, "6天") || !strings.Contains(initial.Content, "10天") || initial.Metadata["print_artifacts"] != "" {
		t.Fatalf("graded report must retain the submitted attempt and full correction despite PDF failure: %+v", initial)
	}
	persistReportDeliveryReply(t, deps, ctx, initial)
	renderer.fail = false
	recoveryCtx := nextReportDeliveryTurn(t, deps, ctx, "graded-report-prepare")
	recovered, err := sk.Execute(recoveryCtx, map[string]any{"action": "prepare_report"})
	if err != nil {
		t.Fatal(err)
	}
	files := reportRefs(t, recovered)
	if err = json.Unmarshal([]byte(recovered.Metadata["text_solve_refs"]), &refs); err != nil || len(refs) != 1 || refs[0].GradeReportRef == nil || *refs[0].GradeReportRef != gradeRef {
		t.Fatalf("recovery changed the original grading source: %+v err=%v", refs, err)
	}
	pdf, err := deps.GetPrintableArtifactPDF(ctx, "default", files[0].ArtifactID)
	if err != nil || !strings.Contains(pdf.Artifact.SourceRef, ":grade:"+gradeRef.InvocationID+":"+gradeRef.ResultDigest) || pdf.Artifact.CanonicalMarkdown != recovered.Content || !strings.Contains(recovered.Content, "6天") {
		t.Fatalf("recovery selected the ungraded source instead of the original assessment: source=%s err=%v", pdf.Artifact.SourceRef, err)
	}
	persistReportDeliveryReply(t, deps, recoveryCtx, recovered)
	resent, err := sk.Execute(nextReportDeliveryTurn(t, deps, ctx, "graded-report-resend"), map[string]any{"action": "resend_report"})
	if err != nil || reportRefs(t, resent)[0] != files[0] || resent.Content != recovered.Content || solver.calls != 1 || grader.calls != 1 || renderer.calls != 2 {
		t.Fatalf("graded report resend repeated work or changed frozen content: solve=%d grade=%d render=%d err=%v", solver.calls, grader.calls, renderer.calls, err)
	}
}

func (r *reportDeliveryRenderer) RenderTeachingReport(ctx context.Context, bundle render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	if r.fail || r.failCalls[r.calls+1] {
		r.calls++
		return nil, "", errors.New("PDF compiler unavailable")
	}
	return r.standaloneReportRenderer.RenderTeachingReport(ctx, bundle, input)
}

func TestGradeSkill_PrepareReportRecoversAllFailedSourcesAndKeepsReady(t *testing.T) {
	deps, ctx, solver, renderer := reportDeliveryFixture(t, "report-multiple", false, false)
	sk := skilladapter.NewGradeSkill(deps)
	var texts []string
	var refs []json.RawMessage
	var frozen []k12.PrintReplyArtifact
	for _, suffix := range []string{"第一题", "第二题", "第三题"} {
		args := reportDeliveryArgs()
		args["problem"] = args["problem"].(string) + "（" + suffix + "）"
		result, err := sk.Execute(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, result.Content)
		var source []json.RawMessage
		if err := json.Unmarshal([]byte(result.Metadata["text_solve_refs"]), &source); err != nil || len(source) != 1 {
			t.Fatalf("missing original finalization: %v", err)
		}
		refs = append(refs, source...)
		if !renderer.fail {
			frozen = reportRefs(t, result)
		}
		renderer.fail = true
	}
	encodedSources, _ := json.Marshal(refs)
	encodedFiles, _ := json.Marshal(frozen)
	originalContent := strings.Join(texts, "\n\n")
	persistReportDeliveryReply(t, deps, ctx, &skill.Result{Content: originalContent, Metadata: map[string]string{"text_solve_refs": string(encodedSources), "print_artifacts": string(encodedFiles)}})
	renderer.fail, renderer.failCalls = false, map[int]bool{5: true}
	partialCtx := nextReportDeliveryTurn(t, deps, ctx, "report-multiple-partial")
	partial, err := sk.Execute(partialCtx, map[string]any{"action": "prepare_report"})
	if err != nil {
		t.Fatal(err)
	}
	var partialFiles []k12.PrintReplyArtifact
	if err = json.Unmarshal([]byte(partial.Metadata["print_artifacts"]), &partialFiles); err != nil || len(partialFiles) != 2 || partialFiles[0] != frozen[0] {
		t.Fatalf("ready source must survive mixed recovery: files=%+v err=%v", partialFiles, err)
	}
	var partialSources []map[string]string
	if err = json.Unmarshal([]byte(partial.Metadata["text_solve_refs"]), &partialSources); err != nil || len(partialSources) != 3 {
		t.Fatalf("all source receipts must survive recovery: sources=%+v err=%v", partialSources, err)
	}
	for i, source := range partialSources {
		want := "ready"
		if i == 2 {
			want = "failed"
		}
		if source["pdf_status"] != want || source["source_request_id"] != skill.CurrentInvocation(ctx).RequestID {
			t.Fatalf("source %d lost its result or original request: %+v", i, source)
		}
	}
	if solver.calls != 3 || renderer.calls != 5 || partial.Content != originalContent {
		t.Fatalf("mixed recovery repeated ready work or lost report text: solve=%d render=%d", solver.calls, renderer.calls)
	}
	persistReportDeliveryReply(t, deps, partialCtx, partial)
	renderer.failCalls = nil
	completeCtx := nextReportDeliveryTurn(t, deps, ctx, "report-multiple-complete")
	complete, err := sk.Execute(completeCtx, map[string]any{"action": "prepare_report"})
	if err != nil {
		t.Fatal(err)
	}
	var completeFiles []k12.PrintReplyArtifact
	if err = json.Unmarshal([]byte(complete.Metadata["print_artifacts"]), &completeFiles); err != nil || len(completeFiles) != 3 || completeFiles[0] != partialFiles[0] || completeFiles[1] != partialFiles[1] {
		t.Fatalf("final recovery changed already frozen files: %+v err=%v", completeFiles, err)
	}
	if solver.calls != 3 || renderer.calls != 6 || complete.Content != originalContent || strings.Contains(complete.Metadata["text_solve_refs"], `"pdf_status":"failed"`) {
		t.Fatalf("only the remaining failed source may render: solve=%d render=%d metadata=%v", solver.calls, renderer.calls, complete.Metadata)
	}
}

func TestGradeSkill_PrepareReportRecoversMultipleFailedSources(t *testing.T) {
	deps, ctx, solver, renderer := reportDeliveryFixture(t, "report-all-failed", false, true)
	sk := skilladapter.NewGradeSkill(deps)
	var sources []json.RawMessage
	var bodies []string
	for _, suffix := range []string{"第一题", "第二题"} {
		args := reportDeliveryArgs()
		args["problem"] = args["problem"].(string) + "（" + suffix + "）"
		result, err := sk.Execute(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		var refs []json.RawMessage
		if err := json.Unmarshal([]byte(result.Metadata["text_solve_refs"]), &refs); err != nil || len(refs) != 1 {
			t.Fatalf("missing failed source: %v", err)
		}
		sources = append(sources, refs...)
		bodies = append(bodies, result.Content)
	}
	encoded, _ := json.Marshal(sources)
	original := strings.Join(bodies, "\n\n")
	persistReportDeliveryReply(t, deps, ctx, &skill.Result{Content: original, Metadata: map[string]string{"text_solve_refs": string(encoded)}})
	renderer.fail = false
	result, err := sk.Execute(nextReportDeliveryTurn(t, deps, ctx, "report-all-recover"), map[string]any{"action": "prepare_report"})
	if err != nil {
		t.Fatal(err)
	}
	var files []k12.PrintReplyArtifact
	if err = json.Unmarshal([]byte(result.Metadata["print_artifacts"]), &files); err != nil || len(files) != 2 || files[0].ArtifactID == files[1].ArtifactID {
		t.Fatalf("both original sources must produce their own frozen files: %+v %v", files, err)
	}
	if result.Content != original || solver.calls != 2 || renderer.calls != 4 || strings.Contains(result.Metadata["text_solve_refs"], `"pdf_status":"failed"`) {
		t.Fatalf("multi-source rendering must preserve both reports without solving: solve=%d render=%d metadata=%v", solver.calls, renderer.calls, result.Metadata)
	}
}

func reportDeliveryFixture(t *testing.T, name string, long, fail bool) (usecase.Deps, context.Context, *standaloneSolveFixture, *reportDeliveryRenderer) {
	t.Helper()
	deps := newDeps(t, usecase.GradeOutcome{})
	deps.TextbookOwnerID = "application-owner"
	solver := &standaloneSolveFixture{long: long}
	renderer := &reportDeliveryRenderer{fail: fail}
	deps.Solver, deps.Renderer, deps.TeachingReportResources = solver, renderer, standaloneReportResources{}
	return deps, standaloneGradeContext(t, deps, name), solver, renderer
}

func reportDeliveryArgs() map[string]any {
	return map[string]any{"problem": "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？", "subject": "数学", "grade": "六年级上"}
}

func assertFullReport(t *testing.T, result *skill.Result) {
	t.Helper()
	if result == nil || !strings.Contains(result.Content, "10天") || !strings.Contains(result.Content, "把工作量相加后再求甲队效率") {
		t.Fatalf("complete answer and teaching explanation must survive PDF delivery: %+v", result)
	}
}

func reportRefs(t *testing.T, result *skill.Result) []k12.PrintReplyArtifact {
	t.Helper()
	var refs []k12.PrintReplyArtifact
	if err := json.Unmarshal([]byte(result.Metadata["print_artifacts"]), &refs); err != nil || len(refs) != 1 {
		t.Fatalf("expected one real frozen PDF reference: metadata=%v err=%v", result.Metadata, err)
	}
	return refs
}

func TestGradeSkill_FormalReportPreservesFullContentAndPDF(t *testing.T) {
	for _, long := range []bool{false, true} {
		name := "short"
		if long {
			name = "long"
		}
		t.Run(name, func(t *testing.T) {
			deps, ctx, solver, renderer := reportDeliveryFixture(t, "report-"+name, long, false)
			result, err := skilladapter.NewGradeSkill(deps).Execute(ctx, reportDeliveryArgs())
			if err != nil {
				t.Fatal(err)
			}
			assertFullReport(t, result)
			refs := reportRefs(t, result)
			pdf, err := deps.GetPrintableArtifactPDF(ctx, "default", refs[0].ArtifactID)
			if err != nil || pdf.Artifact.CanonicalMarkdown != result.Content || pdf.Render.ByteDigest != refs[0].ByteDigest {
				t.Fatalf("body and PDF must share one frozen report: %+v err=%v", refs, err)
			}
			if solver.calls != 1 || renderer.calls != 1 {
				t.Fatalf("unexpected repeated work: solve=%d render=%d", solver.calls, renderer.calls)
			}
		})
	}
}

// 保存真实工具结果；就绪附件必须先通过原回合冻结引用校验，而不是手造完成摘要。
func persistReportDeliveryReply(t *testing.T, deps usecase.Deps, ctx context.Context, result *skill.Result) string {
	t.Helper()
	inv := skill.CurrentInvocation(ctx)
	metadata := make(map[string]string, len(result.Metadata)+1)
	for key, value := range result.Metadata {
		metadata[key] = value
	}
	metadata["agent_name"] = inv.RoutedAgentName
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	message := &storage.MessageRecord{ID: inv.RequestID + ":assistant", SessionID: inv.SessionID, RequestID: inv.RequestID, Role: "assistant", Content: result.Content, Metadata: string(raw)}
	tx, err := deps.Records.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if result.Metadata["print_artifacts"] != "" {
		var refs []k12.PrintReplyArtifact
		if err = json.Unmarshal([]byte(result.Metadata["print_artifacts"]), &refs); err != nil {
			t.Fatal(err)
		}
		_, err = deps.Records.CommitConversationPrintReply(ctx, tx, message, k12.ConversationPrintReply{Owner: inv.AuthenticatedUserID, AgentName: inv.RoutedAgentName, SessionID: inv.SessionID, RequestID: inv.RequestID, SourceMessageID: inv.SourceMessageID, Platform: "web", Locale: "zh-CN", Artifacts: refs})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO messages(id,session_id,role,content,metadata,request_id,created_at) VALUES(?,?,?,?,?,?,CURRENT_TIMESTAMP)`, message.ID, message.SessionID, message.Role, message.Content, message.Metadata, message.RequestID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return message.ID
}

func nextReportDeliveryTurn(t *testing.T, deps usecase.Deps, prior context.Context, request string) context.Context {
	t.Helper()
	inv := skill.CurrentInvocation(prior)
	inv.RequestID, inv.SourceMessageID = request, request+":user"
	if _, err := deps.Records.DB().Exec(`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES(?,?,'user','重发上份PDF',?,CURRENT_TIMESTAMP)`, inv.SourceMessageID, inv.SessionID, request); err != nil {
		t.Fatal(err)
	}
	return skill.WithInvocationContext(prior, inv)
}

func TestGradeSkill_FailedPDFPreservesBodyAndRecoversWithoutSolve(t *testing.T) {
	deps, ctx, solver, renderer := reportDeliveryFixture(t, "report-failed", true, true)
	sk := skilladapter.NewGradeSkill(deps)
	result, err := sk.Execute(ctx, reportDeliveryArgs())
	if err != nil {
		t.Fatalf("known PDF failure must not discard the verified report: %v", err)
	}
	assertFullReport(t, result)
	if result.Metadata["print_artifacts"] != "" || !strings.Contains(result.Metadata["text_solve_refs"], `"pdf_status":"failed"`) || !strings.Contains(result.Metadata["text_solve_refs"], `"finalization_digest"`) {
		t.Fatalf("failed PDF must expose a recoverable identity without claiming a file: %v", result.Metadata)
	}
	persistReportDeliveryReply(t, deps, ctx, result)
	retryCtx := nextReportDeliveryTurn(t, deps, ctx, "report-recover-still-failed")
	retried, err := sk.Execute(retryCtx, map[string]any{"action": "prepare_report"})
	if err != nil || retried.Metadata["print_artifacts"] != "" {
		t.Fatalf("another known preparation failure must remain recoverable: %+v err=%v", retried, err)
	}
	persistReportDeliveryReply(t, deps, retryCtx, retried)
	renderer.fail = false
	recovered, err := sk.Execute(nextReportDeliveryTurn(t, deps, ctx, "report-recover"), map[string]any{"action": "prepare_report"})
	if err != nil {
		t.Fatal(err)
	}
	assertFullReport(t, recovered)
	reportRefs(t, recovered)
	if solver.calls != 1 || renderer.calls != 3 {
		t.Fatalf("render recovery repeated solving: solve=%d render=%d", solver.calls, renderer.calls)
	}
}

func TestGradeSkill_ResendReportUsesCurrentSessionFrozenBytes(t *testing.T) {
	deps, ctx, solver, renderer := reportDeliveryFixture(t, "report-resend", true, false)
	sk := skilladapter.NewGradeSkill(deps)
	first, err := sk.Execute(ctx, reportDeliveryArgs())
	if err != nil {
		t.Fatal(err)
	}
	refs := reportRefs(t, first)
	messageID := persistReportDeliveryReply(t, deps, ctx, first)
	next := nextReportDeliveryTurn(t, deps, ctx, "report-resend-next")
	resend, err := sk.Execute(next, map[string]any{"action": "resend_report"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reportRefs(t, resend); got[0] != refs[0] || solver.calls != 1 || renderer.calls != 1 {
		t.Fatalf("explicit resend must read the same bytes without generating: refs=%+v solve=%d render=%d", got, solver.calls, renderer.calls)
	}
	if resend.Metadata["print_reply_action"] != "resend_report" {
		t.Fatal("explicit file resend intent must reach the original channel projection")
	}
	var outboxCount int
	if err = deps.Records.DB().QueryRow(`SELECT COUNT(*) FROM outbox_events WHERE event_type=?`, k12.EventConversationPrintReply).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatalf("tool must hand the reference to the new turn, not dispatch itself: count=%d err=%v", outboxCount, err)
	}
	for _, bad := range []context.Context{
		skill.WithAuthenticatedUser(next, "different-user"),
		standaloneGradeContext(t, deps, "different-session"),
		skill.WithRoutedAgent(next, "mingming"),
	} {
		if _, err = sk.Execute(bad, map[string]any{"action": "resend_report", "report_message_id": messageID}); err == nil {
			t.Fatal("report references must not cross owner, session or routed agent")
		}
	}
	if solver.calls != 1 || renderer.calls != 1 {
		t.Fatal("rejected history requests must not call solve or render")
	}
}
