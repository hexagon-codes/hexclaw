package main

import (
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestK12RecognizingDefaultKeepsNewAndFrozenBudgetsSeparate(t *testing.T) {
	next := k12GradingBudgetSnapshotFromConfig(config.DefaultK12GradingBudget())
	if err := next.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{2, 5, 8} {
		_, millis, err := next.RecognizingBuckets.Select(count)
		if err != nil || millis != 300_000 || next.StageSeconds.Recognizing != 300 {
			t.Fatalf("new %d-item recognizing budget: %d, %v", count, millis, err)
		}
	}
	_, single, _ := next.RecognizingBuckets.Select(1)
	if single != 180_000 || next.PhysicalCallCapMillis != 120_000 || next.EffectiveConcurrency != 2 ||
		next.RecognizingBuckets.UpTo16ProblemsMillis != 300_000 || next.RecognizingBuckets.UpTo32ProblemsMillis != 300_000 {
		t.Fatal("unrelated recognizing limits changed")
	}
	prior := next
	prior.RecognizingBuckets.UpTo8ProblemsMillis = 180_000
	raw, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	var restored k12.GradingBudgetSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil || restored.Validate() != nil || restored.RecognizingBuckets.UpTo8ProblemsMillis != 180_000 {
		t.Fatal("frozen prior recognizing budget was changed")
	}
}

func TestK12GradingBudgetSnapshotFromConfigPreservesReleaseEvidence(t *testing.T) {
	cfg := config.K12GradingBudgetConfig{
		PolicyVersion: 3, QueuedSeconds: 11, NormalizingSeconds: 12,
		RecognitionPlanVersion: 2,
		RecognizingSeconds:     13, LocatingSeconds: 14, RenderingSeconds: 15,
		ProjectingSeconds: 16, ItemConcurrency: 2,
		AssessingBuckets: []config.K12AssessingBudgetBucketConfig{
			{MaxProblems: 1, Seconds: 21}, {MaxProblems: 8, Seconds: 22},
			{MaxProblems: 16, Seconds: 23}, {MaxProblems: 32, Seconds: 24},
		},
		RecognizingBuckets: config.K12RecognizingBudgetBucketsConfig{
			UpTo1ProblemMillis: 3_100, UpTo8ProblemsMillis: 6_200,
			UpTo16ProblemsMillis: 9_300, UpTo32ProblemsMillis: 13_000,
		},
		PhysicalCallCapMillis: 120000, WorkerHardCap: 2, EffectiveConcurrency: 1,
	}
	got := k12GradingBudgetSnapshotFromConfig(cfg)
	if err := got.Validate(); err != nil {
		t.Fatalf("converted snapshot invalid: %v", err)
	}
	if got.PolicyVersion != 3 || got.StageSeconds.Recognizing != 13 ||
		got.AssessingBuckets[2].Seconds != 23 || got.ItemConcurrency != 2 ||
		got.RecognitionPlanVersion != 2 || got.RecognizingBuckets.UpTo32ProblemsMillis != 13_000 ||
		got.PhysicalCallCapMillis != 120000 || got.WorkerHardCap != 2 || got.EffectiveConcurrency != 1 {
		t.Fatalf("release evidence changed during composition: %+v", got)
	}
}

func TestK12GradingBudgetSnapshotFromConfigKeepsZeroPolicyUnfrozen(t *testing.T) {
	got := k12GradingBudgetSnapshotFromConfig(config.K12GradingBudgetConfig{})
	if got.IsFrozen() || got.Validate() != nil {
		t.Fatalf("zero config must remain strict legacy gate: %+v", got)
	}
}
