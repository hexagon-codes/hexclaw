package usecase

import (
	"context"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageScopeRuntimeSurvivesColdRestore(t *testing.T) {
	o := newOrchestrator(t, &countingRecognizer{}, nil, nil)
	o.runDir = t.TempDir()
	job := startOrchestratorJob(t, o, "image-scope-persistence")
	run := o.runs[job.Record.RecordID]
	run.req.ImageScope = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, MessageIntent: "只做第1题"}
	run.questions = []RecognizedQuestion{{Question: "visible question", Subject: "语文", ImageSourceContext: &k12.RecognitionImageSourceContextV1{ScopeDigest: "frozen-scope", TargetIDs: []string{"target-1"}, Materials: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1}, Text: "shared source material", Region: k12.SourcePixelRegion{Width: 10, Height: 10}}}, Recoveries: []k12.RecognitionImageSourceRecoveryV1{{Gap: k12.RecognitionImageSourceGapV1{Order: 1, Kind: "instruction", Before: "visible before"}, State: "same_exercise_source_required"}}}}}
	if err := o.persistRun(job.Record.RecordID, run); err != nil {
		t.Fatal(err)
	}
	delete(o.runs, job.Record.RecordID)
	restored, err := o.ensureRun(context.Background(), job.Record.RecordID)
	if err != nil || !reflect.DeepEqual(restored.req.ImageScope, run.req.ImageScope) || !reflect.DeepEqual(restored.questions, run.questions) {
		t.Fatalf("scope/material restoration changed: %+v error=%v", restored, err)
	}
	old := recognizingInvocationDigest(run.req.Image, job.Fields.ModelSnapshot, k12.ModelRequestPolicySnapshot{}, run.req.InitialReadMode)
	scoped := recognizingInvocationDigest(run.req.Image, job.Fields.ModelSnapshot, k12.ModelRequestPolicySnapshot{}, run.req.InitialReadMode, k12.RecognitionImageScopeInputIdentityV1(run.req.ImageScope))
	if old == scoped {
		t.Fatal("scope is absent from recognition identity")
	}
}

func TestImageScopeExplicitActionAndLegacyPolicy(t *testing.T) {
	for _, tc := range []struct{ message, want string }{
		{"请解答这张图片中的题目", "solve"}, {"只做第1、2题，帮我解题", "solve"},
		{"不要解答，只批改", "grade"}, {"不要批改，只求解", "solve"},
		{"这是作业", ""}, {"先批改再解题", ""},
	} {
		if got := explicitImageHomeworkAction(tc.message); got != tc.want {
			t.Fatalf("%q action=%q want=%q", tc.message, got, tc.want)
		}
	}
	dispatch := k12.ImageTaskDispatch{TaskIntent: k12.ImageTaskIntentCompletedHomework, MessageIntent: "请解答这张图片中的题目"}
	if photoTaskIntentForImageDispatch(dispatch) != PhotoTaskCompletedHomework {
		t.Fatal("legacy dispatch was reinterpreted")
	}
	dispatch.ClassificationRouteSnapshot.ImageScope = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, Action: "solve", MessageIntent: dispatch.MessageIntent}
	if photoTaskIntentForImageDispatch(dispatch) != PhotoTaskBlankWorksheet || dispatch.TaskIntent != k12.ImageTaskIntentCompletedHomework {
		t.Fatal("explicit solve was lost or original image evidence was overwritten")
	}
	budget := recognitionLayoutInitialV2Budget()
	selected, err := trustedPhotoRecognitionCreationPolicy(budget, []byte("small source"), PhotoTaskBlankWorksheet, dispatch.ClassificationRouteSnapshot.ImageScope)
	if err != nil || selected.RecognitionPlanVersion != k12.RecognitionPlanVersionV2 {
		t.Fatalf("scope has no durable layout plan: %+v %v", selected, err)
	}
	legacy, err := trustedPhotoRecognitionCreationPolicy(budget, []byte("small source"), PhotoTaskBlankWorksheet)
	if err != nil || legacy.RecognitionPlanVersion != k12.RecognitionPlanVersionV1 {
		t.Fatal("legacy recognition policy changed")
	}
}

func TestImageScopeLatePrintedClippingTriggersLookupWithoutInventingGap(t *testing.T) {
	q := RecognizedQuestion{RawTranscription: "可见题干，我也要结\n校园，仿写一句话：", OCRSignals: []string{"印刷文字在右侧被裁切"}, ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, ScopeDigest: "unchanged-plan", TargetIDs: []string{"selected-target"}}}
	ensureUnlocalizedImageSourceGap(&q)
	if len(q.ImageSourceContext.Recoveries) != 1 || q.ImageSourceContext.Recoveries[0].Gap.Kind != "unlocalized" || q.ImageSourceContext.Recoveries[0].Gap.After != "" || q.ImageSourceContext.ScopeDigest != "unchanged-plan" {
		t.Fatal("late source risk was lost or a gap location was invented")
	}
	ensureUnlocalizedImageSourceGap(&q)
	if len(q.ImageSourceContext.Recoveries) != 1 {
		t.Fatal("source lookup was duplicated")
	}
	q.ImageSourceContext.Recoveries = nil
	q.OCRSignals = []string{"手写作答模糊看不清"}
	ensureUnlocalizedImageSourceGap(&q)
	if len(q.ImageSourceContext.Recoveries) != 0 {
		t.Fatal("student handwriting triggered reference-answer recovery")
	}
}
