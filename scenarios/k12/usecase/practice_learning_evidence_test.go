package usecase_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestLearningEvidencePersistsReportedContextWithoutChangingMasteryPolicy(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	d := newDataDeps(t)
	now := int64(2000000)
	d.Now = func() int64 { return now }
	ctx := context.Background()
	sourceID := seedSinglePracticeMistake(t, d, "")
	source, err := d.Records.Get(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.MarkRetried(ctx, sourceID, source.Version); err != nil {
		t.Fatal(err)
	}
	now += 3 * 86400
	item := verifiedItem("evidence-q1", "5÷0.5=?", "10")
	item.SourceProblemID = sourceID
	setID, _, err := d.CreatePracticeSet(ctx, "xiaoming", "learning-evidence", k12.PracticeSetFields{Title: "复测", SourceKind: k12.PracticeSourceManual, Items: []k12.PracticeItem{item}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.FinalizeBasket(ctx, "xiaoming", setID, "print", ""); err != nil {
		t.Fatal(err)
	}
	view, err := d.GetPracticeSet(ctx, "xiaoming", setID)
	if err != nil || len(view.Fields.Items[0].LearningEvidence) != 0 || len(view.Fields.ReturnAssets) != 0 {
		t.Fatalf("printing created learning events: %v", err)
	}
	asset := saveReturnAsset(t, "xiaoming")
	input := usecase.PracticeReturnInput{ReturnID: "context-return", AssetID: asset, ItemIDs: []string{item.ItemID}, AnswerContext: k12.LearningEvidencePrompted}
	view, err = d.SubmitReturns(ctx, "xiaoming", setID, []usecase.PracticeReturnInput{input})
	if err != nil {
		t.Fatal(err)
	}
	events := view.Fields.ReturnAssets[0].LearningEvidence
	if len(events) != 1 || events[0].Kind != k12.LearningEvidencePrompted || events[0].Source != "guardian_report" || events[0].EventID != input.ReturnID || events[0].OccurredAt != now {
		t.Fatalf("reported context lost provenance: %+v", events)
	}
	conflicting := input
	conflicting.AnswerContext = k12.LearningEvidenceIndependent
	if _, err := d.SubmitReturns(ctx, "xiaoming", setID, []usecase.PracticeReturnInput{conflicting}); err == nil {
		t.Fatal("same return was relabeled independent")
	}
	view, err = d.GradePracticeSetItems(ctx, "xiaoming", setID, []usecase.PracticeGradeResult{{ItemID: item.ItemID, ReturnID: input.ReturnID, Correct: true}})
	if err != nil {
		t.Fatal(err)
	}
	events = view.Fields.Items[0].LearningEvidence
	if len(events) != 2 || events[0].Kind != k12.LearningEvidencePrompted || events[1].Kind != k12.LearningEvidenceDelayed || events[1].BasisAt != 2000000 {
		t.Fatalf("prompted and delayed evidence conflated: %+v", events)
	}
	source, err = d.Records.Get(ctx, sourceID)
	if err != nil || source.Status != k12.StatusMastered {
		t.Fatalf("existing two-correct three-day policy changed: status=%s error=%v", source.Status, err)
	}
	reread, err := d.GetPracticeSet(ctx, "xiaoming", setID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reread.Fields.Items[0].LearningEvidence)
	prior, _ := json.Marshal(events)
	if string(raw) != string(prior) {
		t.Fatal("learning evidence did not survive independent SQLite readback")
	}
}

func TestLearningEvidenceLegacyRemainsUnknownAndIndependentRequiresReport(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	d := newDataDeps(t)
	ctx := context.Background()
	setID := finalizedReturnSet(t, d, "xiaoming")
	asset := saveReturnAsset(t, "xiaoming")
	view, err := d.SubmitReturns(ctx, "xiaoming", setID, []usecase.PracticeReturnInput{
		{ReturnID: "unknown-return", AssetID: asset, ItemIDs: []string{"q1"}},
		{ReturnID: "independent-return", AssetID: asset, ItemIDs: []string{"q2"}, AnswerContext: k12.LearningEvidenceIndependent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := k12.LearningEvidenceOrUnknown(view.Fields.ReturnAssets[0].LearningEvidence); len(got) != 1 || got[0].Kind != k12.LearningEvidenceUnknown || got[0].OccurredAt != 0 {
		t.Fatalf("unknown photo invented an event: %+v", got)
	}
	view, err = d.GradePracticeSetItems(ctx, "xiaoming", setID, []usecase.PracticeGradeResult{{ItemID: "q1", Correct: true}, {ItemID: "q2", ReturnID: "independent-return", Correct: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Fields.Items[0].LearningEvidence) != 0 || len(view.Fields.Items[1].LearningEvidence) != 1 || view.Fields.Items[1].LearningEvidence[0].Source != "guardian_report" {
		t.Fatal("unknown and explicitly reported independent context were conflated")
	}
}
