package usecase_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

type elementaryEquationSavedRecognition struct{ question usecase.RecognizedQuestion }

func (r elementaryEquationSavedRecognition) Recognize(context.Context, []byte) ([]usecase.RecognizedQuestion, error) {
	return []usecase.RecognizedQuestion{r.question}, nil
}

func elementaryEquationAssembly(t *testing.T) (*assembly.K12, *atomic.Int32, *sql.DB) {
	t.Helper()
	db := openMigratedTestDB(t)
	if _, err := db.Exec(`INSERT INTO agents(name) VALUES('scope-child')`); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	solver := engine.NewSolveSkill(func(_ context.Context, spec engine.SubAgentSpec) (engine.SubAgentResult, error) {
		calls.Add(1)
		return engine.SubAgentResult{}, fmt.Errorf("unexpected model call: %s", spec.Agent)
	}, nil)
	wired, err := assembly.Wire(db, solver, assembly.WithParentTeachingGuideGenerator(
		func(_ context.Context, subject, prompt, grade string) (string, error) {
			_, rawFacts, found := strings.Cut(prompt, "题目事实：")
			var facts struct {
				SourceAnswer string `json:"source_answer"`
			}
			if err := json.Unmarshal([]byte(rawFacts), &facts); !found || err != nil {
				return "", fmt.Errorf("invalid teaching facts: %v", err)
			}
			answer := strings.NewReplacer(" ", "", "**", "").Replace(facts.SourceAnswer)
			if subject != "数学" || grade != "六年级上" || answer != "x=24" && answer != "24" {
				return "", fmt.Errorf("unexpected teaching request: subject=%q grade=%q answer=%q", subject, grade, facts.SourceAnswer)
			}
			guide := usecase.ParentTeachingGuide{
				Answer: facts.SourceAnswer, FullSolutionSteps: []string{"24-24/4=18"},
				GradeLevelMethod:       "把这个未知量看作4份，剩下3份是18，每份是6，4份是24。",
				LikelyMistakes:         []string{"不能把一个量的四分之一看成数四分之一直接相减。"},
				ParentTeachingSequence: []string{"先说明4份与3份的关系。", "再求一份与原数，并代回核对。"},
				FollowUpQuestions:      []string{"剩下3份为何对应18？"}, CheckingMethod: "代入原式，24-24/4=18。",
			}
			raw, err := json.Marshal(guide)
			return string(raw), err
		}))
	if err != nil {
		t.Fatal(err)
	}
	return wired, calls, db
}

func assertElementaryEquationGuide(t *testing.T, result usecase.BlankWorksheetProblemResult) {
	t.Helper()
	if result.Solved.OutOfScope || result.Solved.Evidence.EvidenceType != usecase.EvidenceNumericExec || !strings.Contains(result.Solved.Solution, "x = 24") {
		t.Fatalf("elementary equation was rejected or not verified: %+v", result)
	}
	guide := result.Guide
	if !strings.Contains(guide.Answer, "24") || len(guide.FullSolutionSteps) == 0 || guide.GradeLevelMethod == "" || len(guide.LikelyMistakes) == 0 || len(guide.ParentTeachingSequence) == 0 || len(guide.FollowUpQuestions) == 0 || guide.CheckingMethod == "" {
		t.Fatalf("incomplete seven-field guide: %+v", guide)
	}
	if strings.Contains(guide.GradeLevelMethod, "合并同类项") {
		t.Fatalf("guide described a middle-school method: %q", guide.GradeLevelMethod)
	}
	sample, _ := json.Marshal(map[string]any{"solution": result.Solved.Solution, "guide": guide, "evidence_type": result.Solved.Evidence.EvidenceType})
	t.Logf("ELEMENTARY_EQUATION_SAMPLE=%s", sample)
}

func TestElementaryEquationCurriculumRealAssemblyPreservesSourceAndProducesGuide(t *testing.T) {
	for _, tc := range []struct {
		question string
		points   []string
	}{
		{"x－1/4x＝18", []string{"解方程", "合并同类项"}},
		{"x-1/4x=18", []string{"解方程", "合并同类项"}},
		{`解方程：$x-\frac{1}{4}x=18$`, []string{"解方程", "合并同类项"}},
		{"x-1/4x=18", []string{"解一元一次方程", "合并同类项", "分数系数方程"}},
		{`解方程：$x-\frac{1}{4}x=18$`, []string{"解一元一次方程", "合并同类项", "分数系数方程"}},
	} {
		t.Run(tc.question+strings.Join(tc.points, "/"), func(t *testing.T) {
			wired, calls, _ := elementaryEquationAssembly(t)
			question, points := tc.question, tc.points
			before := append([]string(nil), points...)
			req := usecase.GradeRequest{AgentName: "scope-child", Subject: "数学", Grade: "六年级上", Problem: question, KnowledgePoints: points}
			result, err := wired.Deps.SolveBlankWorksheetProblem(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			assertElementaryEquationGuide(t, result)
			if req.Problem != question || !reflect.DeepEqual(points, before) || calls.Load() != 0 {
				t.Fatalf("source mutated or model used: req=%+v calls=%d", req, calls.Load())
			}
			confidence := .99
			recognized := usecase.RecognizedQuestion{Question: question, RawTranscription: question, CanonicalMarkdown: question, KnowledgePoints: append([]string(nil), points...), Subject: "数学", AnswerState: usecase.AnswerStateBlank, RecognitionConfidence: &confidence}
			wired.Deps.Recognizer = elementaryEquationSavedRecognition{question: recognized}
			digest := usecase.CanonicalRecognizedQuestionsDigest([]usecase.RecognizedQuestion{recognized})
			photo, err := wired.Deps.GradeHomeworkPhoto(context.Background(), usecase.PhotoGradeRequest{AgentName: "scope-child", Subject: "数学", Grade: "六年级上", Image: []byte("saved worksheet fixture")})
			if err != nil {
				t.Fatal(err)
			}
			if len(photo.Items) != 1 || photo.Items[0].Status != usecase.PhotoBlankSolved || photo.Items[0].ParentGuide == nil || photo.Items[0].Grade.RecordCreated {
				t.Fatalf("blank photo did not share solve boundary: %+v", photo)
			}
			if photo.Items[0].Recognized.RawTranscription != question || !reflect.DeepEqual(photo.Items[0].Recognized.KnowledgePoints, before) || usecase.CanonicalRecognizedQuestionsDigest([]usecase.RecognizedQuestion{recognized}) != digest {
				t.Fatal("original recognition or digest changed")
			}
			if calls.Load() != 0 {
				t.Fatalf("model calls=%d", calls.Load())
			}
		})
	}
}

func TestElementaryEquationCurriculumCertificateDoesNotWidenCourseOrGrammar(t *testing.T) {
	wired, calls, _ := elementaryEquationAssembly(t)
	classifier, ok := wired.Deps.Solver.(usecase.ElementaryEquationClassifier)
	if !ok {
		t.Fatal("actual assembly lost classifier port")
	}
	for _, tc := range []struct {
		question, grade string
		want            bool
	}{
		{"x－1/4x＝18", "六年级上", true}, {"18=x-1/4x", "六年级上", true},
		{`解方程：$x-\frac{1}{4}x=18$`, "六年级上", true},
		{`解方程：$x-\frac{1}{4}x=18$`, "五年级下", false},
		{`解方程：$x-\frac{1}{4}x=18`, "六年级上", false},
		{"3x+2x=20", "五年级上", true}, {"0.5x+0.25x=9", "五年级上", true},
		{"x-1/4x=18", "五年级下", false}, {"3x+2x=20", "四年级下", false},
		{"x*x+2*x=18", "六年级上", false}, {"x+2*y=18", "六年级上", false},
		{"x/4+1/x=18", "六年级上", false}, {"2*(x+1)+x=18", "六年级上", false},
		{"2x+x=x+18", "六年级上", false}, {"x+x=18; x=9", "六年级上", false},
		{"x+x", "六年级上", false}, {"x-x=18", "六年级上", false},
		{"x-2*x=18", "六年级上", false}, {"x+1=18", "六年级上", false},
		{"1 2x+x=18", "六年级上", false}, {"x+x=1 8", "六年级上", false},
	} {
		t.Run(tc.question+"/"+tc.grade, func(t *testing.T) {
			allowed, err := wired.Deps.Constraint.Allowed(context.Background(), tc.grade)
			if err != nil {
				t.Fatal(err)
			}
			if got := classifier.ElementaryEquationWithinCurriculum(tc.question, allowed); got != tc.want {
				t.Fatalf("certificate=%t want=%t", got, tc.want)
			}
			if !tc.want {
				result, err := wired.Deps.SolveHomeworkProblem(context.Background(), usecase.GradeRequest{AgentName: "scope-child", Subject: "数学", Grade: tc.grade, Problem: tc.question, KnowledgePoints: []string{"解方程", "合并同类项"}})
				if err != nil || !result.OutOfScope {
					t.Fatalf("unproven input crossed original course gate: %+v err=%v", result, err)
				}
			}
		})
	}
	first, known := wired.Deps.Constraint.FirstGrade(context.Background(), "合并同类项")
	if !known || first != "初一上" || calls.Load() != 0 {
		t.Fatalf("course table/model changed: %q calls=%d", first, calls.Load())
	}
}

type elementaryEquationLegacySolver struct{ usecase.Solver }
type elementaryEquationFailedExecutor struct{}

func (elementaryEquationFailedExecutor) Execute(context.Context, map[string]any) (*skill.Result, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestElementaryEquationCurriculumDurableSQLiteKeepsOldResultsAndTechnicalFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wired, calls, db := elementaryEquationAssembly(t)
	confidence := .99
	question := usecase.RecognizedQuestion{Question: "x－1/4x＝18", RawTranscription: "x－1/4x＝18", CanonicalMarkdown: "x－1/4x＝18", SourceNumberPath: []string{"12"}, DisplayLabel: "12", Subject: "数学", KnowledgePoints: []string{"解方程", "合并同类项"}, AnswerState: usecase.AnswerStateBlank, RecognitionConfidence: &confidence}
	deps := wired.Deps
	deps.Recognizer = elementaryEquationSavedRecognition{question: question}
	deps.Profiles = newMemProfilesE2E()
	deps.Profiles.(*memProfilesE2E).m["scope-child"] = k12.ChildProfile{ChildName: "学生", GradeTerm: "六年级上"}
	deps.Now = func() int64 { return time.Now().Unix() }
	deps.GradingBudgetSnapshot = k12.GradingBudgetSnapshot{PolicyVersion: 1, RecognitionPlanVersion: k12.RecognitionPlanVersionV1, StageSeconds: k12.GradingStageBudgets{Queued: 60, Normalizing: 60, Recognizing: 120, Locating: 60, Rendering: 60, Projecting: 60}, AssessingBuckets: []k12.GradingAssessingBudgetBucket{{MaxProblems: 1, Seconds: 90}, {MaxProblems: 8, Seconds: 180}, {MaxProblems: 16, Seconds: 300}, {MaxProblems: 32, Seconds: 540}}, ItemConcurrency: 1}
	var fixtureImage bytes.Buffer
	if err := png.Encode(&fixtureImage, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	runJob := func(d usecase.Deps, key string) (*usecase.GradingOrchestrator, usecase.GradingJobView, usecase.PhotoGradeResult) {
		o := usecase.NewGradingOrchestrator(d, func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
			return k12.GradingModelSnapshot{Provider: "fixture", Model: "local-math", Capability: "vision"}, nil
		}, usecase.WithGradingRunDir(t.TempDir()))
		t.Cleanup(func() {
			if err := o.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
		job, created, err := o.StartPhotoGradingJob(context.Background(), usecase.StartPhotoGradingInput{Photo: usecase.PhotoGradeRequest{AgentName: "scope-child", Subject: "数学", Grade: "六年级上", Image: fixtureImage.Bytes()}, SourceKind: "im", SourceKey: key})
		if err != nil || !created {
			t.Fatalf("start: created=%t err=%v", created, err)
		}
		job, err = o.RunGradingJob(context.Background(), job.Record.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Record.Status == k12.GradingStageAwaitingConfirmation {
			job, err = o.ConfirmAndRun(context.Background(), job.Record.RecordID, nil)
		}
		if err != nil || job.Record.Status != k12.GradingStageCompleted {
			t.Fatalf("completion stage=%s err=%v", job.Record.Status, err)
		}
		result, ok := o.PhotoResult(job.Record.RecordID)
		if !ok {
			t.Fatal("completed result unavailable")
		}
		return o, job, result
	}
	legacy := deps
	legacy.Solver = elementaryEquationLegacySolver{Solver: deps.Solver}
	_, oldJob, oldResult := runJob(legacy, "legacy-equation")
	if len(oldResult.Items) != 1 || oldResult.Items[0].Status != usecase.PhotoOutOfScope {
		t.Fatalf("legacy fixture did not keep original gate: %+v", oldResult)
	}
	oldBefore, _ := json.Marshal(oldJob)
	_, newJob, newResult := runJob(deps, "new-equation")
	if len(newResult.Items) != 1 || newResult.Items[0].Status != usecase.PhotoBlankSolved || newResult.Items[0].ParentGuide == nil {
		t.Fatalf("durable assembly lost positive certificate: %+v", newResult)
	}
	assertElementaryEquationGuide(t, usecase.BlankWorksheetProblemResult{Solved: newResult.Items[0].Solve, Guide: *newResult.Items[0].ParentGuide})
	if !reflect.DeepEqual(newResult.Items[0].Recognized.KnowledgePoints, question.KnowledgePoints) || newResult.Items[0].Recognized.RawTranscription != question.RawTranscription || newResult.Items[0].Recognized.InputDigest == "" {
		t.Fatal("durable original facts were changed or lost")
	}
	invocations, err := deps.Records.ListGradingItemInvocations(context.Background(), "scope-child", newJob.Record.RecordID)
	if err != nil || len(invocations) == 0 {
		t.Fatalf("new execution receipts missing: %v", err)
	}
	var digests []map[string]any
	for _, row := range invocations {
		if row.RequestDigest == "" {
			t.Fatal("execution request digest missing")
		}
		digests = append(digests, map[string]any{"operation": row.Operation, "execution_kind": row.ExecutionKind, "request_digest": row.RequestDigest, "status": row.Status})
	}
	digestJSON, _ := json.Marshal(digests)
	t.Logf("ELEMENTARY_EQUATION_REQUEST_DIGESTS=%s", digestJSON)
	oldAfter, err := deps.GetGradingJob(context.Background(), "scope-child", oldJob.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	oldAfterJSON, _ := json.Marshal(oldAfter)
	if string(oldBefore) != string(oldAfterJSON) || oldJob.Record.RecordID == newJob.Record.RecordID {
		t.Fatal("new execution changed old completed job or reused key")
	}
	var mistakes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM k12_mistakes`).Scan(&mistakes); err != nil {
		t.Fatal(err)
	}
	if mistakes != 0 || calls.Load() != 0 {
		t.Fatalf("blank solve wrote grade/model calls: mistakes=%d calls=%d", mistakes, calls.Load())
	}
	failed := deps
	failed.Solver = engineadapter.NewSolveAdapter(elementaryEquationFailedExecutor{})
	result, err := failed.SolveHomeworkProblem(context.Background(), usecase.GradeRequest{AgentName: "scope-child", Subject: "数学", Grade: "六年级上", Problem: question.Question, KnowledgePoints: question.KnowledgePoints})
	if err == nil || result.OutOfScope || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("technical failure became a curriculum result: %+v err=%v", result, err)
	}
}
