package skilladapter_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 空白题不要求学生答案；历史报告动作无须重传题干，默认批改仍在运行时校验题干。
func TestGradeSkill_StudentAnswerOptional(t *testing.T) {
	sk := skilladapter.NewGradeSkill(usecase.Deps{})
	req := sk.ToolDefinition().Function.Parameters.Required
	for _, r := range req {
		if r == "student_answer" {
			t.Fatalf("student_answer 不应再必填（空白题要能不填走解题）: required=%v", req)
		}
	}
	for _, r := range req {
		if r == "problem" {
			t.Errorf("report actions must not require a new problem: required=%v", req)
		}
	}
	ctx := skill.WithRoutedAgent(context.Background(), "mingming")
	if _, err := sk.Execute(ctx, map[string]any{}); err == nil {
		t.Error("default grade action must still require the problem")
	}
}

// TestGradeSkill_BlankSolves 空白题（不传 student_answer）→ 解题分叉，返回解法、不入错题本。
func TestGradeSkill_BlankSolves(t *testing.T) {
	deps := newDeps(t, usecase.GradeOutcome{}) // grader 不会被调用
	sk := skilladapter.NewGradeSkill(deps)
	ctx := skill.WithRoutedAgent(context.Background(), "mingming")

	res, err := sk.Execute(ctx, map[string]any{"problem": "3.8 × 3 = ?"}) // 无 student_answer
	if err != nil {
		t.Fatalf("空白题不应报错: %v", err)
	}
	if !strings.Contains(res.Content, "还没作答") {
		t.Errorf("空白题应走解题口径, got %q", res.Content)
	}
	if res.Metadata["k12_record_created"] == "true" {
		t.Error("空白题不应入错题本")
	}
}
