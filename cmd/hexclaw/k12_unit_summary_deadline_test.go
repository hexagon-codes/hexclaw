package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type unitSummaryDeadlineProvider struct {
	hexagon.Provider
	complete func(context.Context, hexagon.CompletionRequest) (*hexagon.CompletionResponse, error)
}

func (p *unitSummaryDeadlineProvider) Complete(ctx context.Context, req hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
	return p.complete(ctx, req)
}

func unitSummaryDeadlineFixture(t *testing.T, complete func(context.Context, hexagon.CompletionRequest) (*hexagon.CompletionResponse, error)) (k12UnitSummaryGenerator, context.Context) {
	t.Helper()
	const name, model = "unit-fixture", "unit-model"
	provider := &unitSummaryDeadlineProvider{Provider: mockllm.NewLLMProvider(name), complete: complete}
	router := llmrouter.NewWithProviders(config.LLMConfig{
		Default: name,
		Providers: map[string]config.LLMProviderConfig{name: {
			ProviderInstanceID: "pvd_v1_00112233445566778899aabbccddeeff", Model: model, Models: []string{model},
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs:     []config.LLMProviderModelSpec{{ID: model, Capabilities: []string{config.LLMModelCapabilityText}}},
		}},
	}, map[string]hexagon.Provider{name: provider})
	snapshot, err := resolveK12PracticeModelSnapshot(router, k12.GradingModelSnapshot{Provider: name, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	return k12UnitSummaryGenerator{router: router}, k12.WithGradingModelSnapshot(context.Background(), snapshot)
}

func TestK12UnitSummaryGenerationDeadlineIsRequestLocal(t *testing.T) {
	calls := 0
	var generated context.Context
	g, parent := unitSummaryDeadlineFixture(t, func(ctx context.Context, _ hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
		calls++
		deadline, hasDeadline := ctx.Deadline()
		budget, hasBudget := egress.ProviderRequestResponseHeaderTimeoutFromContext(ctx)
		if calls == 1 {
			generated = ctx
			remaining := time.Until(deadline)
			if !hasDeadline || remaining < 599*time.Second || remaining > 600*time.Second {
				t.Fatalf("generation deadline = %s, present=%v", remaining, hasDeadline)
			}
			if !hasBudget || budget < 599*time.Second || budget > remaining+20*time.Millisecond {
				t.Fatalf("generation header budget = %s, present=%v", budget, hasBudget)
			}
		} else if hasDeadline || hasBudget {
			t.Fatal("generation budget leaked into the subsequent resolver request")
		}
		return &hexagon.CompletionResponse{Content: `{"schema_version":1,"subject":"math","title":"Local fixture","intent":"none","missing_fields":[]}`}, nil
	})
	if _, err := g.GenerateUnitSummary(parent, usecase.UnitSummaryGenerationInput{}); err != nil {
		t.Fatal(err)
	}
	if generated == nil || !errors.Is(generated.Err(), context.Canceled) {
		t.Fatal("completed generation did not release its deadline context")
	}
	if _, err := g.ResolveUnitSummary(parent, usecase.UnitSummaryResolveInput{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || parent.Err() != nil {
		t.Fatalf("calls=%d, parent error=%v", calls, parent.Err())
	}
}

func TestK12UnitSummaryGenerationHonorsEarlierDeadlineAndHeaderBudget(t *testing.T) {
	calls := 0
	g, parent := unitSummaryDeadlineFixture(t, func(ctx context.Context, _ hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
		calls++
		deadline, ok := ctx.Deadline()
		budget, bound := egress.ProviderRequestResponseHeaderTimeoutFromContext(ctx)
		if !ok || time.Until(deadline) > 50*time.Millisecond || !bound || budget != 10*time.Millisecond {
			t.Fatalf("earlier limits changed: deadline=%v, budget=%s", deadline, budget)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	parent, cancel := context.WithTimeout(parent, 50*time.Millisecond)
	defer cancel()
	parent = egress.WithProviderRequestResponseHeaderTimeout(parent, 10*time.Millisecond)
	_, err := g.GenerateUnitSummary(parent, usecase.UnitSummaryGenerationInput{})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("deadline result=%v, physical attempts=%d", err, calls)
	}
}
