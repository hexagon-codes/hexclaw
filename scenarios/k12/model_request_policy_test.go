package k12

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApprovedRecognizingRequestPolicyIsStableAndScoped(t *testing.T) {
	policy := ApprovedRecognizingRequestPolicy()
	if !policy.IsApprovedRecognizing() || policy.Digest() == "" {
		t.Fatalf("approved policy invalid: %+v", policy)
	}
	mutated := policy
	mutated.Thinking = "on"
	if mutated.Digest() == policy.Digest() {
		t.Fatal("policy mutation must change its digest")
	}

	route := GradingModelSnapshot{
		Provider:                 "hexclaw-gpt",
		Model:                    RecognizingPolicyModel,
		Route:                    "hexclaw-gpt/" + RecognizingPolicyModel,
		RecognizingRequestPolicy: policy,
	}
	if err := ValidateModelInvocationRequestPolicy(
		GradingStageRecognizing,
		route,
		policy,
	); err != nil {
		t.Fatalf("approved recognizing policy rejected: %v", err)
	}
	if err := ValidateModelInvocationRequestPolicy(
		GradingStageLocating,
		route,
		policy,
	); err == nil {
		t.Fatal("locating must not inherit recognizing request policy")
	}
	if err := ValidateModelInvocationRequestPolicy(
		GradingStageLocating,
		route,
		ModelRequestPolicySnapshot{},
	); err != nil {
		t.Fatalf("policy-free locating rejected: %v", err)
	}
}

func TestZeroRecognizingPolicyIsOmittedFromSnapshotJSON(t *testing.T) {
	raw, err := json.Marshal(GradingModelSnapshot{
		Provider: "provider",
		Model:    "vision-model",
		Route:    "provider/vision-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "recognizing_request_policy") {
		t.Fatalf("zero recognizing policy leaked into JSON: %s", raw)
	}
}

func TestConfiguredRecognitionPolicyRemainsStageScoped(t *testing.T) {
	route := GradingModelSnapshot{Provider: "cloud-gpt", Model: "gpt-6-sol", RecognizingRequestPolicy: ConfiguredRecognizingRequestPolicy()}
	for _, stage := range []string{"solve_generate", "solve_verify", "grading"} {
		if err := ValidateModelInvocationRequestPolicy(stage, route, route.RecognizingRequestPolicy); err == nil {
			t.Fatalf("recognition policy accepted for %s", stage)
		}
	}
	if err := ValidateModelInvocationRequestPolicy(GradingStageRecognizing, route, ModelRequestPolicySnapshot{}); err == nil {
		t.Fatal("new invocation silently lost frozen policy")
	}
	route.RecognizingRequestPolicy = ModelRequestPolicySnapshot{}
	if err := ValidateModelInvocationRequestPolicy(GradingStageRecognizing, route, ModelRequestPolicySnapshot{}); err != nil {
		t.Fatalf("legacy zero policy rejected: %v", err)
	}
}

func TestRecognizingAndLocatingPolicyFollowFrozenContractNotModelName(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		policy      ModelRequestPolicySnapshot
		valid       bool
	}{
		{"named_model_without_policy", RecognizingPolicyModel, ModelRequestPolicySnapshot{}, true},
		{"custom_model_without_policy", "school-reader", ModelRequestPolicySnapshot{}, true},
		{"named_model_configured_policy", RecognizingPolicyModel, ConfiguredRecognizingRequestPolicy(), true},
		{"custom_model_configured_policy", "school-reader", ConfiguredRecognizingRequestPolicy(), true},
		{"legacy_explicit_contract", RecognizingPolicyModel, ApprovedRecognizingRequestPolicy(), true},
		{"legacy_contract_does_not_expand", "school-reader", ApprovedRecognizingRequestPolicy(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := GradingModelSnapshot{Provider: "school-provider", Model: tc.model, Route: "school-provider/" + tc.model, RecognizingRequestPolicy: tc.policy}
			if err := ValidateGradingRecognizingRequestPolicy(route); (err == nil) != tc.valid {
				t.Fatalf("frozen route validation: %v", err)
			}
			if !tc.valid {
				return
			}
			if err := ValidateModelInvocationRequestPolicy(GradingStageRecognizing, route, tc.policy); err != nil {
				t.Fatalf("same frozen invocation rejected: %v", err)
			}
			err := ValidateModelInvocationRequestPolicy(GradingStageLocating, route, ApprovedLocatingRequestPolicy())
			if (err == nil) != !tc.policy.IsZero() {
				t.Fatalf("locating authorization must follow explicit recognition policy: %v", err)
			}
		})
	}
}
