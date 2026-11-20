package main

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	k12engineadapter "github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

type countingSolveExecutor struct {
	calls         int
	verifiedCalls int
	ctx           context.Context
}

type interceptingCountingSolveExecutor struct {
	countingSolveExecutor
}

func (*interceptingCountingSolveExecutor) SupportsSubAgentCallInterceptor() bool {
	return true
}

func (e *countingSolveExecutor) GradeVerified(ctx context.Context, _, _, _ string) (*skill.Result, error) {
	e.verifiedCalls++
	e.ctx = ctx
	return &skill.Result{Content: "graded"}, nil
}

func (e *countingSolveExecutor) Execute(ctx context.Context, _ map[string]any) (*skill.Result, error) {
	e.calls++
	e.ctx = ctx
	return &skill.Result{Content: "ok"}, nil
}

// BUG-20260714：安全分类 wrapper 必须保留 GradeVerified 方法，否则生产 adapter 的类型断言
// 失败并把每道题从“solver+verifier+grader”退化为两套 solver+verifier 再 grader。
func TestBUG20260714_K12ClassifiedSolveForwardsVerifiedGradeFastPath(t *testing.T) {
	next := &countingSolveExecutor{}
	exec := classifiedSolveExecutor{next: next}
	res, err := exec.GradeVerified(context.Background(), "1+1", "答案：2", "2")
	if err != nil {
		t.Fatal(err)
	}
	if next.verifiedCalls != 1 || next.calls != 0 || res.Content != "graded" {
		t.Fatalf("verified=%d execute=%d res=%#v", next.verifiedCalls, next.calls, res)
	}
	got, _ := egress.RequestsFromContext(next.ctx)
	if len(got) != 2 || got[0].DataClass != egress.ClassGeneral || got[1].DataClass != egress.ClassSensitiveProfile {
		t.Fatalf("verified grade 出网标签错误: %+v", got)
	}
}

type subjectCountingSolveExecutor struct {
	countingSolveExecutor
	subject string
}

func (e *subjectCountingSolveExecutor) GradeVerifiedForSubject(ctx context.Context, subject, _, _, _ string) (*skill.Result, error) {
	e.subject, e.ctx = subject, ctx
	return &skill.Result{Metadata: map[string]string{"grade_verdict": "agree", "grade_correct": "true"}}, nil
}

func TestK12ClassifiedSolveForwardsFrozenSubjectToVerifiedGrader(t *testing.T) {
	next := &subjectCountingSolveExecutor{}
	grader := k12engineadapter.NewSolveAdapter(classifiedSolveExecutor{next: next})
	if _, err := grader.GradeVerified(t.Context(), "语文", "写一句邀请", "请坐。", "请坐。"); err != nil {
		t.Fatal(err)
	}
	requests, _ := egress.RequestsFromContext(next.ctx)
	if next.subject != "语文" || next.verifiedCalls != 0 || next.calls != 0 || len(requests) != 2 {
		t.Fatalf("wrong verified grade dispatch: subject=%q math=%d solve=%d classifications=%d", next.subject, next.verifiedCalls, next.calls, len(requests))
	}
}

// REG-P0: the production classification wrapper must preserve the capability
// method used by SolveAdapter to select the durable per-physical-call ledger.
// Erasing it silently falls back to the generic whole-operation sender.
func TestBUG20260803_K12ClassifiedSolveForwardsPhysicalCallCapability(t *testing.T) {
	next := &interceptingCountingSolveExecutor{}
	exec := classifiedSolveExecutor{next: next}
	adapter := k12engineadapter.NewSolveAdapter(exec)
	if !adapter.UsesGradingPhysicalCalls() {
		t.Fatal("production SolveAdapter lost the wrapped physical-call interceptor capability")
	}
}

func TestBUG20260710_K12SolveCarriesExplicitEgressClassification(t *testing.T) {
	next := &countingSolveExecutor{}
	exec := classifiedSolveExecutor{next: next}
	if _, err := exec.Execute(context.Background(), map[string]any{"problem": "1+1"}); err != nil {
		t.Fatal(err)
	}
	if next.calls != 1 {
		t.Fatalf("classified executor calls=%d", next.calls)
	}
	got, _ := egress.RequestsFromContext(next.ctx)
	if len(got) != 2 || got[0].Purpose != egress.PurposeSolveVerify || got[1].Purpose != egress.PurposeSolveVerify ||
		got[0].DataClass != egress.ClassGeneral || got[1].DataClass != egress.ClassSensitiveProfile {
		t.Fatalf("solve 出网标签错误: %+v", got)
	}
}
