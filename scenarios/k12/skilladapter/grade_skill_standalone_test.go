package skilladapter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

type standaloneSolveFixture struct {
	calls   int
	unknown bool
	long    bool
}

func (*standaloneSolveFixture) FreezeReportSolveTeachingContext(context.Context, string, string) (string, []skill.ContextReceipt, error) {
	return "Explain the verified current problem completely.", nil, nil
}

func (s *standaloneSolveFixture) Solve(ctx context.Context, _, _, _ string) (usecase.SolveResult, error) {
	s.calls++
	if s.unknown {
		_, err := usecase.ExecuteGradingPhysicalCall(ctx, usecase.GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "standalone-unknown"}, func(context.Context) (string, error) {
			return "", context.DeadlineExceeded
		})
		return usecase.SolveResult{}, err
	}
	solution := "甲乙合作3天完成一半，乙再做7.5天完成另一半，因此乙每天完成1/15，甲每天完成1/6−1/15=1/10。\n答案：10天"
	sum := sha256.Sum256([]byte(solution))
	result := usecase.SolveResult{Solution: solution, Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNone, SolverOutputDigest: hex.EncodeToString(sum[:])}}
	if version, _ := usecase.SolveTeachingRequest(ctx); version == usecase.SolveOutputWithTeachingReportV2 {
		paragraph := "合作3天相当于两队各工作3天，剩余工作量由乙队继续完成；把工作量相加后再求甲队效率。"
		if s.long {
			paragraph = strings.Repeat(paragraph, 160)
		}
		guide := usecase.ParentTeachingGuide{Answer: "10天", FullSolutionSteps: []string{solution}, GradeLevelMethod: "用工作量和效率求时间。", LikelyMistakes: []string{"不要把合作时间当成甲的单独时间。"}, ParentTeachingSequence: []string{"先解释工作量，再列式。"}, FollowUpQuestions: []string{"怎样代回原条件检查？"}, CheckingMethod: "用甲乙每天完成的工作量代回两个条件。",
			ReportContent: &usecase.TeachingReportContentDraft{Version: usecase.TeachingReportContentContractVersion,
				Items:    []usecase.TeachingReportDraftItem{{ProblemID: "current", ShortTitle: "修公路", Answers: []usecase.TeachingReportDraftAnswer{{ID: "answer", Text: "10天"}}, Steps: []usecase.TeachingReportDraftStep{{ID: "calculation", Text: solution}}}},
				Sections: []usecase.TeachingReportDraftSection{{ID: "explanation", Title: "工作量与效率", ProblemIDs: []string{"current"}, Blocks: []usecase.TeachingReportDraftBlock{{ID: "explanation-body", ProblemIDs: []string{"current"}, Text: paragraph}}}},
			},
		}
		result.Generation = &usecase.SolveGeneration{OutputVersion: version, ParentGuideCandidate: &guide, GuideAudit: "VALID", SourceSolutionDigest: result.Evidence.SolverOutputDigest}
	}
	return result, nil
}

type standaloneReportResources struct{}

func (standaloneReportResources) ReadResource(name, path string) (marketplace.Resource, error) {
	fs := k12.BundledSkillsFS()
	raw, err := fs.ReadFile("skill-bundles/" + name + "/bundle.json")
	if err != nil {
		return marketplace.Resource{}, err
	}
	var manifest marketplace.BundleManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return marketplace.Resource{}, err
	}
	content, err := fs.ReadFile("skill-bundles/" + name + "/" + path)
	if err != nil {
		return marketplace.Resource{}, err
	}
	sum := sha256.Sum256(content)
	return marketplace.Resource{SkillID: name, Version: manifest.Version, Path: path, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Content: content}, nil
}

type standaloneReportRenderer struct{ calls int }

func (*standaloneReportRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	return nil, "", errors.New("unexpected legacy renderer")
}

func (r *standaloneReportRenderer) RenderTeachingReport(_ context.Context, _ render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	r.calls++
	// 此替身只验证完整报告交给渲染边界并保存原字节；真实PDF视觉由应用链路验收。
	raw, err := json.Marshal(input.Document)
	if err != nil {
		return nil, "", err
	}
	return append([]byte("%PDF-1.7\n"), raw...), "application/pdf", nil
}

func standaloneGradeContext(t *testing.T, deps usecase.Deps, request string) context.Context {
	t.Helper()
	db := deps.Records.DB()
	session, message := "standalone-session-"+request, "standalone-message-"+request
	if _, err := db.Exec(`INSERT INTO sessions(id,user_id,platform,created_at,updated_at) VALUES(?,?,'dingtalk',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, session, "dingtalk-staff"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES(?,?,'user',?,?,CURRENT_TIMESTAMP)`, message, session, "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？", request); err != nil {
		t.Fatal(err)
	}
	ctx := skill.WithAuthenticatedUser(context.Background(), "dingtalk-staff")
	ctx = skill.WithRoutedAgent(ctx, "default")
	return skill.WithInvocationContext(ctx, skill.InvocationContext{AuthenticatedUserID: "dingtalk-staff", RoutedAgentName: "default", SessionID: session, SourceMessageID: message, RequestID: request})
}

func assertStandaloneStudentState(t *testing.T, deps usecase.Deps) {
	t.Helper()
	for _, q := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM agents`, 1},
		{`SELECT COUNT(*) FROM agents WHERE name='default'`, 0},
		{`SELECT COUNT(*) FROM k12_mistakes`, 0},
		{`SELECT COUNT(*) FROM k12_grading_jobs`, 0},
	} {
		var got int
		if err := deps.Records.DB().QueryRow(q.query).Scan(&got); err != nil || got != q.want {
			t.Fatalf("student state changed: %s got=%d want=%d err=%v", q.query, got, q.want, err)
		}
	}
}

func TestGradeSkill_StandaloneTextSolveKeepsDurableIdentity(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{})
	deps.TextbookOwnerID = "application-owner"
	solver := &standaloneSolveFixture{}
	deps.Solver = solver
	ctx := standaloneGradeContext(t, deps, "standalone-replay")
	sk := skilladapter.NewGradeSkill(deps)
	args := map[string]any{"problem": "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？", "subject": "数学"}
	first, err := sk.Execute(ctx, args)
	if err != nil || !strings.Contains(first.Content, "10天") || first.Metadata["text_solve_run_id"] == "" {
		t.Fatalf("standalone solve failed: result=%+v err=%v", first, err)
	}
	replayed, err := sk.Execute(ctx, args)
	if err != nil || replayed.Content != first.Content || replayed.Metadata["text_solve_run_id"] != first.Metadata["text_solve_run_id"] || solver.calls != 1 {
		t.Fatalf("same request reran or changed result: calls=%d err=%v", solver.calls, err)
	}
	final, err := deps.Records.GetTextSolveFinalization(ctx, "application-owner", "default", first.Metadata["text_solve_run_id"])
	if err != nil || final.SourceMessageID != skill.CurrentInvocation(ctx).SourceMessageID {
		t.Fatalf("durable result lost its authenticated source: %+v %v", final, err)
	}
	assertStandaloneStudentState(t, deps)
	wrongUser := skill.WithAuthenticatedUser(ctx, "another-staff")
	if _, err := sk.Execute(wrongUser, args); !errors.Is(err, k12storage.ErrTextSolveConflict) || solver.calls != 1 {
		t.Fatalf("another user reused the original execution: calls=%d err=%v", solver.calls, err)
	}
}

func TestGradeSkill_StandaloneUnknownDoesNotResend(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{})
	deps.TextbookOwnerID = "application-owner"
	solver := &standaloneSolveFixture{unknown: true}
	deps.Solver = solver
	deps.TextSolveRoute = func(context.Context, string) (k12.GradingModelSnapshot, error) {
		return k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture"}, nil
	}
	ctx := standaloneGradeContext(t, deps, "standalone-unknown")
	sk := skilladapter.NewGradeSkill(deps)
	for i := 0; i < 2; i++ {
		if _, err := sk.Execute(ctx, map[string]any{"problem": "未知结果的原题"}); !errors.Is(err, k12storage.ErrTextSolveUnknown) {
			t.Fatalf("attempt %d must preserve unknown: %v", i, err)
		}
	}
	if solver.calls != 1 {
		t.Fatalf("unknown upstream request repeated %d times", solver.calls)
	}
	var unknown int
	if err := deps.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_text_solve_invocations WHERE status='outcome_unknown'`).Scan(&unknown); err != nil || unknown != 1 {
		t.Fatalf("unknown receipt not durable: count=%d err=%v", unknown, err)
	}
	assertStandaloneStudentState(t, deps)
}

func TestGradeSkill_StandaloneUnpinnedRequestUsesItsOwnIdentity(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{})
	deps.TextbookOwnerID = "application-owner"
	deps.Solver = &standaloneSolveFixture{}
	invocation := skill.CurrentInvocation(standaloneGradeContext(t, deps, "standalone-unpinned"))
	invocation.RoutedAgentName = ""
	ctx := skill.WithInvocationContext(skill.WithAuthenticatedUser(context.Background(), "dingtalk-staff"), invocation)
	result, err := skilladapter.NewGradeSkill(deps).Execute(ctx, map[string]any{"problem": "求公路工程中甲队的单独工期"})
	if err != nil || result.Metadata["text_solve_run_id"] == "" || !strings.Contains(result.Content, "10天") {
		t.Fatalf("default conversation without a pinned agent failed: %+v %v", result, err)
	}
	if _, err := deps.Records.GetTextSolveFinalization(ctx, "application-owner", "default", result.Metadata["text_solve_run_id"]); err != nil {
		t.Fatal(err)
	}
	assertStandaloneStudentState(t, deps)
}

func TestGradeSkill_StandaloneSubmittedAnswerDoesNotCreateStudentRecords(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{Verdict: usecase.VerdictDisagree, WrongStep: "把合作时间当成单独时间", ErrorCause: "没有区分两队效率", KnowledgePoint: "工程问题"})
	deps.TextbookOwnerID = "application-owner"
	deps.Solver = &standaloneSolveFixture{}
	ctx := standaloneGradeContext(t, deps, "standalone-assessment")
	result, err := skilladapter.NewGradeSkill(deps).Execute(ctx, map[string]any{"problem": "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？", "student_answer": "6天"})
	if err != nil || !strings.Contains(result.Content, "10天") || !strings.Contains(result.Content, "没有区分两队效率") || result.Metadata["k12_record_created"] != "false" {
		t.Fatalf("standalone assessment must return the correction without learner records: %+v %v", result, err)
	}
	assertStandaloneStudentState(t, deps)
}

func TestGradeSkill_RegisteredLearnerStillRecordsSubmittedMistake(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{Verdict: usecase.VerdictDisagree, WrongStep: "混淆合作与单独时间", ErrorCause: "没有分别求效率", KnowledgePoint: "工程问题"})
	deps.TextbookOwnerID = "application-owner"
	deps.Solver = &standaloneSolveFixture{}
	if _, err := deps.Records.DB().Exec(`UPDATE agents SET metadata='{"scenario":"k12-tutor"}' WHERE name='mingming'`); err != nil {
		t.Fatal(err)
	}
	ctx := skill.WithRoutedAgent(standaloneGradeContext(t, deps, "registered-assessment"), "mingming")
	result, err := skilladapter.NewGradeSkill(deps).Execute(ctx, map[string]any{"problem": "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？", "student_answer": "6天"})
	if err != nil || result.Metadata["k12_record_created"] != "true" {
		t.Fatalf("registered learner lost the original mistake projection: %+v %v", result, err)
	}
	var count int
	if err := deps.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_mistakes WHERE agent_name='mingming'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("mistake did not remain in the routed learner: count=%d err=%v", count, err)
	}
}

func TestGradeSkill_StandaloneLongReportKeepsPDFSource(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{})
	deps.TextbookOwnerID = "application-owner"
	solver, renderer := &standaloneSolveFixture{long: true}, &standaloneReportRenderer{}
	deps.Solver, deps.Renderer, deps.TeachingReportResources = solver, renderer, standaloneReportResources{}
	ctx := standaloneGradeContext(t, deps, "standalone-pdf")
	sk := skilladapter.NewGradeSkill(deps)
	args := map[string]any{"problem": "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？", "subject": "数学", "grade": "六年级上"}
	first, err := sk.Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	var refs []k12.PrintReplyArtifact
	if err = json.Unmarshal([]byte(first.Metadata["print_artifacts"]), &refs); err != nil || len(refs) != 1 {
		t.Fatalf("long report did not produce one frozen PDF reference: %v %v", first.Metadata, err)
	}
	view, err := deps.GetPrintableArtifactPDF(ctx, "default", refs[0].ArtifactID)
	if err != nil || !strings.Contains(view.Artifact.CanonicalMarkdown, "10天") || len(view.Render.Payload) < 4000 || view.Artifact.SourceKind != k12.PrintSourceTextSolveFinalization {
		t.Fatalf("PDF source or full content missing: bytes=%d err=%v", len(view.Render.Payload), err)
	}
	replayed, err := sk.Execute(ctx, args)
	if err != nil || replayed.Metadata["print_artifacts"] != first.Metadata["print_artifacts"] || solver.calls != 1 || renderer.calls != 1 {
		t.Fatalf("same source rerendered or solved again: solve=%d render=%d err=%v", solver.calls, renderer.calls, err)
	}
	if _, err = deps.GetPrintableArtifactPDF(ctx, "mingming", refs[0].ArtifactID); err == nil {
		t.Fatal("student scope read a standalone report")
	}
	if _, err = deps.Records.DB().Exec(`UPDATE k12_print_artifacts SET canonical_markdown='changed' WHERE artifact_id=?`, refs[0].ArtifactID); err == nil {
		t.Fatal("PDF artifact became mutable")
	}
	assertStandaloneStudentState(t, deps)
}
