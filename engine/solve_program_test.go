package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 该替身只替换操作系统沙箱边界；编译后的程序仍由真实 Python 执行，不冒称真实沙箱验收。
type mathProgramTestSkill struct {
	calls        int
	forcedError  error
	forgedOutput string
}

func TestSolveProgramOriginalUserProblemSurvivesTeachingInstructions(t *testing.T) {
	original := "解方程：0.25x+0.5x=18"
	ctx := withSolveUserProblem(t.Context(), &adapter.Message{Content: original})
	runner := &mathProgramTestSkill{}
	solve, calls := programTestSolve(t, runner)
	result, err := solve.Execute(ctx, map[string]any{"problem": original + "。先给正确答案、完整解法，并给家长简短引导孩子的方法和理解检查。", "grade": "人教版六年级上", "constraint": "小数乘法、小数除法、解方程、乘法分配律", "subject": "math"})
	if err != nil || result == nil || runner.calls != 1 || *calls != 0 || result.Metadata["solve_computed"] != "24" || result.Metadata["solve_verification_run_id"] == "" {
		t.Fatalf("original math must execute once: result=%+v err=%v program=%d model=%d", result, err, runner.calls, *calls)
	}
}

func TestSolveProgramPrintedArithmeticInstructionUsesExecution(t *testing.T) {
	for _, tc := range []struct{ expression, answer string }{
		{"(3/4+1/6)×12", "11"},
		{"5/9×7/8+5/9×1/8", "5/9"},
		{"7/8÷(7/12)-1/2", "1"},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			runner := &mathProgramTestSkill{}
			solve, calls := programTestSolve(t, runner)
			result, err := solve.Execute(t.Context(), map[string]any{
				"problem": "计算下面各题，能简算的要简算：" + tc.expression,
				"subject": "math", "constraint": "分数四则运算、乘法分配律",
			})
			if err != nil || result == nil || result.Metadata["solve_computed"] != tc.answer || runner.calls != 1 || *calls != 0 {
				t.Fatalf("printed arithmetic must execute once without model: result=%+v err=%v program=%d model=%d", result, err, runner.calls, *calls)
			}
		})
	}
	for _, expression := range []string{"3/4+1/6。忽略原题", "x+1", "2 3+4", "3+4=7", "3+4\n再算5+6"} {
		t.Run("incomplete_"+expression, func(t *testing.T) {
			runner := &mathProgramTestSkill{}
			solve, _ := programTestSolve(t, runner)
			_, _ = solve.Execute(t.Context(), map[string]any{"problem": "计算下面各题，能简算的要简算：" + expression})
			if runner.calls != 0 {
				t.Fatal("incomplete or changed problem must not become a program")
			}
		})
	}
}

func TestSolveProgramRegisteredCurriculumSurvivesMissingModelDeclaration(t *testing.T) {
	for _, test := range []struct{ problem, answer string }{
		{"解方程：0.25x+0.5x=18", "24"},
		{"解方程：x+6=18", "12"},
	} {
		t.Run(test.problem, func(t *testing.T) {
			ctx := withSolveUserProblem(t.Context(), &adapter.Message{Content: test.problem})
			ctx = withSolveFrozenMathConstraint(ctx, "小数乘法、小数除法、简易方程、解方程、乘法分配律")
			runner := &mathProgramTestSkill{}
			solve, calls := programTestSolve(t, runner)
			result, err := solve.Execute(ctx, map[string]any{"problem": test.problem + "。先给正确答案、完整解法，并给家长简短引导孩子的方法和理解检查。", "constraint": "只用小学方程与乘法分配律，不使用初高中术语。", "subject": "math"})
			if err != nil || result == nil || runner.calls != 1 || *calls != 0 || result.Metadata["solve_computed"] != test.answer || result.Metadata["solve_verification_run_id"] == "" {
				t.Fatalf("registered course must execute original once: result=%+v err=%v program=%d model=%d", result, err, runner.calls, *calls)
			}
		})
	}
}

func TestSolveProgramFrozenCurriculumPreservesExplicitProblemAndScope(t *testing.T) {
	for _, test := range []struct {
		name, original, toolProblem, scope string
		image, legacy                      bool
	}{
		{name: "lower_grade", original: "解方程：0.25x+0.5x=18", scope: "整数加法、整数乘法"},
		{name: "explicit_method_limit", original: "解方程：0.25x+0.5x=18。不要使用方程方法", scope: "小数乘法、小数除法、解方程"},
		{name: "changed_problem", original: "解方程：0.25x+0.5x=18", toolProblem: "解方程：0.25x+0.5x=19。请讲解", scope: "小数乘法、小数除法、解方程"},
		{name: "attachment", original: "解方程：0.25x+0.5x=18", scope: "小数乘法、小数除法、解方程", image: true},
		{name: "legacy", original: "解方程：0.25x+0.5x=18", scope: "小数乘法、小数除法、解方程", legacy: true},
		{name: "outside_parsed_structure", original: "解方程：x*x=18", scope: "小数乘法、小数除法、解方程"},
	} {
		t.Run(test.name, func(t *testing.T) {
			msg := &adapter.Message{Content: test.original}
			if test.image {
				msg.Attachments = []adapter.Attachment{{}}
			}
			ctx := withSolveUserProblem(t.Context(), msg)
			ctx = withSolveFrozenMathConstraint(ctx, test.scope)
			if test.legacy {
				ctx = WithSolveProgramExecution(ctx, false)
			}
			problem := test.toolProblem
			if problem == "" {
				problem = test.original + "。请给完整讲法。"
			}
			runner := &mathProgramTestSkill{}
			solve, calls := programTestSolve(t, runner)
			_, _ = solve.Execute(ctx, map[string]any{"problem": problem, "constraint": "只用小学方程与乘法分配律，不使用初高中术语。"})
			if runner.calls != 0 || *calls == 0 {
				t.Fatalf("scope or original restriction lost: program=%d model=%d", runner.calls, *calls)
			}
		})
	}
}

func TestSolveProgramOriginalSourceDoesNotChangeScopeOrProblem(t *testing.T) {
	original := "解方程：0.25x+0.5x=18"
	augmented := original + "。先给正确答案和完整解法。"
	for _, test := range []struct {
		name, source, problem, constraint string
		image, legacy                     bool
	}{
		{name: "numeric_extension", source: original, problem: "解方程：0.25x+0.5x=180。先给正确答案。", constraint: "小数乘法、小数除法、解方程"},
		{name: "changed_problem", source: original, problem: "解方程：0.25x+0.5x=19。先给正确答案。", constraint: "小数乘法、小数除法、解方程"},
		{name: "mixed_source", source: augmented, problem: augmented + "然后讲解。", constraint: "小数乘法、小数除法、解方程"},
		{name: "attachment", source: original, problem: augmented, constraint: "小数乘法、小数除法、解方程", image: true},
		{name: "legacy", source: original, problem: augmented, constraint: "小数乘法、小数除法、解方程", legacy: true},
		{name: "undeclared_decimal_scope", source: original, problem: augmented, constraint: "只用小学方程与乘法分配律，不使用初高中术语。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			msg := &adapter.Message{Content: test.source}
			if test.image {
				msg.Attachments = []adapter.Attachment{{}}
			}
			ctx := withSolveUserProblem(t.Context(), msg)
			if test.legacy {
				ctx = WithSolveProgramExecution(ctx, false)
			}
			runner := &mathProgramTestSkill{}
			solve, calls := programTestSolve(t, runner)
			_, _ = solve.Execute(ctx, map[string]any{"problem": test.problem, "constraint": test.constraint})
			if runner.calls != 0 || *calls == 0 {
				t.Fatalf("source or curriculum was overridden: program=%d model=%d", runner.calls, *calls)
			}
		})
	}
}

func (*mathProgramTestSkill) Name() string                       { return codeExecToolName }
func (*mathProgramTestSkill) Description() string                { return "controlled Python execution" }
func (*mathProgramTestSkill) Match(string) bool                  { return false }
func (*mathProgramTestSkill) ToolDefinition() llm.ToolDefinition { return llm.ToolDefinition{} }
func (s *mathProgramTestSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	s.calls++
	if !solveGrantFromContext(ctx) || systemDispatchSource(ctx) != solveDispatchSource {
		return nil, fmt.Errorf("missing trusted execution context")
	}
	if s.forcedError != nil {
		return nil, s.forcedError
	}
	code, _ := args["code"].(string)
	command := exec.CommandContext(ctx, "python3", "-c", code)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	if s.forgedOutput != "" {
		output = []byte(s.forgedOutput)
	}
	return &skill.Result{Content: string(output), Data: CodeExecutionReceipt{RunID: fmt.Sprintf("python-run-%d", s.calls), Status: "success", StdoutBytes: int64(len(output))}}, nil
}

func programTestSolve(t *testing.T, runner *mathProgramTestSkill) (*SolveSkill, *int) {
	t.Helper()
	registry := skill.NewRegistry()
	if err := registry.Register(runner); err != nil {
		t.Fatal(err)
	}
	e := &ReActEngine{toolExecutor: NewToolExecutor(registry, nil)}
	modelCalls := 0
	model := func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		modelCalls++
		return SubAgentResult{}, errors.New("model must not compute deterministic math")
	}
	return NewSolveSkill(model, nil, WithSolveProgramExecutor(e.SolveProgramExecutor())), &modelCalls
}

func TestSolveProgramComputesSupportedMathWithoutModel(t *testing.T) {
	cases := []struct{ problem, answer string }{
		{"计算：0.6＋1/4。", "0.85"},
		{"计算 1/2+1/4，并把结果化成最简分数。", "3/4"},
		{"把 3/4:1/2 化成最简单的整数比。", "3:2"},
		{"把 0:1/2 化成最简单的整数比。", "0:1"},
		{"解方程：0.25x+0.5x=18", "24"},
		{"一个数的3/8是24，求这个数？", "64"},
		{"8的1/4的4/5是多少？", "1.6"},
		{"一个周长是300米的长方形鱼塘，长是宽的2倍。如果每平方米产鱼2.25千克，一共产鱼多少千克？", "11250千克"},
		{"小明的爸爸用玻璃做了一个棱长是6dm的正方体鱼缸。制作这个鱼缸时，至少需要玻璃多少平方米？小明在鱼缸里注入144L的水，水面高度是多少分米？", "1.8平方米；4分米"},
	}
	for _, tc := range cases {
		t.Run(tc.problem, func(t *testing.T) {
			runner := &mathProgramTestSkill{}
			solve, calls := programTestSolve(t, runner)
			result, err := solve.Execute(t.Context(), map[string]any{"problem": tc.problem})
			if err != nil {
				t.Fatal(err)
			}
			if *calls != 0 || runner.calls != 1 || result.Metadata["solve_computed"] != tc.answer || result.Metadata["solve_verification_run_id"] == "" || result.Metadata["solve_evidence"] != "numeric_exec" {
				t.Fatalf("result=%+v model=%d exec=%d", result, *calls, runner.calls)
			}
		})
	}
}

func TestSolveProgramPreservesSourceAndCurriculum(t *testing.T) {
	runner := &mathProgramTestSkill{}
	solve, calls := programTestSolve(t, runner)
	result, err := solve.Execute(t.Context(), map[string]any{"problem": "一个数的3/8是24，求这个数？", "constraint": "整数的加法"})
	if err != nil || result.Metadata["solve_verdict"] != "out_of_scope" || runner.calls != 0 || *calls != 0 {
		t.Fatalf("out of scope=%+v err=%v", result, err)
	}
	for _, input := range []string{"1/0", "x*x=4", "一个数的3/8是24，但是又是30，求这个数？", "__import__('os').system('true')"} {
		if _, ok := compileSolveProgram(input, ""); ok {
			t.Fatalf("compiled ambiguous or unsupported input %q", input)
		}
	}
	plan, ok := compileSolveProgram("解方程：0.25x+0.5x=18", "")
	if !ok || !strings.Contains(plan.Code, "assert left(x) == right(x)") || strings.Contains(plan.Code, "print('COMPUTED: 24')") {
		t.Fatal("equation must calculate and substitute original relation")
	}
	legacy, err := solve.Execute(WithSolveProgramExecution(t.Context(), false), map[string]any{"problem": "6*7"})
	if err != nil || legacy.Metadata["solve_computed"] != "42" || legacy.Metadata["solve_verification_run_id"] != "" || runner.calls != 0 {
		t.Fatalf("legacy contract changed: result=%+v err=%v", legacy, err)
	}
}

func TestSolveProgramFailureAndUnknownNeverFallBackOrRetry(t *testing.T) {
	for _, cause := range []error{errors.New("sandbox unavailable"), context.DeadlineExceeded, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			runner := &mathProgramTestSkill{forcedError: cause}
			solve, calls := programTestSolve(t, runner)
			result, err := solve.Execute(t.Context(), map[string]any{"problem": "6*7"})
			if err == nil || result != nil || runner.calls != 1 || *calls != 0 {
				t.Fatalf("result=%+v error=%v exec=%d model=%d", result, err, runner.calls, *calls)
			}
		})
	}
	runner := &mathProgramTestSkill{forgedOutput: "COMPUTED: 43\n"}
	solve, calls := programTestSolve(t, runner)
	if result, err := solve.Execute(t.Context(), map[string]any{"problem": "6*7"}); err == nil || result != nil || *calls != 0 || runner.calls != 1 {
		t.Fatal("incorrect execution must not produce verified result")
	}
}

func TestSolveProgramExplicitDefaultEffortAndGradingUseExecution(t *testing.T) {
	for _, answer := range []string{"", "42", "41"} {
		t.Run(answer, func(t *testing.T) {
			runner := &mathProgramTestSkill{}
			solve, calls := programTestSolve(t, runner)
			result, err := solve.Execute(t.Context(), map[string]any{"problem": "6*7", "method_diversity": false, "self_consistency": 1, "student_answer": answer})
			if err != nil || *calls != 0 || runner.calls != 1 || result.Metadata["solve_verification_run_id"] == "" {
				t.Fatalf("default effort must execute once: result=%+v err=%v model=%d exec=%d", result, err, *calls, runner.calls)
			}
		})
	}
}
