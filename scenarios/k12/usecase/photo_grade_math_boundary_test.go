package usecase_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestGradeHomeworkPhoto_CompleteFractionSourcesUseExactLocalArithmetic(t *testing.T) {
	const q13Answer = `24÷\(\frac{3}{8}\)＝24×\(\frac{8}{3}\)` + "\n＝64\n答：这个数是64。"
	const q14Answer = `8×\(\frac{1}{4}\)×\(\frac{4}{5}\)` + "\n" + `＝2×\(\frac{4}{5}\)` + "\n" +
		`＝\(\frac{8}{5}\)＝1\(\frac{3}{5}\)` + "\n" + `答：是1\(\frac{3}{5}\)。`
	const q15Answer = `\(300\div2\div2=50\,(m)\)` + "\n" + `\(50\times2=100\,(m)\)` + "\n" +
		`\(50\times100=5000\,(m^2)\)` + "\n" + `\(5000\times2.25=11250\,(kg)\)` + "\n答：一共产鱼11250千克。"
	var worksheet bytes.Buffer
	if err := png.Encode(&worksheet, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, question, rawAnswer, projectedAnswer, wrongStep string
		wantStatus                                            usecase.PhotoItemStatus
	}{
		{"Q13 valid reciprocal", `一个数的\(\frac{3}{8}\)是24，求这个数？`, q13Answer,
			"24÷3/8＝24×8/3\n＝64\n答：这个数是64。", "", usecase.PhotoCorrect},
		{"Q14 mixed number across delimiters", `8的\(\frac{1}{4}\)的\(\frac{4}{5}\)是多少？`, q14Answer,
			"8×1/4×4/5\n＝2×4/5\n＝8/5＝1 3/5\n答：是1 3/5。", "", usecase.PhotoCorrect},
		{"Q13 wrong reciprocal retains process issue", `一个数的\(\frac{3}{8}\)是24，求这个数？`,
			`24÷\(\frac{3}{8}\)＝24×\(\frac{3}{8}\)` + "\n＝64\n答：这个数是64。",
			"24÷3/8＝24×3/8\n＝64\n答：这个数是64。", "24", usecase.PhotoCorrectWithProcessIssue},
		{"Q14 wrong intermediate retains process issue", `8的\(\frac{1}{4}\)的\(\frac{4}{5}\)是多少？`,
			strings.Replace(q14Answer, "＝2×", "＝3×", 1),
			"8×1/4×4/5\n＝3×4/5\n＝8/5＝1 3/5\n答：是1 3/5。", "3", usecase.PhotoCorrectWithProcessIssue},
		{"Q15 real process error remains", "一个周长是300米的长方形鱼塘，长是宽的2倍。如果每平方米产鱼2.25千克，一共产鱼多少千克？",
			q15Answer, "300÷2÷2=50 (m)\n50×2=100 (m)\n50×100=5000 (m²)\n5000×2.25=11250 (kg)\n答：一共产鱼11250千克。",
			"300÷2÷2", usecase.PhotoCorrectWithProcessIssue},
		{"Q16 real process error remains", "在下列六个数：5、6、12、14、23、29中划去数（ ）后，能使其中3个数的和为另外2个数和的2倍。",
			"划去：29\n因为：5+23+14=42\n6+12=18\n42=18×2", "划去：29\n因为：5+23+14=42\n6+12=18\n42=18×2",
			"42", usecase.PhotoCorrectWithProcessIssue},
		{"Q13 fraction literal remains a divisor", "一个数的3/8是24，求这个数？",
			"24÷3/8=24×8/3\n=64\n答：这个数是64。", "24÷3/8=24×8/3\n=64\n答：这个数是64。",
			"", usecase.PhotoCorrect},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// 保存响应替代视觉传输，解题和批改使用真实本地精确计算器。
			response, err := json.Marshal([]map[string]any{{
				"question": tt.question, "problem_kind": "standalone", "subject": "数学",
				"answer_state": "present", "student_answer": tt.rawAnswer, "recognition_confidence": .99,
			}})
			if err != nil {
				t.Fatal(err)
			}
			var savedResponseReads, modelCalls atomic.Int32
			recognizer := engineadapter.NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) {
				savedResponseReads.Add(1)
				return string(response), nil
			})
			solve := engineadapter.NewSolveAdapter(engine.NewSolveSkill(func(_ context.Context, spec engine.SubAgentSpec) (engine.SubAgentResult, error) {
				modelCalls.Add(1)
				return engine.SubAgentResult{}, fmt.Errorf("unexpected model invocation: %s", spec.Agent)
			}, nil))
			d := usecase.Deps{Recognizer: recognizer, Solver: solve, Grader: solve, VerifiedGrader: solve}
			got, err := d.GradeHomeworkPhoto(context.Background(), usecase.PhotoGradeRequest{
				AgentName: "math-boundary", Grade: "六年级上", Image: worksheet.Bytes(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Items) != 1 || got.Items[0].Status != tt.wantStatus {
				t.Fatalf("photo result = %#v, want one %s item", got.Items, tt.wantStatus)
			}
			item := got.Items[0]
			if item.Recognized.RawTranscription != tt.question || item.Recognized.AnswerRawTranscription != tt.rawAnswer {
				t.Fatal("recognition or grading changed original raw bytes")
			}
			if item.Recognized.StudentAnswer != tt.projectedAnswer || item.Recognized.AnswerCanonicalMarkdown != tt.projectedAnswer {
				t.Fatalf("answer projections = %q / %q, want %q", item.Recognized.StudentAnswer, item.Recognized.AnswerCanonicalMarkdown, tt.projectedAnswer)
			}
			if item.Grade.Evidence.EvidenceType != usecase.EvidenceNumericExec || item.Grade.Outcome.FinalAnswerCorrect == nil || !*item.Grade.Outcome.FinalAnswerCorrect {
				t.Fatalf("missing exact final-answer evidence: %#v", item.Grade)
			}
			if tt.wrongStep == "" {
				if item.Grade.Outcome.Verdict != usecase.VerdictAgree || item.Grade.Outcome.WrongStep != "" {
					t.Fatalf("correct work was rejected: %#v", item.Grade.Outcome)
				}
			} else if item.Grade.Outcome.Verdict != usecase.VerdictDisagree || !strings.Contains(item.Grade.Outcome.WrongStep, tt.wrongStep) {
				t.Fatalf("actual process error was lost: %#v", item.Grade.Outcome)
			}
			if savedResponseReads.Load() != 1 || modelCalls.Load() != 0 {
				t.Fatalf("saved response/model calls = %d/%d, want 1/0", savedResponseReads.Load(), modelCalls.Load())
			}
			if item.Grade.RecordCreated || item.Grade.RecordID != "" {
				t.Fatal("read-only arithmetic regression created a record")
			}
		})
	}
}

func TestPhotoGradeMixedNumber_BareFractionRemainsDifferent(t *testing.T) {
	var modelCalls atomic.Int32
	solve := engineadapter.NewSolveAdapter(engine.NewSolveSkill(func(_ context.Context, spec engine.SubAgentSpec) (engine.SubAgentResult, error) {
		modelCalls.Add(1)
		return engine.SubAgentResult{}, fmt.Errorf("unexpected model invocation: %s", spec.Agent)
	}, nil))
	const problem = "8的1/4的4/5是多少？"
	verified, err := solve.SolveSubject(context.Background(), "数学", problem, "六年级上", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		answer string
		want   usecase.Verdict
	}{
		{"1 3/5", usecase.VerdictAgree},
		{"13/5", usecase.VerdictDisagree},
	} {
		t.Run(tt.answer, func(t *testing.T) {
			got, err := solve.GradeVerified(context.Background(), "数学", problem, tt.answer, verified.Solution)
			if err != nil {
				t.Fatal(err)
			}
			if got.Verdict != tt.want || got.FinalAnswerCorrect == nil || *got.FinalAnswerCorrect != (tt.want == usecase.VerdictAgree) {
				t.Fatalf("grade = %#v, want %s", got, tt.want)
			}
		})
	}
	if modelCalls.Load() != 0 {
		t.Fatalf("model calls = %d, want 0", modelCalls.Load())
	}
}
