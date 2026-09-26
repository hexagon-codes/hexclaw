package apihttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const classificationResponseStrings = `{"task_intent":"unknown","intent_evidence":["作答笔迹不明确"],"confidence":0.5,"confirmation_candidates":["completed_homework","blank_worksheet"]}`
const classificationResponseObjects = `{"task_intent":"unknown","intent_evidence":["作答笔迹不明确"],"confidence":0.5,"confirmation_candidates":[{"intent":"completed_homework"},{"intent":"blank_worksheet"}]}`
const classificationResponseHomework = `{"task_intent":"completed_homework","intent_evidence":["题目下方有学生作答"],"confidence":0.99,"confirmation_candidates":[]}`

func classificationReparseBody(agent string, version int) string {
	value, _ := json.Marshal(map[string]any{"agent": agent, "version": version})
	return string(value)
}

func prepareClassificationResponseTask(t *testing.T, fixture imageTaskHTTPFixture) usecase.ImageTaskView {
	t.Helper()
	view, created, err := fixture.coordinator.Create(context.Background(), usecase.CreateImageTaskInput{
		AgentName: "mingming", LearnerID: "learner-1", SourceKind: k12.ImageTaskSourceDesktop,
		SourceRef: "response-receipt", SourceSessionID: "session-response", SourceAssetRefs: []string{fixture.assetID},
		MessageIntent: "请处理", AttemptGeneration: 1,
	})
	if err != nil || !created {
		t.Fatalf("prepare task: created=%v err=%v", created, err)
	}
	return view
}

func TestImageTaskClassificationResponseReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		status k12.ImageTaskInvocationStatus
	}{
		{"string_candidates", classificationResponseStrings, k12.ImageTaskInvocationSucceeded},
		{"object_candidates_rejected", classificationResponseObjects, k12.ImageTaskInvocationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newImageTaskHTTPFixture(t)
			calls := 0
			fixture.coordinator.Classifier = engineadapter.NewImageTaskAdapter(func(_ context.Context, _ []byte, prompt string) (string, error) {
				calls++
				if !strings.Contains(prompt, `例如 ["completed_homework","blank_worksheet"]`) || !strings.Contains(prompt, "不能是对象") {
					t.Fatal("classification request lacks concrete candidate contract")
				}
				return tc.raw, nil
			})
			prepared := prepareClassificationResponseTask(t, fixture)
			_, runErr := fixture.coordinator.Run(context.Background(), "mingming", prepared.Dispatch.DispatchID)
			if (runErr != nil) != (tc.status == k12.ImageTaskInvocationFailed) {
				t.Fatalf("classification error=%v", runErr)
			}
			inv, err := fixture.coordinator.Records.GetLatestClassificationInvocation(context.Background(), "mingming", prepared.Dispatch.DispatchID)
			if err != nil || inv.Status != tc.status || calls != 1 {
				t.Fatalf("receipt status=%s calls=%d err=%v", inv.Status, calls, err)
			}
			response, present, err := k12storage.ReadImageTaskClassificationResponse(inv)
			if err != nil || !present || response.RawResponse != tc.raw || response.RawResponseDigest == "" {
				t.Fatalf("complete response not retained: present=%v err=%v", present, err)
			}
			if tc.status == k12.ImageTaskInvocationFailed {
				view, err := fixture.coordinator.Get(context.Background(), "mingming", prepared.Dispatch.DispatchID)
				if err != nil || view.Dispatch.Status != k12.ImageTaskStatusFailed || !view.Dispatch.RetrySafe {
					t.Fatalf("known parse failure changed to unknown: %+v err=%v", view.Dispatch, err)
				}
				rec, _ := do(t, fixture.handler, http.MethodPost, "/image-tasks/"+prepared.Dispatch.DispatchID+"/reparse", classificationReparseBody("mingming", view.Dispatch.Version))
				if rec.Code != http.StatusConflict || calls != 1 {
					t.Fatalf("invalid response was guessed or resent: status=%d calls=%d", rec.Code, calls)
				}
				after, _ := fixture.coordinator.Records.GetLatestClassificationInvocation(context.Background(), "mingming", prepared.Dispatch.DispatchID)
				if after.ResultJSON != inv.ResultJSON || after.Status != inv.Status || after.UpdatedAt != inv.UpdatedAt {
					t.Fatal("failed local reparse changed original receipt")
				}
			}
		})
	}
}

func TestImageTaskClassificationReparsePreservesResponseAndMakesNoProviderCall(t *testing.T) {
	fixture := newImageTaskHTTPFixture(t)
	calls := 0
	fixture.coordinator.Classifier = engineadapter.NewImageTaskAdapter(func(context.Context, []byte, string) (string, error) {
		calls++
		t.Fatal("local reparse sent a provider request")
		return "", nil
	})
	prepared := prepareClassificationResponseTask(t, fixture)
	ctx := context.Background()
	invID := prepared.Dispatch.ClassificationInvocationID
	if _, claimed, err := fixture.coordinator.Records.ClaimImageTaskInvocationSend(ctx, "mingming", invID, "original-request", prepared.Dispatch.CreatedAt); err != nil || !claimed {
		t.Fatalf("claim original: claimed=%v err=%v", claimed, err)
	}
	if err := fixture.coordinator.Records.SaveImageTaskClassificationResponse(ctx, "mingming", invID, classificationResponseHomework); err != nil {
		t.Fatal(err)
	}
	if err := fixture.coordinator.Records.FailImageTaskInvocation(ctx, "mingming", invID, "classification_provider_failed", false, true); err != nil {
		t.Fatal(err)
	}
	before, _ := fixture.coordinator.Records.GetImageTaskInvocation(ctx, "mingming", invID)
	failed, _ := fixture.coordinator.Get(ctx, "mingming", prepared.Dispatch.DispatchID)
	path := "/image-tasks/" + prepared.Dispatch.DispatchID + "/reparse"
	for _, body := range []string{
		classificationReparseBody("gege", failed.Dispatch.Version),
		classificationReparseBody("mingming", failed.Dispatch.Version+1),
	} {
		rec, _ := do(t, fixture.handler, http.MethodPost, path, body)
		if rec.Code == http.StatusOK {
			t.Fatalf("invalid owner or version accepted: %v", body)
		}
	}
	for i := 0; i < 2; i++ {
		rec, _ := do(t, fixture.handler, http.MethodPost, path, classificationReparseBody("mingming", failed.Dispatch.Version))
		if rec.Code != http.StatusOK {
			t.Fatalf("local reparse/replay: %d %s", rec.Code, rec.Body.String())
		}
	}
	after, err := fixture.coordinator.Records.GetImageTaskInvocation(ctx, "mingming", invID)
	if err != nil || after.Status != k12.ImageTaskInvocationSucceeded || after.Attempt != before.Attempt || after.StartedAt != before.StartedAt || after.ProviderRequestKey != before.ProviderRequestKey || calls != 0 {
		t.Fatalf("original request identity changed: after=%+v calls=%d err=%v", after, calls, err)
	}
	var decision k12storage.ImageTaskRoutingDecision
	if err := json.Unmarshal([]byte(after.ResultJSON), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.RawResponse != classificationResponseHomework || decision.OriginalParseFailure == nil || decision.OriginalParseFailure.Kind != before.ErrorKind || decision.OriginalParseFailure.FinishedAt != before.FinishedAt || decision.OriginalParseFailure.DispatchVersion != failed.Dispatch.Version {
		t.Fatal("original response or parse failure evidence lost")
	}
	view, err := fixture.coordinator.Get(ctx, "mingming", prepared.Dispatch.DispatchID)
	if err != nil || view.Dispatch.Status != k12.ImageTaskStatusRouted || view.Homework == nil || view.Homework.GradingJobID != "" {
		t.Fatalf("local reparse did not preserve downstream execution boundary: %+v err=%v", view, err)
	}
	var count int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM k12_image_task_invocations WHERE dispatch_id=?`, prepared.Dispatch.DispatchID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("local reparse invented a model invocation: count=%d err=%v", count, err)
	}
}

func TestImageTaskClassificationResponseDeadlineAndIntegrity(t *testing.T) {
	fixture := newImageTaskHTTPFixture(t)
	fixture.coordinator.Classifier = engineadapter.NewImageTaskAdapter(func(context.Context, []byte, string) (string, error) {
		t.Fatal("retained response was sent again")
		return "", nil
	})
	prepared := prepareClassificationResponseTask(t, fixture)
	ctx := context.Background()
	id := prepared.Dispatch.ClassificationInvocationID
	if _, claimed, err := fixture.coordinator.Records.ClaimImageTaskInvocationSend(ctx, "mingming", id, "original", prepared.Dispatch.CreatedAt); err != nil || !claimed {
		t.Fatalf("claim: %v", err)
	}
	if err := fixture.coordinator.Records.SaveImageTaskClassificationResponse(ctx, "mingming", id, classificationResponseStrings); err != nil {
		t.Fatal(err)
	}
	inv, _ := fixture.coordinator.Records.GetImageTaskInvocation(ctx, "mingming", id)
	dispatch, expired, changed, err := fixture.coordinator.Records.ExpireImageTaskInvocation(ctx, "mingming", prepared.Dispatch.DispatchID, id, inv.DeadlineAt)
	if err != nil || !changed || expired.Status != k12.ImageTaskInvocationFailed || expired.ErrorKind != "classification_response_pending_parse" || !dispatch.RetrySafe {
		t.Fatalf("known response became unknown: %+v changed=%v err=%v", expired, changed, err)
	}
	if _, err := fixture.db.Exec(`UPDATE k12_image_task_invocations SET result_json=replace(result_json,?,?) WHERE invocation_id=?`, "sha256:", "invalid:", id); err != nil {
		t.Fatal(err)
	}
	rec, _ := do(t, fixture.handler, http.MethodPost, "/image-tasks/"+prepared.Dispatch.DispatchID+"/reparse", classificationReparseBody("mingming", dispatch.Version))
	if rec.Code != http.StatusConflict {
		t.Fatalf("invalid response digest advanced: %d", rec.Code)
	}
	after, _ := fixture.coordinator.Records.GetImageTaskInvocation(ctx, "mingming", id)
	if after.Status != k12.ImageTaskInvocationFailed || after.FinishedAt != expired.FinishedAt {
		t.Fatal("invalid receipt was rewritten")
	}
}

func TestImageTaskClassificationReparseRejectsUnknownAndMissingResponse(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		fixture := newImageTaskHTTPFixture(t)
		fixture.coordinator.Classifier = engineadapter.NewImageTaskAdapter(func(context.Context, []byte, string) (string, error) {
			t.Fatal("reparse sent a provider request")
			return "", nil
		})
		prepared := prepareClassificationResponseTask(t, fixture)
		ctx := context.Background()
		id := prepared.Dispatch.ClassificationInvocationID
		if _, claimed, err := fixture.coordinator.Records.ClaimImageTaskInvocationSend(ctx, "mingming", id, "original", prepared.Dispatch.CreatedAt); err != nil || !claimed {
			t.Fatal(err)
		}
		if unknown {
			if err := fixture.coordinator.Records.SaveImageTaskClassificationResponse(ctx, "mingming", id, classificationResponseStrings); err != nil {
				t.Fatal(err)
			}
		}
		if err := fixture.coordinator.Records.FailImageTaskInvocation(ctx, "mingming", id, "classification_provider_failed", unknown, !unknown); err != nil {
			t.Fatal(err)
		}
		view, _ := fixture.coordinator.Get(ctx, "mingming", prepared.Dispatch.DispatchID)
		rec, _ := do(t, fixture.handler, http.MethodPost, "/image-tasks/"+prepared.Dispatch.DispatchID+"/reparse", classificationReparseBody("mingming", view.Dispatch.Version))
		if rec.Code != http.StatusConflict {
			t.Fatalf("unknown=%v reparse status=%d", unknown, rec.Code)
		}
	}
}
