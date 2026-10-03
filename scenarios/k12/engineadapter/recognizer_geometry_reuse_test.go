package engineadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestAnchorAnswerGeometryReusesAllTrustedRegionsWithoutProvider(t *testing.T) {
	questions := geometryReuseQuestions()
	questions = []usecase.RecognizedQuestion{questions[0], questions[2]}
	var calls atomic.Int32
	a := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) {
		calls.Add(1)
		return "[]", nil
	})
	image := anchorTestImage(t)
	wantBoxes := []usecase.BBox{
		{X: 0.1, Y: 0.025, W: 0.1, H: 0.005},
		{X: 0.1, Y: 0.175, W: 0.1, H: 0.005},
	}
	reused, ok := a.ReuseRecognitionAnswerGeometry(context.Background(), image, questions)
	if !ok || len(reused) != 2 {
		t.Fatalf("trusted recognition rectangles did not reuse: ok=%t result=%+v", ok, reused)
	}
	anchored, err := a.AnchorAnswerGeometry(context.Background(), image, questions)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("trusted rectangles caused %d provider calls", calls.Load())
	}
	for i := range questions {
		if reused[i].BBox == nil || *reused[i].BBox != wantBoxes[i] || anchored[i].BBox == nil || *anchored[i].BBox != wantBoxes[i] {
			t.Fatalf("target %d reused a different rectangle: reused=%+v anchored=%+v", i+1, reused[i].BBox, anchored[i].BBox)
		}
		assertGeometryFrozenFacts(t, questions[i], anchored[i])
	}
}

func TestAnchorAnswerGeometryOnlyLocatesUnresolvedTargetsAndMapsReturnedIDs(t *testing.T) {
	questions := geometryReuseQuestions()
	before, err := json.Marshal(questions)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
		calls.Add(1)
		targets := geometryReusePromptTargets(t, prompt)
		if len(targets) != 2 || targets[0].Index != 2 || targets[1].Index != 4 {
			t.Fatalf("locator received already trusted or blank targets: %+v", targets)
		}
		if targets[0].AnswerHint != "4" || targets[1].AnswerHint != "8" || !reflect.DeepEqual(targets[0].SourceRegion, questions[1].SourceRegion) || !reflect.DeepEqual(targets[1].SourceRegion, questions[3].SourceRegion) {
			t.Fatalf("locator lost frozen target identity: %+v", targets)
		}
		// 定位返回顺序不定义题目身份，未请求题目的结果不能覆盖可信框。
		return `[{"index":4,"bbox_1000":[600,175,700,180]},{"index":1,"bbox_1000":[600,25,700,30]},{"index":2,"bbox_1000":[600,25,700,30]}]`, nil
	})
	got, err := a.AnchorAnswerGeometry(context.Background(), anchorTestImage(t), questions)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(got) != len(questions) {
		t.Fatalf("remaining targets must share one location call: calls=%d questions=%d", calls.Load(), len(got))
	}
	wantBoxes := []*usecase.BBox{
		{X: 0.1, Y: 0.025, W: 0.1, H: 0.005},
		{X: 0.6, Y: 0.025, W: 0.1, H: 0.005},
		{X: 0.1, Y: 0.175, W: 0.1, H: 0.005},
		{X: 0.6, Y: 0.175, W: 0.1, H: 0.005},
		nil,
	}
	for i := range questions {
		if !reflect.DeepEqual(got[i].BBox, wantBoxes[i]) {
			t.Fatalf("target %d received wrong location: got=%+v want=%+v", i+1, got[i].BBox, wantBoxes[i])
		}
		assertGeometryFrozenFacts(t, questions[i], got[i])
	}
	after, err := json.Marshal(questions)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("geometry mutated the caller's frozen facts: err=%v", err)
	}
}

func TestAnchorAnswerGeometryInvalidRegionsRemainUnresolvedWithExactReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		change func(*usecase.RecognizedQuestion)
	}{
		{"missing observed region", "answer_region_missing", func(q *usecase.RecognizedQuestion) { q.ObservedAnswerRegion = nil }},
		{"missing source region", "source_region_missing", func(q *usecase.RecognizedQuestion) { q.SourceRegion = nil }},
		{"different image dimensions", "source_dimensions_mismatch", func(q *usecase.RecognizedQuestion) { q.SourceWidth = 999 }},
		{"outside source", "answer_region_outside_source", func(q *usecase.RecognizedQuestion) {
			q.ObservedAnswerRegion = &k12.SourcePixelRegion{X: 100, Y: 40, Width: 100, Height: 8}
		}},
		{"whole source region", "answer_region_is_source", func(q *usecase.RecognizedQuestion) {
			q.ObservedAnswerRegion = &k12.SourcePixelRegion{X: 500, Y: 0, Width: 500, Height: 200}
		}},
		{"no visible ink", "answer_bbox_without_ink", func(q *usecase.RecognizedQuestion) {
			q.ObservedAnswerRegion = &k12.SourcePixelRegion{X: 600, Y: 60, Width: 100, Height: 8}
		}},
		{"unresolved handwriting", "answer_not_present", func(q *usecase.RecognizedQuestion) {
			q.AnswerState, q.StudentAnswer = usecase.AnswerStateUnclear, ""
		}},
	}
	image := anchorTestImage(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			questions := geometryReuseQuestions()[:2]
			questions[1].ObservedAnswerRegion = &k12.SourcePixelRegion{X: 600, Y: 40, Width: 100, Height: 8}
			tt.change(&questions[1])
			var trace bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&trace, nil)))
			defer slog.SetDefault(previous)
			var calls atomic.Int32
			a := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				calls.Add(1)
				targets := geometryReusePromptTargets(t, prompt)
				if len(targets) != 1 || targets[0].Index != 2 {
					t.Fatalf("invalid candidate affected its trusted sibling: %+v", targets)
				}
				return "[]", nil
			})
			if _, ok := a.ReuseRecognitionAnswerGeometry(context.Background(), image, questions); ok {
				t.Fatal("whole-page reuse accepted an invalid candidate")
			}
			got, err := a.AnchorAnswerGeometry(context.Background(), image, questions)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || got[0].BBox == nil || got[1].BBox != nil {
				t.Fatalf("unresolved rectangle must not be fabricated: calls=%d result=%+v", calls.Load(), got)
			}
			for _, field := range []string{"problem_id=problem-geometry-2", "target_index=2", "reason=" + tt.reason} {
				if !strings.Contains(trace.String(), field) {
					t.Fatalf("trace did not explain failed candidate %q: %s", field, trace.String())
				}
			}
			for i := range questions {
				assertGeometryFrozenFacts(t, questions[i], got[i])
			}
		})
	}
}

func TestAnchorAnswerGeometryUnknownPreservesVerifiedGeometryWithoutRetry(t *testing.T) {
	questions := geometryReuseQuestions()[:2]
	before, err := json.Marshal(questions)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a := NewRecognizerAdapter(func(context.Context, []byte, string) (string, error) {
		calls.Add(1)
		return "", context.DeadlineExceeded
	})
	got, err := a.AnchorAnswerGeometry(context.Background(), anchorTestImage(t), questions)
	if !errors.Is(err, context.DeadlineExceeded) || len(got) != 2 || calls.Load() != 1 {
		t.Fatalf("unknown location must preserve the error and verified geometry without resend: calls=%d result=%+v err=%v", calls.Load(), got, err)
	}
	if got[0].BBox == nil || got[1].BBox != nil {
		t.Fatalf("only locally verified geometry may survive locator unknown: %+v", got)
	}
	after, marshalErr := json.Marshal(questions)
	if marshalErr != nil || !bytes.Equal(before, after) {
		t.Fatalf("unknown location changed frozen source facts: err=%v", marshalErr)
	}
}

func TestAnchorAnswersReusedGeometryKeepsIndependentTranscription(t *testing.T) {
	questions := geometryReuseQuestions()[:1]
	var calls atomic.Int32
	a := NewRecognizerAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
		calls.Add(1)
		if !strings.Contains(prompt, "批量答案誊录") {
			t.Errorf("trusted geometry should still request independent transcription only")
			return "[]", nil
		}
		return `[{"index":1,"student_answer":"2"}]`, nil
	})
	got, err := a.AnchorAnswers(context.Background(), anchorTestImage(t), questions)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || got[0].BBox == nil || got[0].StudentAnswer != "2" || got[0].AnswerState != usecase.AnswerStatePresent {
		t.Fatalf("geometry reuse skipped independent handwriting evidence: calls=%d result=%+v", calls.Load(), got)
	}
}

func geometryReuseQuestions() []usecase.RecognizedQuestion {
	questions := make([]usecase.RecognizedQuestion, 5)
	regions := []k12.SourcePixelRegion{
		{X: 0, Y: 0, Width: 500, Height: 200},
		{X: 500, Y: 0, Width: 500, Height: 200},
		{X: 0, Y: 200, Width: 500, Height: 200},
		{X: 500, Y: 200, Width: 500, Height: 200},
		{X: 0, Y: 400, Width: 500, Height: 200},
	}
	for i := range questions {
		questions[i] = usecase.RecognizedQuestion{
			ProblemID:                    []string{"problem-geometry-1", "problem-geometry-2", "problem-geometry-3", "problem-geometry-4", "problem-geometry-5"}[i],
			Question:                     []string{"1+1=", "2+2=", "3+3=", "4+4=", "5+5="}[i],
			StudentAnswer:                []string{"2", "4", "6", "8", ""}[i],
			AnswerState:                  usecase.AnswerStatePresent,
			Subject:                      "数学",
			SourceWidth:                  1000,
			SourceHeight:                 1600,
			SourceRegion:                 &regions[i],
			SourceNumberPath:             []string{"original-number"},
			SourceSectionLabel:           "计算",
			EvidenceTranscriptions:       []string{"first printed read", "independent printed read"},
			AnswerEvidenceTranscriptions: []string{"first handwriting read", "independent handwriting read"},
		}
	}
	questions[0].ObservedAnswerRegion = &k12.SourcePixelRegion{X: 100, Y: 40, Width: 100, Height: 8}
	questions[2].ObservedAnswerRegion = &k12.SourcePixelRegion{X: 100, Y: 280, Width: 100, Height: 8}
	questions[3].ObservedAnswerRegion = &k12.SourcePixelRegion{X: 100, Y: 40, Width: 100, Height: 8}
	questions[4].AnswerState = usecase.AnswerStateBlank
	return questions
}

func geometryReusePromptTargets(t *testing.T, prompt string) []answerBBoxTarget {
	t.Helper()
	start := strings.Index(prompt, "[{")
	if start < 0 {
		t.Fatalf("locator omitted its target list")
	}
	var targets []answerBBoxTarget
	if err := json.NewDecoder(strings.NewReader(prompt[start:])).Decode(&targets); err != nil {
		t.Fatalf("locator targets were not valid JSON: %v", err)
	}
	return targets
}

func assertGeometryFrozenFacts(t *testing.T, want, got usecase.RecognizedQuestion) {
	t.Helper()
	if got.ProblemID != want.ProblemID || got.Question != want.Question || got.StudentAnswer != want.StudentAnswer || got.AnswerState != want.AnswerState ||
		got.SourceWidth != want.SourceWidth || got.SourceHeight != want.SourceHeight || !reflect.DeepEqual(got.SourceRegion, want.SourceRegion) || !reflect.DeepEqual(got.ObservedAnswerRegion, want.ObservedAnswerRegion) ||
		!reflect.DeepEqual(got.SourceNumberPath, want.SourceNumberPath) || got.SourceSectionLabel != want.SourceSectionLabel ||
		!reflect.DeepEqual(got.EvidenceTranscriptions, want.EvidenceTranscriptions) || !reflect.DeepEqual(got.AnswerEvidenceTranscriptions, want.AnswerEvidenceTranscriptions) {
		t.Fatalf("geometry rewrote frozen identity, source or readings: want=%+v got=%+v", want, got)
	}
}
