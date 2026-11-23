package apihttp_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestImageTaskHTTPProjectsFrozenHomeworkAction(t *testing.T) {
	for _, tc := range []struct {
		name       string
		message    string
		classified k12.ImageTaskIntent
		want       k12.ImageTaskIntent
	}{
		{"solve written homework", "请解题", k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentBlankWorksheet},
		{"grade a worksheet", "请批改", k12.ImageTaskIntentBlankWorksheet, k12.ImageTaskIntentCompletedHomework},
		{"retain artwork classification", "请解题", k12.ImageTaskIntentArtwork, k12.ImageTaskIntentArtwork},
		{"retain writing classification", "请批改", k12.ImageTaskIntentWriting, k12.ImageTaskIntentWriting},
		{"retain original classification", "请处理", k12.ImageTaskIntentCompletedHomework, k12.ImageTaskIntentCompletedHomework},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newImageTaskHTTPFixture(t)
			fixture.classifier.result = usecase.ImageTaskClassification{
				Intent: tc.classified, IntentEvidence: []string{"visible image evidence"}, Confidence: .99,
			}
			fixture.coordinator.Grading = &problemGroundingResultGradingStub{
				jobID: "grading-job", projection: usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageAssessing},
			}
			body := strings.Replace(createImageTaskBody(fixture.assetID, "photo"), "请处理", tc.message, 1)
			created, payload := do(t, fixture.handler, http.MethodPost, "/image-tasks", body)
			initial, ok := payload["dispatch"].(map[string]any)
			if created.Code != http.StatusOK || !ok || initial["task_intent"] != "unknown" {
				t.Fatalf("unclassified image should remain unknown: %#v", payload)
			}
			id := initial["dispatch_id"].(string)
			_, envelope := waitImageTaskHTTPState(t, fixture, id, func(d map[string]any) bool {
				return d["status"] == "routed"
			})
			detail := envelope["dispatch"].(map[string]any)
			response, result := do(t, fixture.handler, http.MethodGet, "/image-tasks/"+id+"/result?agent=mingming", "")
			if detail["task_intent"] != string(tc.want) || response.Code != http.StatusOK || result["task_intent"] != string(tc.want) {
				t.Fatalf("public dispatch/result intent = %v/%v, result HTTP %d; want %q", detail["task_intent"], result["task_intent"], response.Code, tc.want)
			}
			stored, err := fixture.coordinator.Records.GetImageTaskDispatch(t.Context(), "mingming", id)
			if err != nil || stored.TaskIntent != tc.classified {
				t.Fatalf("original classification = %q, err=%v; want %q", stored.TaskIntent, err, tc.classified)
			}
		})
	}
}
