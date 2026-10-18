package usecase_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const conciseWritingResponse = "## 具体肯定\n“柳枝像绿色的丝带”让读者看到柳枝的样子。\n\n## 关键问题\n第二句缺少观察位置，读者不知道柳树在哪里。\n\n## 局部示范\n原句：柳枝像绿色的丝带。\n建议：保留这句，在前面补上实际观察到的位置。\n原因：位置清楚，读者更容易跟着看。\n\n## 孩子下一步\n只补一句真实的观察位置，读给家长听并指出自己补了哪里。"

// 旧公开投影仍可作为单参数函数值传递，新合同不改变既有调用者类型。
var _ func(k12.WorkFeedback) string = k12.ProjectWorkFeedbackMarkdown

type conciseWorkFeedbackSolver struct{ *fakeWorkFeedbackSolver }

func (f *conciseWorkFeedbackSolver) GenerateWorkFeedback(ctx context.Context, req usecase.WorkFeedbackRequest) (usecase.WorkFeedbackOutput, error) {
	out, err := f.fakeWorkFeedbackSolver.GenerateWorkFeedback(ctx, req)
	out.ProjectionContract = k12.WorkFeedbackProjectionWritingConciseV1
	return out, err
}

func TestWritingFeedbackConciseGenerationPreservesSourceAndFrozenHistory(t *testing.T) {
	d := newDataDeps(t)
	ctx := context.Background()
	workID := newWritingWork(t, d, "xiaoming")
	old := &fakeWorkFeedbackSolver{feedback: "## 可见证据\n柳枝的比喻清楚。\n\n## 先这样肯定\n柳枝写得具体。\n\n## 家长可以这样问或讲\n### 家长参考稿\n这是已冻结的完整参考内容，不能改写。\n\n## 下一次只试一个点\n补充真实位置。", skillStamp: "writing-feedback@1.0.1/test"}
	d.Solver = old
	first, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, "first-feedback")
	if err != nil || first.GenerationState.Latest == nil || first.GenerationState.Latest.Feedback == nil {
		t.Fatalf("first generation: %+v err=%v", first.GenerationState, err)
	}
	frozen := *first.GenerationState.Latest
	frozenJSON, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	current := &conciseWorkFeedbackSolver{&fakeWorkFeedbackSolver{feedback: conciseWritingResponse, skillStamp: "writing-feedback@1.0.2/test"}}
	d.Solver = current
	second, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, "concise-feedback")
	if err != nil || second.GenerationState.Latest == nil || second.GenerationState.Latest.Feedback == nil {
		t.Fatalf("second generation: %+v err=%v", second.GenerationState, err)
	}
	latest := second.GenerationState.Latest
	if latest.GenerationID == frozen.GenerationID || latest.GenerationNo != frozen.GenerationNo+1 ||
		!reflect.DeepEqual(latest.Source, frozen.Source) || !reflect.DeepEqual(latest.Feedback.EvidenceRefs, frozen.Feedback.EvidenceRefs) {
		t.Fatalf("new generation changed source or identity: old=%+v new=%+v", frozen, latest)
	}
	projection := latest.Feedback.ProjectionMarkdown
	previous := -1
	for _, heading := range []string{"## 具体肯定", "## 关键问题", "## 局部示范", "## 孩子下一步"} {
		at := strings.Index(projection, heading)
		if at <= previous || strings.Count(projection, heading) != 1 {
			t.Fatalf("wrong persisted section contract: %s", projection)
		}
		previous = at
	}
	if !strings.Contains(latest.Feedback.ParentGuidance, "原句：柳枝") || !strings.Contains(latest.Feedback.NextStep, "只补一句") || strings.Contains(projection, "## 家长可以这样问或讲") {
		t.Fatalf("new semantics were mapped to old sections: %+v", latest.Feedback)
	}
	readOld, err := d.Records.GetWorkFeedbackGeneration(ctx, "xiaoming", frozen.GenerationID)
	readOldJSON, marshalErr := json.Marshal(readOld)
	if err != nil || marshalErr != nil || string(readOldJSON) != string(frozenJSON) {
		t.Fatalf("frozen prior generation changed: err=%v marshal=%v old=%s new=%s", err, marshalErr, frozenJSON, readOldJSON)
	}
	invocation, err := d.Records.GetLatestWorkFeedbackInvocation(ctx, "xiaoming", workID, "work:"+workID+":version:"+latest.GenerationID+":feedback")
	if err != nil || !strings.Contains(invocation.ResultJSON, `"projection_contract":"writing-concise-v1"`) {
		t.Fatalf("response contract was not frozen with the physical result: %+v err=%v", invocation, err)
	}
	replayed, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, "concise-feedback")
	if err != nil || replayed.GenerationState.Latest.GenerationID != latest.GenerationID || current.calls != 1 || old.calls != 1 {
		t.Fatalf("same command regenerated or changed result: calls=%d/%d err=%v", old.calls, current.calls, err)
	}
}

func TestWritingFeedbackConciseRejectsMissingOrReorderedSections(t *testing.T) {
	for _, response := range []string{
		strings.Replace(conciseWritingResponse, "## 关键问题", "## 其他问题", 1),
		strings.Replace(conciseWritingResponse, "## 具体肯定", "## 局部示范", 1),
		strings.Split(conciseWritingResponse, "## 孩子下一步")[0],
	} {
		d := newDataDeps(t)
		workID := newWritingWork(t, d, "xiaoming")
		solver := &conciseWorkFeedbackSolver{&fakeWorkFeedbackSolver{feedback: response}}
		d.Solver = solver
		_, err := d.GenerateWorkFeedbackCommand(context.Background(), "xiaoming", workID, "bad-format")
		view, readErr := d.GetCreativeWork(context.Background(), "xiaoming", workID)
		if err == nil || readErr != nil || view.GenerationState.Current == nil || view.GenerationState.Current.Status != k12.WorkFeedbackFailed || view.GenerationState.Latest != nil || solver.calls != 1 {
			t.Fatalf("invalid response became successful feedback: view=%+v calls=%d err=%v read=%v", view.GenerationState, solver.calls, err, readErr)
		}
	}
}
