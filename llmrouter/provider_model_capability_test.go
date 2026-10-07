package llmrouter

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
)

type capabilityGuardCountingProvider struct {
	completeCalls atomic.Int32
	streamCalls   atomic.Int32
}

func (*capabilityGuardCountingProvider) Name() string { return "mixed" }

func (p *capabilityGuardCountingProvider) Complete(
	context.Context,
	llm.CompletionRequest,
) (*llm.CompletionResponse, error) {
	p.completeCalls.Add(1)
	return &llm.CompletionResponse{}, nil
}

func (p *capabilityGuardCountingProvider) Stream(
	context.Context,
	llm.CompletionRequest,
) (*llm.Stream, error) {
	p.streamCalls.Add(1)
	return nil, fmt.Errorf("test stream")
}

func (*capabilityGuardCountingProvider) Models() []llm.ModelInfo { return nil }

func (*capabilityGuardCountingProvider) CountTokens([]llm.Message) (int, error) { return 0, nil }

var _ hexagon.Provider = (*capabilityGuardCountingProvider)(nil)

func TestSelectorKnowledgeOCRRouteKeepsExactInstanceAndModelScope(t *testing.T) {
	const instanceID = "pvd_v1_11111111111111111111111111111111"
	cfg := config.LLMConfig{Default: "mixed", Providers: map[string]config.LLMProviderConfig{
		"mixed": {
			ProviderInstanceID: instanceID,
			Model:              "book-model", Models: []string{"book-model", "other-model", "vision-model", "vector-model"},
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs: []config.LLMProviderModelSpec{
				{ID: "book-model", Capabilities: []string{config.LLMModelCapabilityText}},
				{ID: "other-model", Capabilities: []string{config.LLMModelCapabilityText}},
				{ID: "vision-model", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision}},
				{ID: "vector-model", Capabilities: []string{config.LLMModelCapabilityEmbedding}},
			},
		},
	}}
	inner := &capabilityGuardCountingProvider{}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{"mixed": inner})
	imageRequest := func(model string) llm.CompletionRequest {
		return llm.CompletionRequest{Model: model, Messages: []llm.Message{{
			Role: llm.RoleUser, MultiContent: []llm.ContentPart{llm.NewImageURLPart("data:image/png;base64,aW1hZ2U=", "auto")},
		}}}
	}
	for _, route := range []struct{ instance, provider, model string }{
		{instanceID, "mixed", "vector-model"},
		{instanceID, "mixed", "unknown-model"},
		{"pvd_v1_22222222222222222222222222222222", "mixed", "book-model"},
		{instanceID, "other-provider", "book-model"},
		{instanceID, "mixed", ""},
	} {
		if _, err := selector.ResolveKnowledgeOCRRoute(route.instance, route.provider, route.model); !errors.Is(err, ErrModelCapabilityMismatch) {
			t.Fatalf("invalid OCR route %+v returned %v", route, err)
		}
	}
	if _, err := selector.ResolveRouteForCapabilities("mixed", "book-model", "text", "vision"); !errors.Is(err, ErrModelCapabilityMismatch) {
		t.Fatalf("ordinary vision route changed: %v", err)
	}
	ordinary, ok := selector.Get("mixed")
	if !ok {
		t.Fatal("ordinary provider missing")
	}
	request := imageRequest("book-model")
	request.Metadata = map[string]any{"capabilities": []string{"vision"}, "purpose": "vision_ocr"}
	ctx := egress.WithRequest(context.Background(), egress.PurposeVisionOCR, "", egress.ClassSensitiveMedia)
	if _, err := ordinary.Complete(ctx, request); !errors.Is(err, ErrModelCapabilityMismatch) {
		t.Fatalf("shared OCR purpose or request metadata granted vision: %v", err)
	}
	if inner.completeCalls.Load() != 0 {
		t.Fatal("invalid or ordinary image request reached transport")
	}
	ocr, err := selector.ResolveKnowledgeOCRRoute(instanceID, "mixed", "book-model")
	if err != nil || ocr.ProviderName != "mixed" || ocr.Model != "book-model" {
		t.Fatalf("exact OCR route=%+v err=%v", ocr, err)
	}
	ocrContext, attempt := egress.WithProviderAttempt(context.Background())
	if _, err := ocr.Provider.Complete(ocrContext, imageRequest("book-model")); err != nil {
		t.Fatalf("unknown vision rejected before actual OCR completion: %v", err)
	}
	if errors.Is(attempt.Reconcile(egress.ErrDenied), egress.ErrProviderNotSent) {
		t.Fatal("OCR route bypassed the provider attempt wrapper")
	}
	for _, model := range []string{"other-model", "vision-model", "vector-model", "unknown-model"} {
		if _, err := ocr.Provider.Complete(context.Background(), imageRequest(model)); !errors.Is(err, ErrModelCapabilityMismatch) {
			t.Fatalf("OCR scope leaked to model %q: %v", model, err)
		}
	}
	if inner.completeCalls.Load() != 1 {
		t.Fatalf("OCR transport calls=%d, want only the exact real request", inner.completeCalls.Load())
	}
	current, _ := selector.ProviderConfig("mixed")
	if current.Model != "book-model" || config.ModelHasCapability(current, "book-model", config.LLMModelCapabilityVision) {
		t.Fatal("OCR execution changed the default model or persistent vision declaration")
	}
}

func TestSelectorProviderRejectsNonTextModelsBeforeCompletionTransport(t *testing.T) {
	const (
		chatModel   = "chat-model"
		vectorModel = "vector-model"
	)
	cfg := config.LLMConfig{
		Default: "mixed",
		Providers: map[string]config.LLMProviderConfig{
			"mixed": {
				Model: chatModel, Models: []string{chatModel, vectorModel},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{
					{ID: chatModel, Capabilities: []string{config.LLMModelCapabilityText}},
					{ID: vectorModel, Capabilities: []string{config.LLMModelCapabilityEmbedding}},
				},
			},
		},
	}
	inner := &capabilityGuardCountingProvider{}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{"mixed": inner})
	provider, ok := selector.Get("mixed")
	if !ok {
		t.Fatal("configured provider missing")
	}

	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{Model: vectorModel}); err == nil {
		t.Fatal("embedding-only model reached completion guard without error")
	}
	if _, err := provider.Stream(context.Background(), llm.CompletionRequest{Model: vectorModel}); err == nil {
		t.Fatal("embedding-only model reached stream guard without error")
	}
	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{Model: "unknown-model"}); err == nil {
		t.Fatal("unclassified model reached completion guard without error")
	}
	if got := inner.completeCalls.Load(); got != 0 {
		t.Fatalf("non-text completions reached transport %d times", got)
	}
	if got := inner.streamCalls.Load(); got != 0 {
		t.Fatalf("non-text streams reached transport %d times", got)
	}

	if _, err := provider.Complete(context.Background(), llm.CompletionRequest{Model: chatModel}); err != nil {
		t.Fatalf("text model rejected: %v", err)
	}
	if got := inner.completeCalls.Load(); got != 1 {
		t.Fatalf("text completion calls=%d, want 1", got)
	}
}

func TestSelectorDefaultRouteForCapabilitiesUsesSameProviderStableModel(t *testing.T) {
	cfg := config.LLMConfig{
		Default: "mixed",
		Providers: map[string]config.LLMProviderConfig{
			"mixed": {
				Model: "text-default", Models: []string{"text-default", "vision-first", "vision-second"},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{
					{ID: "text-default", Capabilities: []string{config.LLMModelCapabilityText}},
					{ID: "vision-first", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision}},
					{ID: "vision-second", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision}},
				},
			},
		},
	}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{
		"mixed": &capabilityGuardCountingProvider{},
	})

	route, err := selector.DefaultRouteForCapabilities(
		config.LLMModelCapabilityText,
		config.LLMModelCapabilityVision,
	)
	if err != nil {
		t.Fatalf("DefaultRouteForCapabilities: %v", err)
	}
	if route.ProviderName != "mixed" || route.Model != "vision-first" {
		t.Fatalf("route=%+v, want mixed/vision-first", route)
	}
}

func TestSelectorDefaultRouteForCapabilitiesFailsClosedWithoutCrossProvider(t *testing.T) {
	cfg := config.LLMConfig{
		Default: "text-only",
		Providers: map[string]config.LLMProviderConfig{
			"text-only": {
				Model: "text-default", Models: []string{"text-default"},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{{
					ID: "text-default", Capabilities: []string{config.LLMModelCapabilityText},
				}},
			},
			"other-vision": {
				Model: "vision-model", Models: []string{"vision-model"},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{{
					ID: "vision-model", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision},
				}},
			},
		},
	}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{
		"text-only":    &capabilityGuardCountingProvider{},
		"other-vision": &capabilityGuardCountingProvider{},
	})

	if _, err := selector.DefaultRouteForCapabilities(
		config.LLMModelCapabilityText,
		config.LLMModelCapabilityVision,
	); err == nil {
		t.Fatal("missing same-provider vision model must fail closed instead of crossing provider")
	}
}

func TestSelectorResolveRouteForCapabilitiesHonorsExactExplicitSelection(t *testing.T) {
	cfg := config.LLMConfig{
		Default: "hexclaw-gpt",
		Providers: map[string]config.LLMProviderConfig{
			"hexclaw-gpt": {
				Model:          "gpt-5.3-codex-spark",
				Models:         []string{"gpt-5.3-codex-spark", "gpt-5.6-sol"},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{
					{ID: "gpt-5.3-codex-spark", Capabilities: []string{config.LLMModelCapabilityText}},
					{ID: "gpt-5.6-sol", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision}},
				},
			},
		},
	}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{
		"hexclaw-gpt": &capabilityGuardCountingProvider{},
	})

	route, err := selector.ResolveRouteForCapabilities(
		"hexclaw-gpt",
		"gpt-5.6-sol",
		config.LLMModelCapabilityText,
		config.LLMModelCapabilityVision,
	)
	if err != nil {
		t.Fatalf("ResolveRouteForCapabilities: %v", err)
	}
	if route.ProviderName != "hexclaw-gpt" || route.Model != "gpt-5.6-sol" {
		t.Fatalf("explicit route drifted to %+v", route)
	}

	if _, err := selector.ResolveRouteForCapabilities(
		"hexclaw-gpt",
		"gpt-5.3-codex-spark",
		config.LLMModelCapabilityText,
		config.LLMModelCapabilityVision,
	); err == nil {
		t.Fatal("explicit text-only selection must fail instead of silently switching models")
	}
}

func TestSelectorResolveRouteForCapabilitiesTreatsAutoAsAutomaticOnlyWhenRouteIsOtherwiseEmpty(t *testing.T) {
	cfg := config.LLMConfig{
		Default: "hexclaw-gpt",
		Providers: map[string]config.LLMProviderConfig{
			"hexclaw-gpt": {
				Model: "text-default", Models: []string{"text-default", "vision"},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{
					{ID: "text-default", Capabilities: []string{config.LLMModelCapabilityText}},
					{ID: "vision", Capabilities: []string{config.LLMModelCapabilityText, config.LLMModelCapabilityVision}},
				},
			},
		},
	}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{
		"hexclaw-gpt": &capabilityGuardCountingProvider{},
	})

	for _, requested := range [][2]string{
		{"auto", "auto"},
		{"AUTO", ""},
		{"", "AuTo"},
	} {
		route, err := selector.ResolveRouteForCapabilities(
			requested[0],
			requested[1],
			config.LLMModelCapabilityText,
			config.LLMModelCapabilityVision,
		)
		if err != nil {
			t.Fatalf("automatic route %q/%q: %v", requested[0], requested[1], err)
		}
		if route.ProviderName != "hexclaw-gpt" || route.Model != "vision" {
			t.Fatalf("automatic route %q/%q resolved to %+v", requested[0], requested[1], route)
		}
	}

	if _, err := selector.ResolveRouteForCapabilities(
		"hexclaw-gpt",
		"auto",
		config.LLMModelCapabilityText,
		config.LLMModelCapabilityVision,
	); err == nil {
		t.Fatal("explicit provider mixed with auto model must fail as an incomplete explicit route")
	}
}

func TestSelectorProviderRejectsImageForTextOnlyModelBeforeTransport(t *testing.T) {
	const textModel = "text-model"
	cfg := config.LLMConfig{
		Default: "mixed",
		Providers: map[string]config.LLMProviderConfig{
			"mixed": {
				Model: textModel, Models: []string{textModel},
				ModelSpecsMode: config.LLMModelSpecsModeExplicit,
				ModelSpecs: []config.LLMProviderModelSpec{{
					ID: textModel, Capabilities: []string{config.LLMModelCapabilityText},
				}},
			},
		},
	}
	inner := &capabilityGuardCountingProvider{}
	selector := NewWithProviders(cfg, map[string]hexagon.Provider{"mixed": inner})
	provider, ok := selector.Get("mixed")
	if !ok {
		t.Fatal("configured provider missing")
	}
	request := llm.CompletionRequest{
		Model: textModel,
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			MultiContent: []llm.ContentPart{
				llm.NewTextPart("read it"),
				llm.NewImageURLPart("data:image/png;base64,AA==", "high"),
			},
		}},
	}

	if _, err := provider.Complete(context.Background(), request); err == nil {
		t.Fatal("image request reached text-only completion guard without error")
	}
	if _, err := provider.Stream(context.Background(), request); err == nil {
		t.Fatal("image request reached text-only stream guard without error")
	}
	if got := inner.completeCalls.Load(); got != 0 {
		t.Fatalf("image completion reached transport %d times", got)
	}
	if got := inner.streamCalls.Load(); got != 0 {
		t.Fatalf("image stream reached transport %d times", got)
	}
}
