package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestGradingPDFConservativeGuideProjectionSQLite(t *testing.T) {
	base := func(answer string) ParentTeachingGuide {
		return ParentTeachingGuide{
			Answer:                 answer,
			FullSolutionSteps:      []string{"保留本题完整计算步骤。"},
			GradeLevelMethod:       "按已学整数加法说明每一步。",
			LikelyMistakes:         []string{"不要漏看原题运算符号。"},
			ParentTeachingSequence: []string{"请先说明运算顺序。"},
			FollowUpQuestions:      []string{"这一步为什么成立？"},
			CheckingMethod:         "将本题答案代回原题检查。",
		}
	}
	tests := []struct {
		name   string
		guides []ParentTeachingGuide
		edit   func([]ParentTeachingGuide)
		counts map[string]int
	}{
		{
			name:   "exact common fields once but full solution belongs to each problem",
			guides: []ParentTeachingGuide{base("2"), base("3")},
			counts: map[string]int{
				"保留本题完整计算步骤。":   2,
				"按已学整数加法说明每一步。": 1,
				"不要漏看原题运算符号。":   1,
				"请先说明运算顺序。":     1,
				"这一步为什么成立？":     1,
				"将本题答案代回原题检查。":  1,
			},
		},
		{
			name:   "partial or similar wording remains attached to its own problem",
			guides: []ParentTeachingGuide{base("2"), base("3"), base("4")},
			edit: func(g []ParentTeachingGuide) {
				g[2].GradeLevelMethod = "使用本题线段图说明数量关系。"
				g[1].ParentTeachingSequence = []string{"请先说明运算次序。"}
			},
			counts: map[string]int{
				"按已学整数加法说明每一步。":  2,
				"使用本题线段图说明数量关系。": 1,
				"请先说明运算顺序。":      2,
				"请先说明运算次序。":      1,
				"将本题答案代回原题检查。":   1,
			},
		},
		{
			name:   "unique long teaching fields retain their complete tails",
			guides: []ParentTeachingGuide{base("2"), base("3")},
			edit: func(g []ParentTeachingGuide) {
				g[0].Answer = strings.Repeat("参照本题完整答案。", 80) + "答案独有尾声。"
				g[0].FullSolutionSteps = []string{strings.Repeat("逐式核对当前题的运算。", 80) + "步骤独有尾声。"}
				g[0].GradeLevelMethod = strings.Repeat("用当前年级已经学过的方法说明。", 100) + "年级方法独有尾声。"
				g[0].LikelyMistakes = []string{strings.Repeat("检查原题每一项条件和单位。", 80) + "易错点独有尾声。"}
				g[0].ParentTeachingSequence = []string{strings.Repeat("让孩子解释当前题的这一步。", 80) + "家长讲法独有尾声。"}
				g[0].FollowUpQuestions = []string{strings.Repeat("这一问对应原题的哪个条件？", 80) + "追问独有尾声。"}
				g[0].CheckingMethod = strings.Repeat("把当前题的结果代回原题核验。", 100) + "检查方法独有尾声。"
			},
			counts: map[string]int{
				"答案独有尾声。":   1,
				"步骤独有尾声。":   1,
				"年级方法独有尾声。": 1,
				"易错点独有尾声。":  1,
				"家长讲法独有尾声。": 1,
				"追问独有尾声。":   1,
				"检查方法独有尾声。": 1,
			},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.edit != nil {
				tt.edit(tt.guides)
			}
			d, artifact := gradingPDFProjectionFixture(t, tt.guides)
			ctx := context.Background()
			before, err := d.Records.ListGradingAssessmentItems(ctx, artifact.AgentName, artifact.JobID)
			if err != nil {
				t.Fatal(err)
			}
			renderer := &finalReplyPDFRenderer{}
			d.Renderer = renderer
			printed, replay, err := d.PrepareGradingFinalArtifactPDFExact(ctx, artifact.AgentName, artifact.ArtifactID, artifact.ArtifactDigest, "作业批改结果")
			if err != nil || replay {
				t.Fatalf("prepare new PDF: replay=%v err=%v", replay, err)
			}
			for content, count := range tt.counts {
				if got := strings.Count(renderer.markdown, content); got != count {
					t.Errorf("teaching content %q occurs %d times; want %d", content, got, count)
				}
			}
			if printed.Artifact.CanonicalMarkdown != artifact.CanonicalMarkdown {
				t.Fatal("PDF projection replaced the frozen canonical source")
			}
			again, replay, err := d.PrepareGradingFinalArtifactPDFExact(ctx, artifact.AgentName, artifact.ArtifactID, artifact.ArtifactDigest, "作业批改结果")
			if err != nil || !replay || renderer.calls != 1 || !bytes.Equal(again.Render.Payload, printed.Render.Payload) || again.Render.ByteDigest != printed.Render.ByteDigest {
				t.Fatalf("frozen PDF replay rendered again or changed bytes: calls=%d replay=%v err=%v", renderer.calls, replay, err)
			}
			after, err := d.Records.ListGradingAssessmentItems(ctx, artifact.AgentName, artifact.JobID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("teaching projection mutated durable assessments: %v", err)
			}
			stored, err := d.Records.GetGradingFinalArtifact(ctx, artifact.AgentName, artifact.ArtifactID)
			if err != nil || !reflect.DeepEqual(stored, artifact) {
				t.Fatalf("teaching projection changed frozen final artifact: %v", err)
			}
			if dir := os.Getenv("HEXCLAW_PDF_PROJECTION_AUTHOR_DIR"); dir != "" && index == 0 {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "public-projection-source.md"), []byte(renderer.markdown), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestGradingPDFCompoundSourceProjection(t *testing.T) {
	shared := "小明 $\\frac{2}{3}$ 小时走了 $2$ 千米，照这样的速度，他每小时行走多少千米？\n\n线段图标注：\n- $1$ 小时走了 $?$ km；\n- $\\frac{1}{3}$ 小时走了 $?$ km；\n- $\\frac{2}{3}$ 小时走了 $2$ km。\n\n教材给出的推算过程：\n$$2\\div\\frac{2}{3}=2\\div2\\times3=2\\times\\frac12\\times3=2\\times\\left(\\frac12\\times3\\right)=2\\times\\frac32=3\\text{（km）}。$$"
	current := []string{
		"照这样的速度，他每小时行走多少千米？",
		"从上图看出：已知（　）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少。",
		"推算过程中“$2\\times\\frac12\\times3=2\\times\\left(\\frac12\\times3\\right)$”的依据是（　）。",
	}
	tests := []struct {
		name  string
		edit  func([]gradingFinalEntry)
		count int
	}{
		{name: "same confirmed D13 parent is printed once", count: 1},
		{name: "unclear child answer preserves one confirmed parent source", count: 1, edit: func(e []gradingFinalEntry) {
			e[2].question.AnswerState = AnswerStateUnclear
			e[2].question.ConfirmationRequired = true
			e[2].question.ConfirmationReasons = []OCRRiskReason{OCRRiskUnclearHandwriting}
		}},
		{name: "different parent remains separate", count: 2, edit: func(e []gradingFinalEntry) { e[1].question.ParentProblemID = "another-parent" }},
		{name: "different source page remains separate", count: 2, edit: func(e []gradingFinalEntry) { e[1].question.PageAssetID = "another-page" }},
		{name: "similar paragraph is not a common source", count: 3, edit: func(e []gradingFinalEntry) {
			e[1].question.CanonicalMarkdown = strings.Replace(e[1].question.CanonicalMarkdown, "小明", "小红", 1)
		}},
		{name: "unconfirmed member keeps the complete source", count: 3, edit: func(e []gradingFinalEntry) { e[1].question.ConfirmedVersion = 0 }},
		{name: "missing input identity keeps the complete source", count: 3, edit: func(e []gradingFinalEntry) { e[1].question.InputDigest = "" }},
		{name: "unclear parent keeps the complete source", count: 3, edit: func(e []gradingFinalEntry) { e[1].question.parentSourceUnclear = true }},
		{name: "current original must survive the whole paragraph cut", count: 3, edit: func(e []gradingFinalEntry) {
			e[1].question.assessmentSource.canonical = e[1].question.CanonicalMarkdown
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := make([]gradingFinalEntry, len(current))
			for i, canonical := range current {
				q := RecognizedQuestion{
					ProblemID: fmt.Sprintf("d13-child-%d", i), ParentProblemID: "problem-1dab3784789d3d21fe14",
					AttemptID:   fmt.Sprintf("d13-attempt-%d", i),
					ProblemKind: ProblemKindSubproblem, PageAssetID: "d13-original-page", SubproblemNo: fmt.Sprint(i + 1),
					SourceSectionLabel: "3 分数除法", CanonicalVersion: 1, ConfirmedVersion: 1, InputDigest: fmt.Sprintf("d13-input-%d", i),
					CanonicalMarkdown: shared + "\n\n" + canonical, RawTranscription: canonical,
				}
				q.assessmentSource = &assessmentQuestionSource{problemID: q.ProblemID, inputDigest: q.InputDigest, raw: q.RawTranscription, canonical: canonical, canonicalVersion: 1, confirmedVersion: 1}
				guide := ParentTeachingGuide{Answer: "2", FullSolutionSteps: []string{"只解当前问项。"}, GradeLevelMethod: "按六年级数量关系计算。", LikelyMistakes: []string{"不要替换当前空位。"}, ParentTeachingSequence: []string{"先说明当前空位要填什么。"}, FollowUpQuestions: []string{"如何代回检查？"}, CheckingMethod: "将当前答案代回当前问项。"}
				item := gradingAssessmentCanonicalResult(PhotoGradeItem{Recognized: q, Status: PhotoWrong, Grade: GradeResult{Solution: "2", Outcome: GradeOutcome{Verdict: VerdictDisagree, WrongStep: "本题计算错误", ErrorCause: "未按原题计算"}}, ParentGuide: &guide})
				raw, err := json.Marshal(item)
				if err != nil {
					t.Fatal(err)
				}
				entries[i] = gradingFinalEntry{question: q, assessment: &k12.GradingAssessmentItem{AgentName: "d13-child", ProblemID: q.ProblemID, Status: k12.GradingAssessmentWrong, ResultJSON: string(raw)}}
			}
			if tt.edit != nil {
				tt.edit(entries)
			}
			beforeResults := make([]string, len(entries))
			beforeQuestions := make([]RecognizedQuestion, len(entries))
			for i := range entries {
				beforeResults[i] = entries[i].assessment.ResultJSON
				beforeQuestions[i] = entries[i].question
				copySource := *entries[i].question.assessmentSource
				beforeQuestions[i].assessmentSource = &copySource
			}
			markdown, ok := renderGradingPDF(entries, "")
			if !ok {
				t.Fatal("valid source projection was rejected")
			}
			if got := strings.Count(markdown, "教材给出的推算过程："); got != tt.count {
				t.Errorf("shared material occurs %d times; want %d", got, tt.count)
			}
			for _, text := range current {
				if !strings.Contains(markdown, strings.TrimSpace(weeklySheetQuestionMarkdown(text))) && !strings.Contains(markdown, strings.TrimSpace(text)) {
					// 完整题面未裁切时，当前问项仍位于同一个受控文字参数中。
					encoded, _ := json.Marshal(text)
					if !strings.Contains(markdown, string(encoded[1:len(encoded)-1])) {
						t.Errorf("current question or unique formula was lost: %s", text)
					}
				}
			}
			if strings.Contains(markdown, "### 1\n") || strings.Contains(markdown, "### 2\n") {
				t.Fatal("system ordering was printed as a source question number")
			}
			for i := range entries {
				if !reflect.DeepEqual(beforeQuestions[i], entries[i].question) || entries[i].assessment.ResultJSON != beforeResults[i] {
					t.Fatal("projection rewrote the frozen current source")
				}
			}
		})
	}
	t.Run("public PDF preserves compound sources and frozen replay", func(t *testing.T) {
		sources := []RecognizedQuestion{{ProblemID: "d13-parent", ProblemKind: ProblemKindCompoundParent, RawTranscription: shared, CanonicalMarkdown: shared}}
		guides := make([]ParentTeachingGuide, len(current))
		for i, text := range current {
			sources = append(sources, RecognizedQuestion{ProblemID: fmt.Sprintf("d13-current-%d", i), ProblemKind: ProblemKindSubproblem, ParentProblemID: "d13-parent", RawTranscription: text, CanonicalMarkdown: text})
			guides[i] = ParentTeachingGuide{Answer: "2", FullSolutionSteps: []string{"只解当前问项。"}, GradeLevelMethod: "按六年级数量关系计算。", LikelyMistakes: []string{"不要替换当前空位。"}, ParentTeachingSequence: []string{"先说明当前空位要填什么。"}, FollowUpQuestions: []string{"如何代回检查？"}, CheckingMethod: "将当前答案代回当前问项。"}
		}
		d, artifact := gradingPDFProjectionFixture(t, guides, sources)
		ctx := context.Background()
		before, err := d.Records.ListGradingAssessmentItems(ctx, artifact.AgentName, artifact.JobID)
		if err != nil {
			t.Fatal(err)
		}
		renderer := &finalReplyPDFRenderer{}
		d.Renderer = renderer
		printed, replay, err := d.PrepareGradingFinalArtifactPDFExact(ctx, artifact.AgentName, artifact.ArtifactID, artifact.ArtifactDigest, "作业批改结果")
		if err != nil || replay || strings.Count(renderer.markdown, "教材给出的推算过程：") != 1 {
			t.Fatalf("public compound source projection: replay=%v err=%v common_count=%d", replay, err, strings.Count(renderer.markdown, "教材给出的推算过程："))
		}
		for _, text := range current {
			if !strings.Contains(renderer.markdown, strings.TrimSpace(text)) && !strings.Contains(renderer.markdown, strings.TrimSpace(weeklySheetQuestionMarkdown(text))) {
				encoded, _ := json.Marshal(text)
				if !strings.Contains(renderer.markdown, string(encoded[1:len(encoded)-1])) {
					t.Errorf("public PDF lost the complete current question: %s", text)
				}
			}
		}
		again, replay, err := d.PrepareGradingFinalArtifactPDFExact(ctx, artifact.AgentName, artifact.ArtifactID, artifact.ArtifactDigest, "作业批改结果")
		if err != nil || !replay || renderer.calls != 1 || !bytes.Equal(printed.Render.Payload, again.Render.Payload) || printed.Artifact.CanonicalMarkdown != artifact.CanonicalMarkdown {
			t.Fatalf("compound PDF changed frozen source or replay: calls=%d replay=%v err=%v", renderer.calls, replay, err)
		}
		after, err := d.Records.ListGradingAssessmentItems(ctx, artifact.AgentName, artifact.JobID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("compound projection changed persisted assessments: %v", err)
		}
	})
}

// 通过正式来源和最终化构造临时真实库；模型与投递边界不参与本组领域验证。
func gradingPDFProjectionFixture(t *testing.T, guides []ParentTeachingGuide, sources ...[]RecognizedQuestion) (Deps, k12.GradingFinalArtifact) {
	t.Helper()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	ctx := context.Background()
	d, store := newPipeline(t, nil, nil, nil)
	o := &GradingOrchestrator{deps: d, runDir: t.TempDir()}
	repo := &PageAssetRepository{Records: store}
	imageBytes := validPNGFixture(t, "pdf-guide-source")
	asset, err := repo.Persist(ctx, "guardian-pdf-guide", "mingming", imageBytes)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &ImageTaskCoordinator{Records: store, PageAssets: repo, Classifier: &imageTaskClassifierStub{result: ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"source content"}, Confidence: 1}}, ResolveRoute: imageTaskRouteForTest}
	input := testCreateImageTaskInput()
	input.OwnerScope, input.SourceRef = "guardian-pdf-guide", "pdf-guide-source"
	input.SourceAssetRefs = []string{asset.Metadata.PageAssetID}
	view, _, err := coordinator.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	job, err := k12.NewGradingJobRecord("mingming", "session-pdf-guide", k12.GradingJobFields{SubmissionID: "submission-pdf-guide", SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", view.Dispatch.DispatchID, 1), ConfirmedVersion: 1, ConfirmationState: k12.GradingConfirmationConfirmed, AnchorState: k12.GradingAnchorLocated, ModelSnapshot: k12.GradingModelSnapshot{Provider: "provider-a", Model: "model-a", Route: "provider-a/model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	job.Status = k12.GradingStageCompleted
	if _, err = store.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	refs, err := json.Marshal(input.SourceAssetRefs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB().Exec(`INSERT INTO k12_homework_submissions(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at) VALUES('submission-pdf-guide',?,'mingming','mingming','desktop','pdf-guide-source',?,'completed_homework','completed',?,'pdf-guide-homework',100,100)`, view.Dispatch.DispatchID, string(refs), job.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB().Exec(`UPDATE k12_image_task_dispatches SET task_intent='completed_homework',target_object_type='homework_submission',target_object_id='submission-pdf-guide' WHERE dispatch_id=?`, view.Dispatch.DispatchID); err != nil {
		t.Fatal(err)
	}
	var snapshot k12.ProblemAttemptSnapshot
	if len(sources) > 0 {
		parent := sources[0][0]
		snapshot.Problems = append(snapshot.Problems, k12.Problem{ProblemID: parent.ProblemID, AgentName: "mingming", SubmissionID: "submission-pdf-guide", PageAssetID: asset.Metadata.PageAssetID, Ordinal: 0, ProblemKind: k12.ProblemKindCompoundParent, Subject: "数学", StemRaw: parent.RawTranscription, StemMarkdown: parent.CanonicalMarkdown, CanonicalVersion: 1, CreatedAt: 100, UpdatedAt: 100})
	}
	for index := range guides {
		id := fmt.Sprintf("pdf-guide-%d", index+1)
		stem := fmt.Sprintf("1+%d=", index+1)
		problem := k12.Problem{ProblemID: id, AgentName: "mingming", SubmissionID: "submission-pdf-guide", PageAssetID: asset.Metadata.PageAssetID, Ordinal: index, ProblemKind: k12.ProblemKindStandalone, SourceNumberPath: []string{fmt.Sprint(index + 1)}, DisplayLabel: fmt.Sprint(index + 1), Subject: "数学", StemRaw: stem, StemMarkdown: stem, CanonicalVersion: 1, CreatedAt: 100, UpdatedAt: 100}
		if len(sources) > 0 {
			current := sources[0][index+1]
			id = current.ProblemID
			problem.ProblemID, problem.ParentProblemID, problem.ProblemKind = id, current.ParentProblemID, k12.ProblemKindSubproblem
			problem.Ordinal, problem.StemRaw, problem.StemMarkdown = index+1, current.RawTranscription, current.CanonicalMarkdown
			problem.SourceNumberPath, problem.DisplayLabel, problem.SourceSectionLabel = nil, "", "3 分数除法"
			problem.SourceSectionPath = []string{"3"}
			problem.SubproblemNo = fmt.Sprint(index + 1)
			problem.SystemSectionOrdinal = index + 1
			problem.SystemDisplayLabel = fmt.Sprintf("第 %d 题（系统序号）", index+1)
		}
		snapshot.Problems = append(snapshot.Problems, problem)
		snapshot.Attempts = append(snapshot.Attempts, k12.Attempt{AttemptID: "attempt-" + id, AgentName: "mingming", SubmissionID: "submission-pdf-guide", ProblemID: id, AnswerState: "present", AnswerRaw: "0", AnswerMarkdown: "0", ConfirmedVersion: 1, InputDigest: "input-" + id, CreatedAt: 100, UpdatedAt: 100})
	}
	if err = store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	questions, err := RecognizedQuestionsFromProblemAttemptSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	questions = RecognizedQuestionsForAssessment(questions)
	for index, question := range questions {
		guide := guides[index]
		item := gradingAssessmentCanonicalResult(PhotoGradeItem{Recognized: question, Status: PhotoWrong, Grade: GradeResult{Solution: guide.Answer, Outcome: GradeOutcome{Verdict: VerdictDisagree, WrongStep: "原式计算错误", ErrorCause: "未按原式计算"}}, ParentGuide: &guide})
		invocations := make(map[k12.GradingItemOperation]string)
		for _, operation := range []k12.GradingItemOperation{k12.GradingItemOperationSolve, k12.GradingItemOperationGrade, k12.GradingItemOperationParentGuide} {
			invocation, _, prepareErr := store.PrepareGradingItemInvocation(ctx, k12.GradingItemInvocation{InvocationID: question.ProblemID + "-" + string(operation), AgentName: "mingming", JobID: job.RecordID, ProblemID: question.ProblemID, AttemptID: question.AttemptID, Operation: operation, OperationAttempt: 1, RequestDigest: "request-" + question.ProblemID + "-" + string(operation), InputRevision: 1, InputDigest: question.InputDigest, RouteSnapshot: k12.GradingModelSnapshot{Provider: "provider-a", Model: "model-a", Route: "provider-a/model-a"}, CreatedAt: 150})
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			if _, err = store.MarkGradingItemInvocationSent(ctx, "mingming", invocation.InvocationID); err != nil {
				t.Fatal(err)
			}
			payload, marshalErr := json.Marshal(map[string]any{"fixture_operation": operation, "answer": guide.Answer})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err = store.MarkGradingItemInvocationSucceeded(ctx, "mingming", invocation.InvocationID, modelInvocationDigest(payload), string(payload)); err != nil {
				t.Fatal(err)
			}
			invocations[operation] = invocation.InvocationID
		}
		raw, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, _, err = store.CommitGradingAssessmentItem(ctx, k12.GradingAssessmentItem{AgentName: "mingming", JobID: job.RecordID, ProblemID: question.ProblemID, AttemptID: question.AttemptID, ConfirmedVersion: 1, InputRevision: 1, StructureVersion: 1, InputDigest: question.InputDigest, Status: k12.GradingAssessmentWrong, ResultJSON: string(raw), ResultDigest: modelInvocationResultDigest(item), SolveInvocationID: invocations[k12.GradingItemOperationSolve], GradeInvocationID: invocations[k12.GradingItemOperationGrade], ParentGuideInvocationID: invocations[k12.GradingItemOperationParentGuide], ProjectionStatus: k12.GradingProjectionCommitted, CreatedAt: 200}, k12storage.GradingAssessmentEffects{}); err != nil {
			t.Fatal(err)
		}
	}
	current, err := d.GetGradingJob(ctx, "mingming", job.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := o.finalizeGradingPage(ctx, &gradingRun{questions: questions, result: &PhotoGradeResult{AnnotatedImage: &RenderedPhoto{Data: imageBytes, MIME: "image/png"}}}, current)
	if err != nil {
		t.Fatal(err)
	}
	return d, artifact
}
