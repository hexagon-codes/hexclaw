package usecase

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
)

const practiceReturnOnePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

type practiceReturnGradingFake struct {
	started        StartPhotoGradingInput
	job            GradingJobView
	result         PhotoGradeResult
	starts         int
	runs           int
	resultUnloaded bool
	runJobID       string
}

func (f *practiceReturnGradingFake) StartPhotoGradingJob(
	_ context.Context,
	in StartPhotoGradingInput,
) (GradingJobView, bool, error) {
	f.started = in
	f.starts++
	return f.job, true, nil
}

func (f *practiceReturnGradingFake) RunGradingJob(
	_ context.Context,
	jobID string,
) (GradingJobView, error) {
	f.runs++
	f.runJobID = jobID
	f.resultUnloaded = false
	return f.job, nil
}

func (f *practiceReturnGradingFake) PhotoResult(string) (PhotoGradeResult, bool) {
	if f.resultUnloaded {
		return PhotoGradeResult{}, false
	}
	questions := make([]RecognizedQuestion, len(f.result.Items))
	for i := range f.result.Items {
		questions[i] = f.result.Items[i].Recognized
	}
	refs := practiceQuestionReferences(f.started.Photo.PracticeReferences, f.started.Photo.PracticePaperSize, questions)
	for i, ref := range refs {
		if ref != nil {
			f.result.Items[i].PracticeItemID = ref.ItemID
			f.result.Items[i].PracticeProblemID = ref.PracticeProblemID
		}
	}
	return f.result, true
}

func (f *practiceReturnGradingFake) RecognizedQuestionsForOwner(
	context.Context,
	string,
	string,
) ([]RecognizedQuestion, bool) {
	return nil, false
}

func TestPracticeReturnRegradeCoordinator_AppliesClearResultsAndPersistsAnnotatedGuide(t *testing.T) {
	d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, &fakeInsights{})
	setID := seedRegradePaper(t, d, "")
	set, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	ret := set.Fields.ReturnAssets[0]
	route := k12.GradingModelSnapshot{
		Provider: "provider-a",
		Model:    "vision-a",
		Route:    "provider-a/vision-a",
	}
	annotated, err := base64.StdEncoding.DecodeString(practiceReturnOnePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	grading := &practiceReturnGradingFake{
		job: GradingJobView{
			Record: &records.AgentRecord{
				RecordID: "grade-return-1",
				Status:   k12.GradingStageCompleted,
			},
			Fields: k12.GradingJobFields{ModelSnapshot: route},
		},
		result: PhotoGradeResult{
			Mode:          PhotoModeGrade,
			TaskIntent:    PhotoTaskCompletedHomework,
			ResultSurface: PhotoSurfaceAnnotatedHomework,
			Items: []PhotoGradeItem{
				{Status: PhotoCorrect, Recognized: RecognizedQuestion{SourceNumberPath: []string{"2"}, DisplayLabel: "2"}},
				{
					Status: PhotoWrong, Recognized: RecognizedQuestion{SourceNumberPath: []string{"1"}, DisplayLabel: "1"},
					ParentGuide: &ParentTeachingGuide{
						Answer:           "1.82",
						GradeLevelMethod: "先把小数乘法看成整数乘法，再点小数点。",
					},
				},
			},
			AnnotatedImage: &RenderedPhoto{Data: annotated, MIME: "image/png"},
			Markdown:       "## 批改结果\n\n第 2 题需要讲解。",
		},
	}
	coordinator := &PracticeReturnRegradeCoordinator{
		Deps:    &d,
		Grading: grading,
	}

	if err := coordinator.Process(context.Background(), "mingming", setID, ret.ReturnID); err != nil {
		t.Fatalf("自动复批: %v", err)
	}
	got, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	projected := got.Fields.ReturnAssets[0]
	if projected.RegradeJobID != "grade-return-1" ||
		projected.RegradeStatus != k12.PracticeRegradeCompleted ||
		projected.RouteSnapshot != route ||
		projected.AnnotatedAssetID == "" ||
		projected.ResultMarkdown != grading.result.Markdown ||
		len(projected.UnresolvedItemIDs) != 0 {
		t.Fatalf("自动复批投影不完整: %+v", projected)
	}
	if owner, ok := assetstore.OwnerOf(projected.AnnotatedAssetID); !ok || owner != "mingming" {
		t.Fatalf("批注图未按 owner 内容寻址保存: %q", projected.AnnotatedAssetID)
	}
	if grading.started.SourceKind != PracticeReturnGradingSourceKind ||
		grading.started.SourceKey != setID+":"+ret.ReturnID ||
		grading.started.Photo.TaskIntent != PhotoTaskCompletedHomework {
		t.Fatalf("复批任务没有冻结为练习回传语义: %+v", grading.started)
	}
	refs := grading.started.Photo.PracticeReferences
	if len(refs) != 2 || grading.started.Photo.PracticePaperSize != 2 || refs[0].ItemID != "item-a" ||
		refs[0].PracticeProblemID != set.Fields.Items[0].PracticeProblemID ||
		refs[0].QuestionMarkdown != "3.8×3=?" || refs[0].ExpectedAnswerMarkdown != "11.4" {
		t.Fatalf("missing frozen practice references: %+v", grading.started.Photo)
	}
	for index, item := range got.Fields.Items {
		if item.ResultCorrect == nil ||
			*item.ResultCorrect != (index == 1) ||
			item.ResultEvidence != k12.PracticeResultSystemVerified {
			t.Fatalf("题 %d 自动结论/证据错误: %+v", index, item)
		}
	}

	// 回传投影可能落后于独立恢复的持久 Job；未知态只同步，完成态只回放结果。
	jobRecord, err := k12.NewGradingJobRecord("mingming", "", k12.GradingJobFields{
		SubmissionID: "return-submission", SourceKind: PracticeReturnGradingSourceKind,
		IdempotencyKey: "return-recovery", ModelSnapshot: route,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobRecord.RecordID = grading.job.Record.RecordID
	jobRecord.Status = k12.GradingStageOutcomeUnknown
	if _, err := d.Records.Put(context.Background(), jobRecord); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.updateProjection(context.Background(), "mingming", setID, ret.ReturnID,
		practiceReturnRegradeProjection{JobID: jobRecord.RecordID, Status: k12.PracticeRegradeFailedRetryable}); err != nil {
		t.Fatal(err)
	}
	starts, runs := grading.starts, grading.runs
	if recovered, err := coordinator.Recover(context.Background(), []string{"mingming"}); err != nil || recovered != 1 {
		t.Fatalf("recover stale return projection: count=%d err=%v", recovered, err)
	}
	unknown, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Fields.ReturnAssets[0].RegradeStatus != k12.PracticeRegradeOutcomeUnknown ||
		grading.starts != starts || grading.runs != runs {
		t.Fatalf("unknown return must only synchronize persisted status: return=%+v starts=%d runs=%d",
			unknown.Fields.ReturnAssets[0], grading.starts, grading.runs)
	}
	persisted, err := d.GetGradingJob(context.Background(), "mingming", jobRecord.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{
		k12.GradingStageFailedRetryable, k12.GradingStageQueued, k12.GradingStageNormalizing,
		k12.GradingStageRecognizing, k12.GradingStageAwaitingConfirmation, k12.GradingStageAssessing,
		k12.GradingStageRendering, k12.GradingStageProjecting, k12.GradingStageCompleted,
	} {
		persisted, err = d.saveGradingJob(context.Background(), persisted, status)
		if err != nil {
			t.Fatal(err)
		}
	}
	grading.resultUnloaded = true
	if recovered, err := coordinator.Recover(context.Background(), []string{"mingming"}); err != nil || recovered != 1 {
		t.Fatalf("recover completed return projection: count=%d err=%v", recovered, err)
	}
	recovered, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Fields.ReturnAssets[0].RegradeStatus != k12.PracticeRegradeCompleted ||
		recovered.Fields.ReturnAssets[0].RegradeJobID != jobRecord.RecordID ||
		!reflect.DeepEqual(recovered.Fields.Items, got.Fields.Items) ||
		grading.starts != starts || grading.runs != runs+1 || grading.runJobID != jobRecord.RecordID {
		t.Fatalf("completed return must load the original terminal job once: return=%+v starts=%d runs=%d",
			recovered.Fields.ReturnAssets[0], grading.starts, grading.runs)
	}
	if count, err := coordinator.Recover(context.Background(), []string{"mingming"}); err != nil || count != 0 {
		t.Fatalf("completed return must not be recovered again: count=%d err=%v", count, err)
	}
	replayed, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Record.Version != recovered.Record.Version || grading.starts != starts || grading.runs != runs+1 {
		t.Fatalf("completed recovery must not resubmit results: version=%d starts=%d runs=%d",
			replayed.Record.Version, grading.starts, grading.runs)
	}
}

func TestPracticeReturnRegradeCoordinator_OnlyProjectsTrueUncertainty(t *testing.T) {
	d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, &fakeInsights{})
	setID := seedRegradePaper(t, d, "", true)
	set, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	ret := set.Fields.ReturnAssets[0]
	// 自动模式只冻结候选，照片实际覆盖不能预先标成全卷。
	input := PracticeReturnInput{ReturnID: ret.ReturnID, AssetID: ret.AssetID, AutoMatch: true}
	set, err = d.SubmitReturns(context.Background(), "mingming", setID, []PracticeReturnInput{input})
	if err != nil || len(set.Fields.ReturnAssets[0].ItemIDs) != 0 || len(set.Fields.ReturnAssets[0].CandidateItemIDs) != 2 {
		t.Fatalf("automatic matching must freeze candidates without coverage: set=%+v err=%v", set, err)
	}
	grading := &practiceReturnGradingFake{
		job: GradingJobView{
			Record: &records.AgentRecord{
				RecordID: "grade-return-review",
				Status:   k12.GradingStageCompleted,
			},
			Fields: k12.GradingJobFields{ModelSnapshot: k12.GradingModelSnapshot{
				Provider: "p", Model: "m", Route: "p/m",
			}},
		},
		result: PhotoGradeResult{
			Items: []PhotoGradeItem{
				{Status: PhotoCorrect, Recognized: RecognizedQuestion{SourceNumberPath: []string{"1"}, DisplayLabel: "1"}},
				{Status: PhotoCorrect, Recognized: RecognizedQuestion{SourceNumberPath: []string{"99"}, DisplayLabel: "99"}},
			},
			Markdown: "第 2 题看不清，需要家长核对。",
		},
	}
	coordinator := &PracticeReturnRegradeCoordinator{Deps: &d, Grading: grading}

	if err := coordinator.Process(context.Background(), "mingming", setID, ret.ReturnID); err != nil {
		t.Fatalf("自动复批: %v", err)
	}
	got, err := d.GetPracticeSet(context.Background(), "mingming", setID)
	if err != nil {
		t.Fatal(err)
	}
	projected := got.Fields.ReturnAssets[0]
	if projected.RegradeStatus != k12.PracticeRegradeNeedsReview ||
		len(projected.UnresolvedItemIDs) != 0 ||
		len(projected.ItemIDs) != 1 || projected.ItemIDs[0] != got.Fields.Items[0].ItemID {
		t.Fatalf("只应降级真实不确定题: %+v", projected)
	}
	if got.Fields.Items[0].ResultCorrect == nil || !*got.Fields.Items[0].ResultCorrect {
		t.Fatalf("清晰题不应被同批不确定题阻断: %+v", got.Fields.Items[0])
	}
	if got.Fields.Items[1].ResultCorrect != nil || got.Fields.Items[1].Returned {
		t.Fatalf("不确定题不得猜测结论: %+v", got.Fields.Items[1])
	}
	replayed, err := d.SubmitReturns(context.Background(), "mingming", setID, []PracticeReturnInput{input})
	if err != nil || replayed.Record.Version != got.Record.Version || len(replayed.Fields.ReturnAssets) != 1 {
		t.Fatalf("automatic return replay must preserve the original batch: err=%v", err)
	}
}
