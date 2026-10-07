package apihttp_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type imageTaskKnownTechnicalHTTPRuntime struct {
	imageTaskHTTPRetryableGradingStub
	knownCalls                  int
	agent, owner, dispatch, job string
	version                     int
}

func (s *imageTaskKnownTechnicalHTTPRuntime) RetryPhotoGradingJobForKnownLocalTechnical(
	_ context.Context, agent, owner, dispatch, job string, version int,
) (usecase.GradingJobView, bool, error) {
	s.knownCalls++
	s.agent, s.owner, s.dispatch, s.job, s.version = agent, owner, dispatch, job, version
	return usecase.GradingJobView{}, true, nil
}

// 公共入口验证专用 intent 和服务派生归属；实际事务、运行和产物由同范围真实 SQLite 回归验证。
func TestKnownLocalTechnicalHTTPIntentAndDefaultContract(t *testing.T) {
	for _, test := range []struct {
		name, extra            string
		status, known, generic int
	}{
		{"fixed_intent", `,"intent":"known_local_technical"`, http.StatusOK, 1, 0},
		{"default", ``, http.StatusOK, 0, 1},
		{"unknown_intent", `,"intent":"force_terminal"`, http.StatusBadRequest, 0, 0},
		{"client_owner_rejected", `,"intent":"known_local_technical","owner_scope":"another-owner"`, http.StatusBadRequest, 0, 0},
		{"client_route_rejected", `,"intent":"known_local_technical","model":"another-model"`, http.StatusBadRequest, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImageTaskHTTPFixture(t)
			fixture.classifier.result = usecase.ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: .99, IntentEvidence: []string{"worksheet"}}
			runtime := &imageTaskKnownTechnicalHTTPRuntime{imageTaskHTTPRetryableGradingStub: imageTaskHTTPRetryableGradingStub{jobID: "known-http-original-job"}}
			fixture.coordinator.Grading = runtime
			created, _, err := fixture.coordinator.Create(context.Background(), usecase.CreateImageTaskInput{
				OwnerScope: usecase.DefaultLocalOwnerScope, AgentName: "mingming", LearnerID: "mingming",
				SourceKind: k12.ImageTaskSourceDesktop, SourceRef: "known-http-image", SourceSessionID: "known-http-session",
				SourceAssetRefs: []string{fixture.assetID}, AttemptGeneration: 1,
				RouteRequest: k12.ImageTaskRouteSnapshot{Provider: "hexclaw-gpt", Model: "gpt-5.6-sol", SelectionSource: "explicit"},
			})
			if err != nil {
				t.Fatal(err)
			}
			view, err := fixture.coordinator.Run(context.Background(), "mingming", created.Dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			response, _ := do(t, fixture.handler, http.MethodPost, "/image-tasks/"+view.Dispatch.DispatchID+"/retry",
				fmt.Sprintf(`{"agent":"mingming","version":%d%s}`, view.Dispatch.Version, test.extra))
			if response.Code != test.status || runtime.knownCalls != test.known || runtime.genericRetryCalls != test.generic {
				t.Fatalf("status=%d known=%d generic=%d body=%s", response.Code, runtime.knownCalls, runtime.genericRetryCalls, response.Body.String())
			}
			if test.known == 1 && (runtime.agent != "mingming" || runtime.owner != usecase.DefaultLocalOwnerScope || runtime.dispatch != view.Dispatch.DispatchID || runtime.job != runtime.jobID || runtime.version != view.Dispatch.Version) {
				t.Fatalf("server-derived recovery identity: %+v", runtime)
			}
		})
	}
}
