package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/knowledge"
)

type knowledgeOCRCaptureProvider struct {
	request         llm.CompletionRequest
	err             error
	headerBudget    time.Duration
	hasHeaderBudget bool
	deadline        time.Time
	hasDeadline     bool
}

func (*knowledgeOCRCaptureProvider) Name() string { return "capture" }

func (p *knowledgeOCRCaptureProvider) Complete(
	ctx context.Context,
	request llm.CompletionRequest,
) (*llm.CompletionResponse, error) {
	p.request = request
	p.headerBudget, p.hasHeaderBudget = egress.ProviderRequestResponseHeaderTimeoutFromContext(ctx)
	p.deadline, p.hasDeadline = ctx.Deadline()
	if p.err != nil {
		return nil, p.err
	}
	return &llm.CompletionResponse{Content: "第 1 题：\\(a \\div b = a/b\\)"}, nil
}

func (*knowledgeOCRCaptureProvider) Stream(
	context.Context,
	llm.CompletionRequest,
) (*llm.Stream, error) {
	return nil, nil
}

func (*knowledgeOCRCaptureProvider) Models() []llm.ModelInfo { return nil }

func (*knowledgeOCRCaptureProvider) CountTokens([]llm.Message) (int, error) { return 0, nil }

func TestKnowledgeOCRAdapterUsesFaithfulTextbookTranscriptionAndRealRouteReceipt(t *testing.T) {
	provider := &knowledgeOCRCaptureProvider{}
	result, err := completeKnowledgePDFPageOCR(
		context.Background(), provider, "hexclaw-gpt", "gpt-5.6-sol",
		[]byte("rendered-page"), "image/png",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content == "" || result.RouteReceipt != (knowledge.OCRRouteReceipt{
		Provider: "hexclaw-gpt", Model: "gpt-5.6-sol",
		Operation: knowledge.OCRRouteOperationPDFPage,
		Status:    knowledge.OCRRouteStatusSucceeded, Fake: false,
	}) {
		t.Fatalf("OCR result=%+v", result)
	}
	if provider.request.Model != "gpt-5.6-sol" || len(provider.request.Messages) != 1 ||
		len(provider.request.Messages[0].MultiContent) != 2 {
		t.Fatalf("OCR request=%+v", provider.request)
	}
	prompt := provider.request.Messages[0].MultiContent[0].Text
	for _, required := range []string{
		"Faithfully transcribe", "all visible text", "mathematical formulas",
		"question numbers", "tables", "original hierarchy", "Do not summarize",
		"Do not infer", "Respond in Chinese", "output only the transcription",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("OCR prompt missing %q: %q", required, prompt)
		}
	}
	for _, forbidden := range []string{"briefly describe", "main content", "for knowledge base retrieval"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("OCR prompt still asks for caption %q: %q", forbidden, prompt)
		}
	}
	for _, current := range prompt {
		if unicode.Is(unicode.Han, current) {
			t.Fatalf("OCR provider prompt must be written in English: %q", prompt)
		}
	}
}

func TestKnowledgeOCRAdapterDoesNotCreateSuccessReceiptOnProviderFailure(t *testing.T) {
	provider := &knowledgeOCRCaptureProvider{err: errors.New("provider unavailable")}
	result, err := completeKnowledgePDFPageOCR(
		context.Background(), provider, "hexclaw-gpt", "gpt-5.6-sol",
		[]byte("rendered-page"), "image/png",
	)
	if err == nil || result.Content != "" || result.RouteReceipt.Provider != "" ||
		result.RouteReceipt.Status != "" {
		t.Fatalf("failed OCR result=%+v err=%v", result, err)
	}
}

func TestKnowledgeOCRAdapterKeepsPageDeadlineAndRequestLocalHeaderBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pageBudget    time.Duration
		existingGuard time.Duration
	}{
		{name: "page_deadline", pageBudget: 180 * time.Second},
		{name: "no_deadline"},
		{name: "stricter_existing_guard", pageBudget: 180 * time.Second, existingGuard: 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var deadline time.Time
			if tc.pageBudget > 0 {
				deadline = time.Now().Add(tc.pageBudget)
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
			}
			if tc.existingGuard > 0 {
				ctx = egress.WithProviderRequestResponseHeaderTimeout(ctx, tc.existingGuard)
			}
			provider := &knowledgeOCRCaptureProvider{}
			if _, err := completeKnowledgePDFPageOCR(ctx, provider, "hexclaw-gpt", "gpt-5.6-sol", []byte("rendered-page"), "image/png"); err != nil {
				t.Fatal(err)
			}
			if tc.pageBudget == 0 {
				if provider.hasHeaderBudget || provider.hasDeadline {
					t.Fatal("OCR without a page deadline must keep the default transport guard")
				}
				return
			}
			if !provider.hasDeadline || !provider.deadline.Equal(deadline) {
				t.Fatalf("page deadline changed: got=%v want=%v", provider.deadline, deadline)
			}
			if !provider.hasHeaderBudget || provider.headerBudget <= 0 {
				t.Fatal("page deadline did not reach the provider response-header guard")
			}
			if tc.existingGuard > 0 {
				if provider.headerBudget != tc.existingGuard {
					t.Fatalf("stricter response-header guard changed: got=%v want=%v", provider.headerBudget, tc.existingGuard)
				}
			} else if provider.headerBudget <= 120*time.Second || provider.headerBudget > tc.pageBudget {
				t.Fatalf("180-second page was cut short or extended: header guard=%v", provider.headerBudget)
			}
			if ctx.Err() != nil {
				t.Fatalf("page context unexpectedly cancelled: %v", ctx.Err())
			}
		})
	}
}
