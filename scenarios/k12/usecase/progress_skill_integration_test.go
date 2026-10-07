package usecase_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

type autoProgressHomeworkSolver struct{ calls int }

func (s *autoProgressHomeworkSolver) Solve(context.Context, string, string, string) (usecase.SolveResult, error) {
	s.calls++
	return usecase.SolveResult{Solution: "## 答案\n5", Evidence: usecase.SolveEvidence{
		Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec,
	}}, nil
}

func TestProgressSkillIntegration_ExplicitParentCorrectionUsesCASWithoutLearningWrites(t *testing.T) {
	d, db, _ := newAutoProgressFixture(t)
	ctx := context.Background()
	estimated, err := d.EnsureCurriculumProgress(ctx, "desktop-user", "auto-child")
	if err != nil || estimated == nil || estimated.EvidenceSource != "ai_estimated" {
		t.Fatalf("estimate=%+v err=%v", estimated, err)
	}
	tool := skilladapter.NewProgressSkill(d)
	input := skill.WithOriginalUserText(skill.WithRoutedAgent(ctx, "auto-child"), "请把进度改为第一单元。")
	result, err := tool.Execute(input, map[string]any{"unit_id": "u2", "confirmed": false})
	if err != nil || result == nil {
		t.Fatalf("parent correction result=%+v err=%v", result, err)
	}
	p, err := d.GetCurriculumProgress(ctx, "auto-child", "math")
	if err != nil || p.UnitID != "u1" || p.EvidenceSource != "parent_confirmed" || p.ConfirmedAt <= 0 || p.Revision != estimated.Revision+1 {
		t.Fatalf("stored correction=%+v err=%v", p, err)
	}
	var learning int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM k12_mistakes)+(SELECT count(*) FROM k12_attempts)+(SELECT count(*) FROM k12_mistake_review_states)+(SELECT count(*) FROM k12_grading_assessment_items)`).Scan(&learning); err != nil || learning != 0 {
		t.Fatalf("progress correction fabricated learning: count=%d err=%v", learning, err)
	}
}

func TestProgressSkillIntegration_MissingCatalogDoesNotBlockActualHomework(t *testing.T) {
	d, db, _ := newAutoProgressFixture(t)
	ctx := context.Background()
	if _, err := d.EnsureCurriculumProgress(ctx, "desktop-user", "auto-child"); err != nil {
		t.Fatal(err)
	}
	directive, err := d.TutorCurriculumDirective(ctx, "auto-child")
	if err != nil || !strings.Contains(directive, "Estimated course recommendation") || !strings.Contains(directive, "Never reject or limit") || strings.Contains(directive, "Confirmed course context") {
		t.Fatalf("estimate misrepresented: directive=%s err=%v", directive, err)
	}
	if _, err := db.Exec(`UPDATE k12_textbook_manifests SET state='failed_retryable',retryable=1`); err != nil {
		t.Fatal(err)
	}
	_, before, err := d.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil {
		t.Fatal(err)
	}
	tool := skilladapter.NewProgressSkill(d)
	input := skill.WithOriginalUserText(skill.WithRoutedAgent(ctx, "auto-child"), "老师讲到第一单元")
	if result, err := tool.Execute(input, nil); err == nil || result != nil {
		t.Fatalf("unavailable catalog claimed correction: result=%+v err=%v", result, err)
	}
	_, after, err := d.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil || after != before {
		t.Fatalf("failed correction wrote progress: before=%d after=%d err=%v", before, after, err)
	}
	solver := &autoProgressHomeworkSolver{}
	d.Solver = solver
	actual, err := d.SolveHomeworkProblem(ctx, usecase.GradeRequest{AgentName: "auto-child", Subject: "数学", Grade: "五年级上", Problem: "2.5×2=?", KnowledgePoints: []string{"小数乘法"}})
	if err != nil || actual.OutOfScope || actual.Solution == "" || solver.calls != 1 {
		t.Fatalf("actual homework blocked by estimate/catalog: result=%+v calls=%d err=%v", actual, solver.calls, err)
	}
	var learning int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM k12_mistakes)+(SELECT count(*) FROM k12_attempts)+(SELECT count(*) FROM k12_mistake_review_states)`).Scan(&learning); err != nil || learning != 0 {
		t.Fatalf("blank homework fabricated learning: count=%d err=%v", learning, err)
	}
}
