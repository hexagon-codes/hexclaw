package k12

import (
	"testing"
)

func TestRecognitionInitialReadFinalizationCountsOnlyActualCalls(t *testing.T) {
	runtime, result := initialReadFinalizationFixture(t)
	if err := validateRecognitionLayoutPlanFinalizationV2(runtime, result); err != nil {
		t.Fatalf("one whole-page call must finalize both covered targets: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*RecognitionLayoutPlanFinalizationResultV2)
	}{
		{"missing_target", func(r *RecognitionLayoutPlanFinalizationResultV2) {
			r.CandidateResults = r.CandidateResults[:1]
			r.CandidateResultCount = 1
		}},
		{"invented_call", func(r *RecognitionLayoutPlanFinalizationResultV2) {
			r.PhysicalResults = append(r.PhysicalResults, r.PhysicalResults[0])
			r.PhysicalResultCount = 2
		}},
		{"wrong_target_source", func(r *RecognitionLayoutPlanFinalizationResultV2) {
			r.CandidateResults[0].SourcePhysicalInvocationID = "modelphysical-22222222222222222222222222222222"
		}},
		{"old_protocol", func(r *RecognitionLayoutPlanFinalizationResultV2) { runtime.Header.InitialReadMode = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneRecognitionLayoutFinalizationTestResult(t, result)
			oldMode := runtime.Header.InitialReadMode
			tc.mutate(&changed)
			defer func() { runtime.Header.InitialReadMode = oldMode }()
			if err := validateRecognitionLayoutPlanFinalizationV2(runtime, changed); err == nil {
				t.Fatal("incomplete or fabricated evidence was accepted")
			}
		})
	}
}

func TestRecognitionInitialReadFinalizationKeepsIndependentReview(t *testing.T) {
	runtime, result := initialReadFinalizationFixture(t)
	targetID := runtime.AuthorizedPlan.Targets[0].TargetID
	unit, _ := RecognitionLayoutReviewUnitV1(1)
	request := RecognitionLayoutReviewBatchAuthorizationRequestV1{
		PlanDigest: runtime.AuthorizedPlan.AuthorizedPlanDigest, PhysicalUnit: unit,
		Members: []RecognitionLayoutReviewMemberAuthorizationV1{{
			AuthorizationID: "review-member", AuthorizationDigest: recognitionLayoutFinalizationTestDigest("member"), CandidateID: targetID, ReviewRound: 1,
		}},
		ImageDigest: recognitionLayoutFinalizationTestDigest("original-pixels"), ImageWidth: 20, ImageHeight: 10,
		PromptDigest: recognitionLayoutFinalizationTestDigest("independent-prompt"), RecognitionFormat: RecognitionLayoutCompactV4,
	}
	var err error
	request.InputDigest, err = RecognitionLayoutReviewBatchInputDigestV1(request)
	if err != nil {
		t.Fatal(err)
	}
	exact, _ := RecognitionLayoutTargetExactSetDigestV2([]string{targetID})
	runtime.ReviewBatches = []RecognitionLayoutReviewBatchAuthorizationV1{{
		RecognitionLayoutReviewBatchAuthorizationRequestV1: request,
		AuthorizationID: "review-batch", AuthorizationDigest: recognitionLayoutFinalizationTestDigest("batch"),
		OrderedTargetIDs: []string{targetID}, ExactSetDigest: exact, ReviewRound: 1,
	}}
	physical := RecognitionLayoutPhysicalResultEvidenceV2{
		PhysicalInvocationID: "modelphysical-22222222222222222222222222222222", PhysicalUnit: unit,
		PlanDigest: request.PlanDigest, CandidateExactSetDigest: exact,
		ResultDigest: recognitionLayoutFinalizationTestDigest("independent-result"), Attempt: 1,
	}
	result.PhysicalResults = append(result.PhysicalResults, physical)
	result.PhysicalResultCount = 2
	result.CandidateResults[0].SourcePhysicalInvocationID = physical.PhysicalInvocationID
	result.CandidateResults[0].SourcePhysicalUnit = physical.PhysicalUnit
	result.CandidateResults[0].SourcePhysicalResultDigest = physical.ResultDigest
	sealInitialReadFinalizationFixture(t, runtime, &result)
	if err := validateRecognitionLayoutPlanFinalizationV2(runtime, result); err != nil {
		t.Fatal(err)
	}
	changed := cloneRecognitionLayoutFinalizationTestResult(t, result)
	changed.CandidateResults[0].SourcePhysicalInvocationID = runtime.ManifestPhysicalInvocationID
	changed.CandidateResults[0].SourcePhysicalUnit = RecognitionPhysicalUnitWholePage
	changed.CandidateResults[0].SourcePhysicalResultDigest = runtime.ManifestResultDigest
	sealInitialReadFinalizationFixture(t, runtime, &changed)
	if err := validateRecognitionLayoutPlanFinalizationV2(runtime, changed); err == nil {
		t.Fatal("a re-sealed whole-page result bypassed its required independent review")
	}
}

func initialReadFinalizationFixture(t *testing.T) (RecognitionLayoutPlanRuntimeV2, RecognitionLayoutPlanFinalizationResultV2) {
	t.Helper()
	runtime, result := recognitionLayoutFinalizationDomainFixture(t, "running")
	plan := runtime.AuthorizedPlan
	plan.InitialReadMode, plan.RecognitionFormat, plan.Batches = RecognitionLayoutManifestWithContentV1, RecognitionLayoutCompactV4, nil
	for index := range plan.Targets {
		region := plan.Targets[index].Region
		plan.Targets[index].OriginalRegion = &region
	}
	var err error
	plan.AuthorizedPlanDigest, err = recognitionLayoutAuthorizedPlanDigestV2(*plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecognitionLayoutPlanV2(*plan); err != nil {
		t.Fatal(err)
	}
	runtime.Header.InitialReadMode = RecognitionLayoutManifestWithContentV1
	runtime.HeaderDigest, err = RecognitionLayoutPlanHeaderDigestV2(runtime.Header)
	if err != nil {
		t.Fatal(err)
	}
	result.PlanDigest = plan.AuthorizedPlanDigest
	result.PhysicalResults = result.PhysicalResults[:1]
	result.PhysicalResults[0].PlanDigest = runtime.HeaderDigest
	result.PhysicalResultCount = 1
	for i := range result.CandidateResults {
		result.CandidateResults[i].SourcePhysicalInvocationID = runtime.ManifestPhysicalInvocationID
		result.CandidateResults[i].SourcePhysicalUnit = RecognitionPhysicalUnitWholePage
		result.CandidateResults[i].SourcePhysicalResultDigest = runtime.ManifestResultDigest
	}
	sealInitialReadFinalizationFixture(t, runtime, &result)
	return runtime, result
}

func sealInitialReadFinalizationFixture(t *testing.T, runtime RecognitionLayoutPlanRuntimeV2, result *RecognitionLayoutPlanFinalizationResultV2) {
	t.Helper()
	result.FinalizationDigest = ""
	var err error
	result.CandidateResultsExactSetDigest, err = RecognitionLayoutCandidateResultsExactSetDigestV2(result.CandidateResults)
	if err != nil {
		t.Fatal(err)
	}
	result.PhysicalResultsExactSetDigest, err = RecognitionLayoutPhysicalResultsExactSetDigestV2(result.PhysicalResults)
	if err != nil {
		t.Fatal(err)
	}
	_, result.FinalizationDigest, err = CanonicalRecognitionLayoutPlanFinalizationV2(runtime.Header.ParentInvocationID, *result)
	if err != nil {
		t.Fatal(err)
	}
}
