package usecase

import (
	"context"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func creativeDisclosureGenerationFixture() (CreativeWorkView, ImageTaskResult, string) {
	source := k12.CreativeWorkSourceSnapshot{
		WorkType: k12.WorkTypeWriting, DisplayName: "writing", ContentMarkdown: "source text",
		SourceAssetID: "asset://child/source.png", OCRRaw: "source text", OCRVersion: 1,
		OCRDigest: "sha256:source-text", ContentConfirmedAt: 1,
	}
	initial := k12.WorkFeedbackGeneration{
		GenerationID: "initial-feedback", WorkID: "work-1", AgentName: "child", GenerationNo: 1,
		Status: k12.WorkFeedbackSucceeded, Source: source,
	}
	latest := k12.WorkFeedbackGeneration{
		GenerationID: "current-feedback", WorkID: "work-1", AgentName: "child", GenerationNo: 2,
		Status: k12.WorkFeedbackSucceeded, Source: source,
		Feedback: &k12.WorkFeedback{ProjectionMarkdown: "frozen feedback"},
	}
	view := CreativeWorkView{
		Record:          &records.AgentRecord{AgentName: "child", RecordID: "work-1"},
		Fields:          k12.CreativeWorkFields{WorkType: k12.WorkTypeWriting, SourceIntakeID: "intake-1"},
		GenerationState: k12.CreativeWorkGenerationState{Initial: &initial, Latest: &latest},
	}
	actualInitial, actualLatest := initial, latest
	result := ImageTaskResult{
		Kind: "creative",
		Dispatch: k12.ImageTaskDispatch{
			AgentName: "child", DispatchID: "dispatch-1", TargetObjectID: "intake-1", SourceDigest: "sha256:source",
			OperationRouteRequest: k12.ImageTaskRouteSnapshot{MessageLocale: "zh-CN"},
		},
		Creative: &k12.CreativeWorkIntake{
			AgentName: "child", IntakeID: "intake-1", DispatchID: "dispatch-1", SourceDigest: "sha256:source",
			PromotedWorkID: "work-1", PromotedGenerationID: "initial-feedback",
		},
		CreativeWork: &CreativeWorkView{
			Record:          &records.AgentRecord{AgentName: "child", RecordID: "work-1"},
			GenerationState: k12.CreativeWorkGenerationState{Initial: &actualInitial, Latest: &actualLatest},
		},
		OperationReceipts: []ImageTaskOperationReceipt{{
			Operation: "work_feedback", InvocationID: "current-invocation", Status: "succeeded",
			ExecutionKind: string(k12.GradingExecutionProvider), CanonicalInputDigest: "sha256:source",
			Provider: "provider", Model: "model", ResultDigest: "sha256:current-result",
		}},
	}
	return view, result, "frozen canonical"
}

func TestCreativeWorkDeliveryDisclosureUsesInitialPromotionAndCurrentGeneration(t *testing.T) {
	for _, initialGeneration := range []bool{true, false} {
		name := "new_generation"
		if initialGeneration {
			name = "initial_generation"
		}
		t.Run(name, func(t *testing.T) {
			view, result, canonical := creativeDisclosureGenerationFixture()
			beforeView, beforeResult, _ := creativeDisclosureGenerationFixture()
			if initialGeneration {
				view.GenerationState.Latest.GenerationID = view.GenerationState.Initial.GenerationID
				result.CreativeWork.GenerationState.Latest.GenerationID = result.Creative.PromotedGenerationID
				beforeView.GenerationState.Latest.GenerationID = beforeView.GenerationState.Initial.GenerationID
				beforeResult.CreativeWork.GenerationState.Latest.GenerationID = beforeResult.Creative.PromotedGenerationID
			}
			ctx := WithCreativeWorkResultDeliveryDisclosure(context.Background(), view, result, canonical)
			locale, disclosure := CreativeWorkDeliveryDisclosure(ctx, canonical)
			if locale != "zh-CN" || disclosure == "" {
				t.Fatalf("same-source completed generation lost its execution receipt: locale=%q eligible=%v", locale, disclosure != "")
			}
			if locale, disclosure := CreativeWorkDeliveryDisclosure(ctx, canonical+" changed"); locale != "" || disclosure != "" {
				t.Fatal("execution receipt was reused for another canonical result")
			}
			if !reflect.DeepEqual(view, beforeView) || !reflect.DeepEqual(result, beforeResult) {
				t.Fatal("disclosure changed frozen business input")
			}
		})
	}
}

func TestCreativeWorkDeliveryDisclosureRejectsUnprovenGeneration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreativeWorkView, *ImageTaskResult)
	}{
		{"wrong_work", func(_ *CreativeWorkView, r *ImageTaskResult) { r.CreativeWork.Record.RecordID = "other-work" }},
		{"wrong_current_work", func(v *CreativeWorkView, _ *ImageTaskResult) { v.GenerationState.Latest.WorkID = "other-work" }},
		{"wrong_initial_owner", func(v *CreativeWorkView, _ *ImageTaskResult) { v.GenerationState.Initial.AgentName = "other-child" }},
		{"missing_initial", func(v *CreativeWorkView, _ *ImageTaskResult) { v.GenerationState.Initial = nil }},
		{"wrong_promoted_generation", func(_ *CreativeWorkView, r *ImageTaskResult) { r.Creative.PromotedGenerationID = "other-generation" }},
		{"wrong_initial_generation", func(_ *CreativeWorkView, r *ImageTaskResult) {
			r.CreativeWork.GenerationState.Initial.GenerationID = "other-generation"
		}},
		{"wrong_current_generation", func(_ *CreativeWorkView, r *ImageTaskResult) {
			r.CreativeWork.GenerationState.Latest.GenerationID = "other-generation"
		}},
		{"changed_original_asset", func(v *CreativeWorkView, _ *ImageTaskResult) {
			v.GenerationState.Latest.Source.SourceAssetID = "asset://child/other.png"
		}},
		{"changed_current_source", func(_ *CreativeWorkView, r *ImageTaskResult) {
			r.CreativeWork.GenerationState.Latest.Source.ContentMarkdown = "other source"
		}},
		{"changed_initial_source", func(_ *CreativeWorkView, r *ImageTaskResult) {
			r.CreativeWork.GenerationState.Initial.Source.OCRVersion++
		}},
		{"unknown_invocation", func(_ *CreativeWorkView, r *ImageTaskResult) { r.OperationReceipts[0].Status = "outcome_unknown" }},
		{"missing_result_digest", func(_ *CreativeWorkView, r *ImageTaskResult) { r.OperationReceipts[0].ResultDigest = "" }},
		{"wrong_receipt_source", func(_ *CreativeWorkView, r *ImageTaskResult) {
			r.OperationReceipts[0].CanonicalInputDigest = "sha256:other"
		}},
		{"missing_frozen_locale", func(_ *CreativeWorkView, r *ImageTaskResult) { r.Dispatch.OperationRouteRequest.MessageLocale = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view, result, canonical := creativeDisclosureGenerationFixture()
			tc.mutate(&view, &result)
			ctx := WithCreativeWorkResultDeliveryDisclosure(context.Background(), view, result, canonical)
			if locale, disclosure := CreativeWorkDeliveryDisclosure(ctx, canonical); locale != "" || disclosure != "" {
				t.Fatal("execution receipt escaped its owner, generation, source or confirmed-outcome boundary")
			}
		})
	}
}
