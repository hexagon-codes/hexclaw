package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/api"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

func TestK12NewTaskVisionProbePersistsOnceAndFreezesSameModel(t *testing.T) {
	for _, initial := range []string{"missing", "stale"} {
		t.Run(initial, func(t *testing.T) {
			ctx := context.Background()
			store, err := sqlitestore.New(filepath.Join(t.TempDir(), "probe.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.Init(ctx); err != nil {
				t.Fatal(err)
			}
			provider := config.LLMProviderConfig{
				ProviderInstanceID: "pvd_v1_00112233445566778899aabbccddeeff",
				BaseURL:            "https://example.invalid/v1", APIKey: "fixture", Model: "vision-v1",
				Models: []string{"vision-v1"}, ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{{ID: "vision-v1", Capabilities: []string{"text", "vision"}}},
			}
			fingerprint := api.ModelCapabilityProbeConfigFingerprint("hexclaw-gpt", provider, "vision-v1")
			if initial == "stale" {
				_, err = store.SaveModelCapabilityProbeReceipt(ctx, &storage.ModelCapabilityProbeReceipt{
					ProviderInstanceID: provider.ProviderInstanceID, ModelID: "vision-v1", ProbeKind: "vision",
					ConfigFingerprint: fingerprint, ProbePolicyVersion: "v0", Outcome: "passed", ProbeStartedAt: 1, TestedAt: 2,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			probe := func(probeCtx context.Context, providerID, model, kind string) error {
				calls++
				if providerID != provider.ProviderInstanceID || model != "vision-v1" || kind != "vision" {
					t.Fatalf("probe changed selection: %s/%s/%s", providerID, model, kind)
				}
				_, err := store.SaveModelCapabilityProbeReceipt(probeCtx, &storage.ModelCapabilityProbeReceipt{
					ProviderInstanceID: providerID, ModelID: model, ProbeKind: kind,
					ConfigFingerprint: fingerprint, ProbePolicyVersion: api.ModelCapabilityProbePolicyVersion,
					Outcome: "passed", ProbeStartedAt: 100, TestedAt: 101,
				})
				return err
			}
			router := k12CapabilityReceiptTestRouter(provider)
			first, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(ctx, router, store, k12.GradingModelSnapshot{}, probe)
			if err != nil {
				t.Fatal(err)
			}
			second, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(ctx, router, store, k12.GradingModelSnapshot{}, probe)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !first.HasFrozenCapabilityProbeEvidence() || first.CapabilityReceiptDigest != second.CapabilityReceiptDigest {
				t.Fatalf("probe was duplicated or freeze drifted: calls=%d first=%+v second=%+v", calls, first, second)
			}
			if err := validateK12FrozenModelCapabilityReceipt(ctx, router, store, first, "vision"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestK12NewTaskVisionProbeDoesNotResendCurrentFailedProbe(t *testing.T) {
	provider := config.LLMProviderConfig{
		ProviderInstanceID: "pvd_v1_00112233445566778899aabbccddeeff", Model: "vision-v1",
		Models: []string{"vision-v1"}, ModelSpecsMode: config.LLMModelSpecsModeExplicit,
		ModelSpecs: []config.LLMProviderModelSpec{{ID: "vision-v1", Capabilities: []string{"text", "vision"}}},
	}
	receipts := &k12CapabilityReceiptStoreStub{receipt: &storage.ModelCapabilityProbeReceipt{
		ProviderInstanceID: provider.ProviderInstanceID, ModelID: "vision-v1", ProbeKind: "vision",
		ConfigFingerprint:  api.ModelCapabilityProbeConfigFingerprint("hexclaw-gpt", provider, "vision-v1"),
		ProbePolicyVersion: api.ModelCapabilityProbePolicyVersion, Outcome: "failed",
		FailureCode: "UPSTREAM_TIMEOUT", ProbeStartedAt: 100, TestedAt: 101,
	}}
	calls := 0
	_, err := resolveK12GradingModelSnapshotWithCapabilityReceipt(context.Background(), k12CapabilityReceiptTestRouter(provider), receipts, k12.GradingModelSnapshot{},
		func(context.Context, string, string, string) error { calls++; return nil })
	if !errors.Is(err, k12.ErrModelCapabilityUnverified) || calls != 0 {
		t.Fatalf("failed probe was resent: calls=%d err=%v", calls, err)
	}
}
