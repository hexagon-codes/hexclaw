package k12

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecognitionExecutionPolicyHeaderIsFrozenAndLegacyOmitted(t *testing.T) {
	header := RecognitionLayoutPlanHeaderV2{
		InitialReadMode: RecognitionLayoutManifestWithContentV1,
		PlanID:          "layout-policy", ParentInvocationID: "model-policy", AgentName: "policy-owner", JobID: "policy-job",
		PageDigest: "sha256:" + strings.Repeat("1", 64), ParentRequestDigest: "parent-policy",
		RouteSnapshot:            GradingModelSnapshot{Provider: "cloud-gpt", Model: "gpt-6.1-sol", Route: "cloud-gpt/gpt-6.1-sol", Capability: "vision"},
		StageStartedAtUnixMillis: 1000, PhysicalCallCapMillis: 120000,
		BudgetBuckets:        RecognitionLayoutBudgetBucketsV2{UpTo1ProblemMillis: 120000, UpTo8ProblemsMillis: 600000, UpTo16ProblemsMillis: 600000, UpTo32ProblemsMillis: 600000},
		AdapterWorkerHardCap: 2, EffectiveConcurrency: 2,
	}
	legacy, legacyDigest, err := CanonicalRecognitionLayoutPlanHeaderV2(header)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte(`"execution_policy"`)) {
		t.Fatal("empty execution policy changed the legacy JSON shape")
	}
	header.ExecutionPolicy = RecognitionExecutionPolicyCompactDAGV1
	compact, compactDigest, err := CanonicalRecognitionLayoutPlanHeaderV2(header)
	if err != nil {
		t.Fatal(err)
	}
	if compactDigest == legacyDigest || !bytes.Contains(compact, []byte(`"execution_policy":"compact_dag_v1"`)) {
		t.Fatal("execution policy is not bound to the canonical header and digest")
	}
	var restored RecognitionLayoutPlanHeaderV2
	if err := json.Unmarshal(legacy, &restored); err != nil {
		t.Fatal(err)
	}
	replayed, replayedDigest, err := CanonicalRecognitionLayoutPlanHeaderV2(restored)
	if err != nil || !bytes.Equal(legacy, replayed) || replayedDigest != legacyDigest {
		t.Fatalf("legacy header changed after readback: %v", err)
	}
	for _, tc := range []struct{ name, policy, mode string }{
		{"unknown_policy", "compact_dag_v2", RecognitionLayoutManifestWithContentV1},
		{"policy_without_initial_read", RecognitionExecutionPolicyCompactDAGV1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := header
			candidate.ExecutionPolicy, candidate.InitialReadMode = tc.policy, tc.mode
			if _, _, err := CanonicalRecognitionLayoutPlanHeaderV2(candidate); err == nil {
				t.Fatal("invalid execution policy accepted")
			}
		})
	}
}

func TestRecognitionExecutionPolicyContextKeepsExplicitLegacyScope(t *testing.T) {
	ctx := WithRecognitionExecutionPolicy(context.Background(), RecognitionExecutionPolicyCompactDAGV1)
	if RecognitionExecutionPolicyFromContext(ctx) != RecognitionExecutionPolicyCompactDAGV1 {
		t.Fatal("frozen execution policy missing")
	}
	if RecognitionExecutionPolicyFromContext(WithRecognitionExecutionPolicy(ctx, "")) != "" || RecognitionExecutionPolicyFromContext(nil) != "" {
		t.Fatal("legacy scope inherited a newer execution policy")
	}
}
