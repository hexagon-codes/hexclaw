package engineadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const printedPracticeSavedResponse = `[
  {
    "problem_id": "p1",
    "problem_kind": "compound_parent",
    "parent_problem_id": "",
    "subproblem_no": "",
    "source_number_path": ["二", "1"],
    "display_label": "二、1",
    "source_section_path": ["二"],
    "source_section_label": "二 分数乘法",
    "question": "环保小组用回收的包装纸做纸花，做一朵花需要用3/8张纸。",
    "canonical_markdown": "环保小组用回收的包装纸做纸花，做一朵花需要用 $\\frac{3}{8}$ 张纸。",
    "subject": "数学",
    "knowledge_points": ["分数乘整数"],
    "answer_state": "blank",
    "student_answer": "",
    "answer_canonical_markdown": "",
    "recognition_confidence": 0.99,
    "ocr_signals": ["fraction"]
  },
  {
    "problem_id": "p2",
    "problem_kind": "subproblem",
    "parent_problem_id": "p1",
    "subproblem_no": "1",
    "source_number_path": ["二", "1", "1"],
    "display_label": "二、1、（1）",
    "source_section_path": ["二"],
    "source_section_label": "二 分数乘法",
    "question": "（1）做2朵花需要用多少张纸？\n3/8×2=____",
    "canonical_markdown": "（1）做2朵花需要用多少张纸？\n\n$\\frac{3}{8}\\times 2=\\underline{\\qquad}$",
    "subject": "数学",
    "knowledge_points": ["分数乘整数", "分数乘法的意义"],
    "answer_state": "blank",
    "student_answer": "",
    "answer_canonical_markdown": "",
    "recognition_confidence": 0.99,
    "ocr_signals": ["fraction"]
  },
  {
    "problem_id": "p3",
    "problem_kind": "subproblem",
    "parent_problem_id": "p1",
    "subproblem_no": "2",
    "source_number_path": ["二", "1", "2"],
    "display_label": "二、1、（2）",
    "source_section_path": ["二"],
    "source_section_label": "二 分数乘法",
    "question": "（2）做8朵花需要用多少张纸？",
    "canonical_markdown": "（2）做8朵花需要用多少张纸？",
    "subject": "数学",
    "knowledge_points": ["分数乘整数", "约分"],
    "answer_state": "blank",
    "student_answer": "",
    "answer_canonical_markdown": "",
    "recognition_confidence": 0.99,
    "ocr_signals": []
  },
  {
    "problem_id": "p4",
    "problem_kind": "standalone",
    "parent_problem_id": "",
    "subproblem_no": "",
    "source_number_path": [],
    "display_label": "",
    "source_section_path": ["二"],
    "source_section_label": "二 分数乘法",
    "question": "讨论一下：分数乘整数，怎样计算？",
    "canonical_markdown": "讨论一下：分数乘整数，怎样计算？",
    "subject": "数学",
    "knowledge_points": ["分数乘整数的计算法则"],
    "answer_state": "blank",
    "student_answer": "",
    "answer_canonical_markdown": "",
    "recognition_confidence": 0.99,
    "ocr_signals": []
  },
  {
    "problem_id": "p5",
    "problem_kind": "standalone",
    "parent_problem_id": "",
    "subproblem_no": "",
    "source_number_path": [],
    "display_label": "",
    "source_section_path": ["二"],
    "source_section_label": "二 分数乘法\n做一做",
    "question": "一袋面包重3/10 kg，3袋重多少千克？\n□×□=□（kg）",
    "canonical_markdown": "一袋面包重 $\\frac{3}{10}\\,\\mathrm{kg}$，3袋重多少千克？\n\n$\\square\\times\\square=\\square\\;(\\mathrm{kg})$",
    "subject": "数学",
    "knowledge_points": ["分数乘整数", "分数乘法应用题"],
    "answer_state": "blank",
    "student_answer": "",
    "answer_canonical_markdown": "",
    "recognition_confidence": 0.99,
    "ocr_signals": ["fraction", "unit"]
  }
]`

func assertPrintedRecognitionRules(t *testing.T, prompt string) {
	t.Helper()
	for _, rule := range []string{
		"Printed worked examples, printed answers, coloured cancellation marks and printed solution steps",
		"never student_answer",
		"If there is no actual student handwriting, use answer_state=blank",
		"genuine student handwriting must still be recovered",
		"For the same source_section_path, use the same numbered heading",
		"“做一做”",
		"must not be appended to that heading",
	} {
		if !strings.Contains(prompt, rule) {
			t.Fatalf("actual vision prompt is missing ownership/heading rule %q", rule)
		}
	}
}

func TestRecognizerPrintedPracticeSavedResponsePreservesSourceFacts(t *testing.T) {
	calls := 0
	recognizer := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
		calls++
		assertPrintedRecognitionRules(t, prompt)
		return printedPracticeSavedResponse, nil
	})
	questions, err := recognizer.Recognize(context.Background(), []byte{1})
	if err != nil {
		t.Fatalf("saved complete response must pass the recognition adapter: %v", err)
	}
	if calls != 1 || len(questions) != 5 {
		t.Fatalf("vision calls=%d questions=%d, want 1/5", calls, len(questions))
	}
	if questions[0].RawTranscription != "环保小组用回收的包装纸做纸花，做一朵花需要用3/8张纸。" ||
		questions[4].RawTranscription != "一袋面包重3/10 kg，3袋重多少千克？\n□×□=□（kg）" {
		t.Fatalf("saved source transcription changed: %#v", questions)
	}
	if questions[4].SourceSectionLabel != "二 分数乘法\n做一做" {
		t.Fatal("adapter must retain the original response facts before new-input normalization")
	}
	normalized, err := usecase.NormalizeRecognizedProblems("printed-textbook-response", questions)
	if err != nil {
		t.Fatal(err)
	}
	for i, question := range normalized {
		if question.SourceSectionLabel != "二 分数乘法" ||
			!reflect.DeepEqual(question.SourceSectionPath, []string{"二"}) ||
			!reflect.DeepEqual(question.SourceNumberPath, questions[i].SourceNumberPath) ||
			question.DisplayLabel != questions[i].DisplayLabel ||
			question.RawTranscription != questions[i].RawTranscription ||
			question.AnswerState != usecase.AnswerStateBlank ||
			question.StudentAnswer != "" {
			t.Fatalf("normalization changed the saved printed facts at %d: %#v", i, question)
		}
	}
	if normalized[1].ParentProblemID != normalized[0].ProblemID ||
		normalized[2].ParentProblemID != normalized[0].ProblemID ||
		normalized[1].SubproblemNo != "1" || normalized[2].SubproblemNo != "2" {
		t.Fatalf("printed compound-question mapping changed: %#v", normalized)
	}
	if normalized[3].SystemSectionOrdinal != 1 || normalized[4].SystemSectionOrdinal != 2 {
		t.Fatalf("unnumbered questions lost server-derived order: %#v", normalized)
	}
}

func TestRecognizerPrintedRulesReachWholePageAndFragmentsWithoutClearingHandwriting(t *testing.T) {
	var response []map[string]any
	if err := json.Unmarshal([]byte(printedPracticeSavedResponse), &response); err != nil {
		t.Fatal(err)
	}
	// 同一印刷题旁有可辨认手写时，归属提示不能成为事后清空作答的理由。
	response[1]["answer_state"] = "present"
	response[1]["student_answer"] = "3/4张纸"
	response[1]["answer_canonical_markdown"] = "3/4张纸"
	array, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	inventory := make([]map[string]any, len(response))
	for i, question := range response {
		inventory[i] = map[string]any{
			"source_number_path": question["source_number_path"],
			"display_label":      question["display_label"],
			"question":           question["question"],
		}
	}
	whole, err := json.Marshal(map[string]any{"questions": response, "printed_inventory": inventory})
	if err != nil {
		t.Fatal(err)
	}
	printed, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, fallback := range []bool{false, true} {
		name := "whole_page"
		if fallback {
			name = "fragment_fallback"
		}
		t.Run(name, func(t *testing.T) {
			wholeCalls, fragmentCalls := 0, 0
			recognizer := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				if strings.Contains(prompt, "整页印刷题清单") {
					return string(printed), nil
				}
				assertPrintedRecognitionRules(t, prompt)
				if strings.Contains(prompt, "纵向分片") {
					fragmentCalls++
					return string(array), nil
				}
				wholeCalls++
				if fallback {
					return "not-json", nil
				}
				return string(whole), nil
			})
			questions, err := recognizer.Recognize(
				context.Background(), denseWorksheetTestImage(t, 1000, 1800),
			)
			if err != nil {
				t.Fatal(err)
			}
			if wholeCalls != 1 || (fallback && fragmentCalls == 0) || (!fallback && fragmentCalls != 0) {
				t.Fatalf("wrong actual recognition route: whole=%d fragments=%d fallback=%t", wholeCalls, fragmentCalls, fallback)
			}
			if len(questions) != 5 {
				t.Fatalf("recognized questions=%d, want 5", len(questions))
			}
			foundHandwriting, foundPrintedBlank := false, false
			for _, question := range questions {
				switch {
				case reflect.DeepEqual(question.SourceNumberPath, []string{"二", "1", "1"}):
					foundHandwriting = question.AnswerState == usecase.AnswerStatePresent &&
						question.StudentAnswer == "3/4张纸" && question.AnswerCanonicalMarkdown == "3/4张纸"
				case strings.HasPrefix(question.RawTranscription, "一袋面包重3/10"):
					foundPrintedBlank = question.AnswerState == usecase.AnswerStateBlank &&
						question.StudentAnswer == "" && question.AnswerCanonicalMarkdown == ""
				}
			}
			if !foundHandwriting || !foundPrintedBlank {
				t.Fatalf("printed/handwritten ownership facts changed: handwriting=%t printed_blank=%t questions=%#v",
					foundHandwriting, foundPrintedBlank, questions)
			}
		})
	}
}
