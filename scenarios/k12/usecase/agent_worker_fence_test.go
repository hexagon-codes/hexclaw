package usecase

import (
	"context"
	"testing"
	"time"
)

func TestAgentWorkerFenceRegistryOverlappingQuiesceRequiresLastRelease(t *testing.T) {
	var registry agentWorkerFenceRegistry
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	releaseFirst, err := registry.quiesceAgent(ctx, "mingming")
	if err != nil {
		t.Fatalf("first quiesce: %v", err)
	}
	releaseSecond, err := registry.quiesceAgent(ctx, "mingming")
	if err != nil {
		t.Fatalf("second quiesce: %v", err)
	}

	releaseFirst()
	_, finish, accepted := registry.start(
		context.Background(),
		"mingming",
		"after-first-release",
	)
	if accepted {
		finish()
		t.Fatal("first release removed a fence still held by the second caller")
	}

	releaseSecond()
	_, finish, accepted = registry.start(
		context.Background(),
		"mingming",
		"after-last-release",
	)
	if !accepted {
		t.Fatal("last release did not restore Agent worker admission")
	}
	finish()
}

func TestAgentWorkerFenceRegistryCancelsOnlyExactWorkerAndRejectsOldContext(t *testing.T) {
	var registry agentWorkerFenceRegistry
	first, finishFirst, accepted := registry.start(t.Context(), "mingming", "first-dispatch")
	if !accepted {
		t.Fatal("first worker was not admitted")
	}
	second, finishSecond, accepted := registry.start(t.Context(), "mingming", "second-dispatch")
	if !accepted {
		t.Fatal("second worker was not admitted")
	}
	defer finishSecond()
	if !registry.ownsContext(first, "first-dispatch") || registry.ownsContext(first, "second-dispatch") {
		t.Fatal("worker context ownership crossed dispatches")
	}
	registry.cancelWorker("mingming", "first-dispatch")
	if first.Err() != context.Canceled || second.Err() != nil {
		t.Fatalf("cancellation crossed dispatches: first=%v second=%v", first.Err(), second.Err())
	}
	finishFirst()
	replacement, finishReplacement, accepted := registry.start(t.Context(), "mingming", "first-dispatch")
	if !accepted {
		t.Fatal("finished worker did not release its key")
	}
	defer finishReplacement()
	if registry.ownsContext(first, "first-dispatch") || !registry.ownsContext(replacement, "first-dispatch") {
		t.Fatal("old context was accepted as the replacement worker")
	}
}
