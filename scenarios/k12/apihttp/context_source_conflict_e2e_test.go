package apihttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type sourceConflictHTTPRecognizer struct{}

func (sourceConflictHTTPRecognizer) Recognize(context.Context, []byte) ([]usecase.RecognizedQuestion, error) {
	confidence := .99
	return []usecase.RecognizedQuestion{{Subject: "数学", Question: "计算234的值。", RawTranscription: "计算234的值。", CanonicalMarkdown: "计算234的值。",
		AnswerState: usecase.AnswerStatePresent, StudentAnswer: "24", AnswerRawTranscription: "24", AnswerCanonicalMarkdown: "24", RecognitionConfidence: &confidence,
		EvidenceTranscriptions: []string{"计算2*3*4的值。", "计算234的值。"}, AnswerEvidenceTranscriptions: []string{"24", "24"},
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, ScopeDigest: "isolated-source", TargetIDs: []string{"one"}},
	}}, nil
}

// 不同原文证据经过真实图片任务的冻结与投影后仍须保留冲突和待核验题目状态。
func TestImageTaskMathSourceConflictHTTPAPI(t *testing.T) {
	fixture := newImageTaskHTTPFixture(t)
	fixture.classifier.result = usecase.ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"visible handwritten answer"}, Confidence: .99}
	deps := usecase.Deps{Records: fixture.coordinator.Records, Recognizer: sourceConflictHTTPRecognizer{}, Now: func() int64 { return time.Now().Unix() }}
	fixture.coordinator.GradingBudgetSnapshot = k12.GradingBudgetSnapshot{
		PolicyVersion: 1, RecognitionPlanVersion: k12.RecognitionPlanVersionV1, ItemConcurrency: 1,
		StageSeconds:     k12.GradingStageBudgets{Queued: 60, Normalizing: 60, Recognizing: 120, Locating: 60, Rendering: 60, Projecting: 60},
		AssessingBuckets: []k12.GradingAssessingBudgetBucket{{MaxProblems: 1, Seconds: 90}, {MaxProblems: 8, Seconds: 180}, {MaxProblems: 16, Seconds: 300}, {MaxProblems: 32, Seconds: 540}},
	}
	deps.GradingBudgetSnapshot = fixture.coordinator.GradingBudgetSnapshot
	orchestrator := usecase.NewGradingOrchestrator(deps, func(snapshot k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return snapshot, nil })
	t.Cleanup(func() {
		if err := orchestrator.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	fixture.coordinator.Grading = orchestrator
	server := httptest.NewServer(fixture.handler)
	t.Cleanup(server.Close)
	response, err := server.Client().Post(server.URL+"/image-tasks", "application/json", strings.NewReader(createImageTaskBody(fixture.assetID, "source-conflict")))
	if err != nil {
		t.Fatal(err)
	}
	var created imageTaskCreateContract
	err = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || created.Dispatch.DispatchID == "" {
		t.Fatalf("create: status=%d result=%+v err=%v", response.StatusCode, created, err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err = server.Client().Get(server.URL + "/image-tasks/" + created.Dispatch.DispatchID + "?agent=mingming")
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("result: status=%d body=%s err=%v", response.StatusCode, body, readErr)
		}
		if strings.Contains(string(body), `"evidence_conflict"`) {
			if !strings.Contains(string(body), "计算234的值") || !strings.Contains(string(body), `"confirmation_required":true`) || !strings.Contains(string(body), `"status":"awaiting_source"`) {
				t.Fatalf("source conflict or confirmation state lost: %s", body)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal(fmt.Sprintf("source conflict did not remain reviewable: %s", body))
		case <-ticker.C:
		}
	}
}
