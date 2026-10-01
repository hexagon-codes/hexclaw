package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12engineadapter "github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type k12VisionRequestCaptureProvider struct {
	name              string
	content           string
	request           llm.CompletionRequest
	operationSafety   llm.OperationSafety
	headerBudget      time.Duration
	hasHeaderBudget   bool
	hasCallerDeadline bool
	egressRequests    []egress.Request
	hasEgressRequest  bool
	calls             int
}

func (p *k12VisionRequestCaptureProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return "capture"
}

func (p *k12VisionRequestCaptureProvider) Complete(
	ctx context.Context,
	req llm.CompletionRequest,
) (*llm.CompletionResponse, error) {
	p.calls++
	p.request = req
	p.operationSafety = llm.OperationSafetyFromContext(ctx)
	p.headerBudget, p.hasHeaderBudget = egress.ProviderRequestResponseHeaderTimeoutFromContext(ctx)
	_, p.hasCallerDeadline = ctx.Deadline()
	p.egressRequests, p.hasEgressRequest = egress.RequestsFromContext(ctx)
	content := p.content
	if content == "" {
		content = "captured"
	}
	return &llm.CompletionResponse{Content: content}, nil
}

func (*k12VisionRequestCaptureProvider) Stream(
	context.Context,
	llm.CompletionRequest,
) (*llm.Stream, error) {
	return nil, nil
}

func (*k12VisionRequestCaptureProvider) Models() []llm.ModelInfo { return nil }

func (*k12VisionRequestCaptureProvider) CountTokens([]llm.Message) (int, error) {
	return 0, nil
}

// REG-K12-RECOGNIZING-REAL-PROBE-PARITY-20260808-001：生产代码和两种真实模型探针
// 必须使用同一个 K12 视觉请求构建器。探针自行组装 CompletionRequest 时可能静默
// 丢失强类型识题策略，并验证了不同的 gpt-5.6-sol 线路契约。
func TestBUG20260808K12ProductionAndRealProbesUseOneVisionRequestBuilder(t *testing.T) {
	t.Parallel()

	for _, file := range []string{
		"main.go",
		"k12_im_photo_real_probe_test.go",
		"bug_20260808_k12_self_inventory_real_probe_test.go",
	} {
		file := file
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(raw), "completeK12VisionRequest("); got != 1 {
				t.Fatalf("canonical K12 vision request builder calls=%d, want exactly 1", got)
			}
		})
	}

	for file, forbidden := range map[string]string{
		"k12_im_photo_real_probe_test.go":                    "provider.Complete(visionCtx",
		"bug_20260808_k12_self_inventory_real_probe_test.go": "route.Provider.Complete(ctx",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("%s still assembles a direct provider completion", file)
		}
	}
}

func TestBUG20260808K12VisionRequestBuilderPreservesTheProductionWireContract(t *testing.T) {
	provider := &k12VisionRequestCaptureProvider{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot := k12.GradingModelSnapshot{
		Provider:                 "hexclaw-gpt",
		Model:                    "gpt-5.6-sol",
		Route:                    "hexclaw-gpt/gpt-5.6-sol",
		TimeoutMS:                120_000,
		RecognizingRequestPolicy: k12.ApprovedRecognizingRequestPolicy(),
	}
	ctx = k12.WithGradingModelSnapshot(ctx, snapshot)
	ctx = k12.WithGradingModelRequestPolicy(ctx, snapshot.RecognizingRequestPolicy)

	image := []byte("\x89PNG\r\n\x1a\n")
	content, err := completeK12VisionRequest(ctx, provider, snapshot.Model, image, "recognize")
	if err != nil {
		t.Fatal(err)
	}
	if content != "captured" || provider.calls != 1 {
		t.Fatalf("content=%q calls=%d", content, provider.calls)
	}
	if provider.operationSafety != llm.OperationSafetyNonIdempotent {
		t.Fatalf("operation safety=%q, want non-idempotent", provider.operationSafety)
	}
	if !provider.hasCallerDeadline || !provider.hasHeaderBudget || provider.headerBudget <= 0 {
		t.Fatalf("deadline=%t response-header budget=%s ok=%t",
			provider.hasCallerDeadline, provider.headerBudget, provider.hasHeaderBudget)
	}
	if !provider.hasEgressRequest || len(provider.egressRequests) != 1 ||
		provider.egressRequests[0].Purpose != egress.PurposeVisionOCR ||
		provider.egressRequests[0].DataClass != egress.ClassSensitiveMedia {
		t.Fatalf("egress requests=%+v ok=%t", provider.egressRequests, provider.hasEgressRequest)
	}
	if deadline, ok := ctx.Deadline(); !ok || provider.headerBudget > time.Until(deadline)+20*time.Millisecond {
		t.Fatalf("response-header budget=%s outlives caller deadline", provider.headerBudget)
	}
	if !reflect.DeepEqual(provider.request.Metadata, map[string]any{"thinking": "off"}) {
		t.Fatalf("metadata=%v, want exact recognizing policy", provider.request.Metadata)
	}
	if provider.request.ReasoningPolicyScope != llm.ReasoningPolicyScopeStructuredVisionRecognition {
		t.Fatalf("reasoning scope=%q", provider.request.ReasoningPolicyScope)
	}
	if provider.request.Model != snapshot.Model || len(provider.request.Messages) != 1 {
		t.Fatalf("model=%q messages=%d", provider.request.Model, len(provider.request.Messages))
	}
	message := provider.request.Messages[0]
	if message.Role != llm.RoleUser || len(message.MultiContent) != 2 ||
		message.MultiContent[0].Text != "recognize" || message.MultiContent[1].ImageURL == nil ||
		message.MultiContent[1].ImageURL.Detail != "high" {
		t.Fatalf("multimodal request=%+v", message)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(message.MultiContent[1].ImageURL.URL, prefix) {
		t.Fatalf("image URL has wrong MIME prefix")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(
		message.MultiContent[1].ImageURL.URL,
		prefix,
	))
	if err != nil || !reflect.DeepEqual(decoded, image) {
		t.Fatalf("image payload mismatch: err=%v", err)
	}
}

func TestK12VisionLogIdentitySeparatesFrozenRouteFromCompatibleAdapterName(t *testing.T) {
	ctx := k12.WithGradingModelSnapshot(context.Background(), k12.GradingModelSnapshot{
		Provider: "hexclaw-gpt",
		Model:    "gpt-5.6-sol",
	})
	routeProvider, adapterProvider := k12ProviderLogIdentity(
		ctx,
		&k12VisionRequestCaptureProvider{name: "openai"},
	)
	if routeProvider != "hexclaw-gpt" || adapterProvider != "openai" {
		t.Fatalf("log identity route=%q adapter=%q", routeProvider, adapterProvider)
	}
}

func TestBUG20260808K12VisionRequestBuilderDoesNotInventARecognizingPolicy(t *testing.T) {
	provider := &k12VisionRequestCaptureProvider{}
	if _, err := completeK12VisionRequest(
		context.Background(),
		provider,
		"other-vision-model",
		[]byte("\x89PNG\r\n\x1a\n"),
		"caption",
	); err != nil {
		t.Fatal(err)
	}
	if provider.request.Metadata != nil || provider.request.ReasoningPolicyScope != "" {
		t.Fatalf("unscoped request inherited policy: metadata=%v scope=%q",
			provider.request.Metadata, provider.request.ReasoningPolicyScope)
	}
	if provider.hasHeaderBudget {
		t.Fatalf("deadline-free request extended transport by %s", provider.headerBudget)
	}
}

// 首读格式选择只属于一次请求；复读和裁决继续使用同一任务的原始上下文。
func TestK12VisionRequestInitialReadJSONObjectIsRequestLocal(t *testing.T) {
	provider := &k12VisionRequestCaptureProvider{}
	snapshot := k12.GradingModelSnapshot{
		Provider:                 "hexclaw-gpt",
		Model:                    "gpt-5.6-sol",
		RecognizingRequestPolicy: k12.ApprovedRecognizingRequestPolicy(),
	}
	parent := k12.WithGradingModelSnapshot(t.Context(), snapshot)
	parent = k12.WithGradingModelRequestPolicy(parent, snapshot.RecognizingRequestPolicy)
	parent = k12.WithRecognitionLayoutPlanV2(parent, "sha256:"+strings.Repeat("1", 64))
	parent = k12.WithRecognitionLayoutInitialReadMode(parent, k12.RecognitionLayoutManifestWithContentV1)
	first := k12.WithRecognitionLayoutInitialReadJSONOutput(parent)
	first, cancel := context.WithTimeout(first, 5*time.Second)
	defer cancel()

	for _, tc := range []struct {
		name       string
		ctx        context.Context
		wantObject bool
	}{
		{"whole_page_initial_read", first, true},
		{"review_batch", parent, false},
		{"adjudication", parent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := completeK12VisionRequest(tc.ctx, provider, snapshot.Model, []byte("\x89PNG\r\n\x1a\n"), tc.name); err != nil {
				t.Fatal(err)
			}
			format := provider.request.ResponseFormat
			if tc.wantObject {
				if format == nil || format.Type != "json_object" || format.JSONSchema != nil {
					t.Fatalf("whole-page initial-read response format=%+v, want json_object", format)
				}
			} else if format != nil {
				t.Fatalf("%s inherited first-read response format=%+v", tc.name, format)
			}
		})
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls=%d, want one per request", provider.calls)
	}
}

// 通过实际非首读适配器与共享请求构建器检查输出合同，模型边界由 capture Provider 替代。
func TestK12VisionRequestNonBAdaptersKeepUnconstrainedOutput(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, response string
	}{
		{"creative_work_ocr", `{"text":"原稿文字"}`},
		{"answer_anchor", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &k12VisionRequestCaptureProvider{content: tc.response}
			vision := func(ctx context.Context, image []byte, prompt string) (string, error) {
				return completeK12VisionRequest(ctx, provider, "other-vision-model", image, prompt)
			}
			if tc.name == "creative_work_ocr" {
				text, err := k12engineadapter.NewCreativeWorkOCRAdapter(vision).RecognizeWriting(t.Context(), encoded.Bytes())
				if err != nil || text != "原稿文字" {
					t.Fatalf("writing OCR result=%q err=%v", text, err)
				}
			} else {
				questions, err := k12engineadapter.NewRecognizerAdapter(vision).AnchorAnswers(t.Context(), encoded.Bytes(), []usecase.RecognizedQuestion{{Question: "1+1=", Subject: "数学", AnswerState: usecase.AnswerStatePresent, StudentAnswer: "2"}})
				if err != nil || len(questions) != 1 {
					t.Fatalf("answer-anchor result count=%d err=%v", len(questions), err)
				}
			}
			if provider.calls != 1 || provider.request.ResponseFormat != nil {
				t.Fatalf("%s calls=%d response format=%+v, want one unconstrained request", tc.name, provider.calls, provider.request.ResponseFormat)
			}
		})
	}
}
