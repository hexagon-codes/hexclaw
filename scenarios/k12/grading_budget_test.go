package k12

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGradingBudgetRequiredImageContractIsOptionalAndFrozen(t *testing.T) {
	legacy := frozenBudgetFixture()
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		t.Fatal("invalid legacy budget")
	}
	if _, exists := fields["image_output_contract"]; exists {
		t.Fatal("old snapshot acquired an output contract")
	}
	before := append([]byte(nil), raw...)
	decoded, err := ParseStoredGradingBudgetSnapshot(raw)
	if err != nil || decoded.RequiresAnnotatedImage() {
		t.Fatalf("old budget semantics changed: %v", err)
	}
	after, err := json.Marshal(decoded)
	if err != nil || string(after) != string(before) {
		t.Fatal("old frozen bytes changed")
	}
	current := legacy
	current.ImageOutputContract = GradingImageOutputContractAnnotatedHomeworkV1
	current.StageSeconds.Locating = 120
	raw, err = json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ParseStoredGradingBudgetSnapshot(raw)
	if err != nil || !stored.RequiresAnnotatedImage() || !reflect.DeepEqual(stored, current) {
		t.Fatalf("new image contract did not round trip: %v", err)
	}
	for _, name := range []string{"unknown", "unfrozen", "over locating cap"} {
		t.Run(name, func(t *testing.T) {
			bad := current
			switch name {
			case "unknown":
				bad.ImageOutputContract = "unrecognized_contract"
			case "unfrozen":
				bad.PolicyVersion = 0
			case "over locating cap":
				bad.StageSeconds.Locating = 121
			}
			if bad.Validate() == nil {
				t.Fatal("invalid new contract was accepted")
			}
		})
	}
}

func frozenBudgetFixture() GradingBudgetSnapshot {
	return GradingBudgetSnapshot{
		PolicyVersion:          1,
		RecognitionPlanVersion: RecognitionPlanVersionV1,
		StageSeconds: GradingStageBudgets{
			Queued: 60, Normalizing: 60, Recognizing: 120,
			Locating: 60, Rendering: 60, Projecting: 60,
		},
		AssessingBuckets: []GradingAssessingBudgetBucket{
			{MaxProblems: 1, Seconds: 90},
			{MaxProblems: 8, Seconds: 180},
			{MaxProblems: 16, Seconds: 300},
			{MaxProblems: 32, Seconds: 540},
		},
		ItemConcurrency: 2,
	}
}

func TestGradingBudgetSnapshotZeroIsStrictlyLegacyAndUnfrozen(t *testing.T) {
	var snapshot GradingBudgetSnapshot
	if snapshot.IsFrozen() {
		t.Fatal("policy_version=0 must remain legacy/unfrozen")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("zero legacy snapshot must remain readable: %v", err)
	}
	if _, ok := snapshot.StageBudgetSeconds(GradingStageAssessing, 1); ok {
		t.Fatal("unfrozen snapshot must not manufacture an assessing budget")
	}
}

func TestGradingBudgetSnapshotRequiresMeasuredOneEightSixteenThirtyTwoBuckets(t *testing.T) {
	valid := frozenBudgetFixture()
	if !valid.IsFrozen() {
		t.Fatal("positive policy version should be frozen")
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid frozen snapshot: %v", err)
	}

	missing32 := frozenBudgetFixture()
	missing32.AssessingBuckets = missing32.AssessingBuckets[:3]
	if err := missing32.Validate(); err == nil {
		t.Fatal("frozen policy missing the real 32-problem bucket must fail closed")
	}
	badConcurrency := frozenBudgetFixture()
	badConcurrency.ItemConcurrency = 0
	if err := badConcurrency.Validate(); err == nil {
		t.Fatal("frozen policy with zero item_concurrency must be rejected")
	}
	badStage := frozenBudgetFixture()
	badStage.StageSeconds.Recognizing = 0
	if err := badStage.Validate(); err == nil {
		t.Fatal("frozen policy with a zero automatic-stage budget must be rejected")
	}
	missingPlan := frozenBudgetFixture()
	missingPlan.RecognitionPlanVersion = 0
	if err := missingPlan.Validate(); err == nil {
		t.Fatal("new frozen policy without an explicit recognition plan must fail closed")
	}
	v1WithV2Parameters := frozenBudgetFixture()
	v1WithV2Parameters.PhysicalCallCapMillis = 120_000
	if err := v1WithV2Parameters.Validate(); err == nil {
		t.Fatal("recognition plan v1 must not carry v2 parameters")
	}
	v2 := frozenBudgetFixture()
	v2.RecognitionPlanVersion = RecognitionPlanVersionV2
	v2.RecognizingBuckets = RecognitionLayoutBudgetBucketsV2{
		UpTo1ProblemMillis: 1_001, UpTo8ProblemsMillis: 2_001,
		UpTo16ProblemsMillis: 3_001, UpTo32ProblemsMillis: 4_001,
	}
	v2.StageSeconds.Recognizing = 5
	v2.PhysicalCallCapMillis = 120_000
	v2.WorkerHardCap = 2
	v2.EffectiveConcurrency = 2
	if err := v2.Validate(); err != nil {
		t.Fatalf("domain tests may exercise complete v2 effective=2 policy: %v", err)
	}
	v2.RecognizingBuckets.UpTo16ProblemsMillis = 0
	if err := v2.Validate(); err == nil {
		t.Fatal("incomplete v2 recognizing buckets must fail closed")
	}
}

func TestGradingBudgetSnapshotSelectsFrozenStageAndQuestionBucket(t *testing.T) {
	snapshot := frozenBudgetFixture()
	for _, tc := range []struct {
		stage    string
		problems int
		want     int64
	}{
		{GradingStageQueued, 0, 60},
		{GradingStageRecognizing, 0, 120},
		{GradingStageAssessing, 1, 90},
		{GradingStageAssessing, 2, 180},
		{GradingStageAssessing, 8, 180},
		{GradingStageAssessing, 9, 300},
		{GradingStageAssessing, 17, 540},
	} {
		got, ok := snapshot.StageBudgetSeconds(tc.stage, tc.problems)
		if !ok || got != tc.want {
			t.Errorf("stage=%s problems=%d got=(%d,%v), want=(%d,true)",
				tc.stage, tc.problems, got, ok, tc.want)
		}
	}
	if _, ok := snapshot.StageBudgetSeconds(GradingStageAssessing, 33); ok {
		t.Fatal("unmeasured >32 problem count must fail closed")
	}
	if _, ok := snapshot.StageBudgetSeconds(GradingStageAwaitingConfirmation, 0); ok {
		t.Fatal("human wait state must have no automatic budget")
	}
}

func TestREGK12RecognitionPlanVersion20260808001CopiesSelectedRecognizingSeconds(t *testing.T) {
	caller := frozenBudgetFixture()
	caller.StageSeconds.Recognizing = 777

	selectedOrdinary := frozenBudgetFixture()
	selectedOrdinary.StageSeconds.Recognizing = 120
	got := caller.WithRecognitionPolicyFrom(selectedOrdinary)
	if got.RecognitionPlanVersion != RecognitionPlanVersionV1 ||
		got.StageSeconds.Recognizing != 120 ||
		!got.RecognizingBuckets.IsZero() || got.PhysicalCallCapMillis != 0 ||
		got.WorkerHardCap != 0 || got.EffectiveConcurrency != 0 {
		t.Fatalf("ordinary selected recognition policy was not copied exactly: %+v", got)
	}
}
