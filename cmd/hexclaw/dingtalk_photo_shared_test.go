package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/records"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestSharedK12PhotoFreezesBeforeDownloadAndCompletesAllPages(t *testing.T) {
	for _, outcome := range []adapter.DeliveryStatus{adapter.DeliveryDelivered, adapter.DeliveryFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			verifySharedK12PhotoPages(t, outcome)
		})
	}
}

func TestSharedK12PhotoCancellationSettlesOnRecovery(t *testing.T) {
	ctx := t.Context()
	deps, transport := statusNoticeDeps(t)
	inbound := k12usecase.NewInboundPhotoCoordinator(deps.Records)
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		Inbound: inbound, ReplyBatches: deps, Router: k12PhotoTestRouter(t, true, k12TutorScenario),
		ResolveSharedRoute: func(k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
			return k12.ImageTaskRouteSnapshot{Provider: "isolated", Model: "vision"}, nil
		},
	})
	msg := k12PhotoTestMessage()
	route, handled, err := runtime.PrepareSharedPhoto(ctx, msg, msg)
	if err != nil || !handled {
		t.Fatalf("prepare handled=%v, err=%v", handled, err)
	}
	if admitErr := runtime.AdmitSharedPhoto(ctx, msg, route); admitErr != nil {
		t.Fatal(admitErr)
	}
	identity, err := k12DingtalkInboundIdentity(msg)
	if err != nil {
		t.Fatal(err)
	}
	original, err := inbound.ResumeByIdentity(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	classifier := &terminalProjectionClassifier{}
	imageTasks := &k12usecase.ImageTaskCoordinator{
		Records: deps.Records, Classifier: classifier,
		ReadAsset: func(string, string) ([]byte, error) { return original.Asset.Bytes, nil },
		ResolveRoute: func(snapshot k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
			snapshot.Route = snapshot.Provider + "/" + snapshot.Model
			snapshot.Capability = "vision"
			snapshot.PolicyVersion = "image-task-routing-v1"
			snapshot.PromptVersion = "image-task-classifier-v1"
			snapshot.SelectionSource = "explicit"
			snapshot.TimeoutMS = 120000
			return snapshot, nil
		},
	}
	runtime.imageTasks = imageTasks
	agent := route.AgentName
	receiptID := original.Receipt.ReceiptID
	asset := "asset://" + agent + "/" + strings.TrimPrefix(original.Asset.Digest, "sha256:") + ".png"
	view, created, err := runtime.imageTasks.Create(ctx, k12usecase.CreateImageTaskInput{
		AgentName: agent, LearnerID: agent, SourceKind: k12.ImageTaskSourceIM,
		SourceRef: "dingtalk-inbound:" + receiptID, SourceSessionID: route.SessionID,
		SourceAssetRefs: []string{asset}, MessageIntent: "grade homework", AttemptGeneration: 1,
		RouteRequest: k12.ImageTaskRouteSnapshot{Provider: route.Provider, Model: route.Model, SelectionSource: "explicit"},
	})
	if err != nil || !created {
		t.Fatalf("create task created=%v, err=%v", created, err)
	}
	dispatch := view.Dispatch
	original.Dispatch, err = inbound.RecordImageTask(ctx, agent, receiptID, original.Dispatch.Version, dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, cancelErr := imageTasks.Cancel(ctx, agent, dispatch.DispatchID, dispatch.Version); cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if done, advanceErr := runtime.advanceImageTask(ctx, original); advanceErr != nil || !done {
		t.Fatalf("advance canceled task done=%v, err=%v", done, advanceErr)
	}
	for range 2 {
		current, readErr := inbound.ResumeByIdentity(ctx, identity)
		if readErr != nil || current.Dispatch.FailureKind != k12InboundPhotoCancelled {
			t.Fatalf("read inbound cancellation kind=%q, err=%v", current.Dispatch.FailureKind, readErr)
		}
		if done, recoverErr := runtime.advance(ctx, current); recoverErr != nil || !done {
			t.Fatalf("recover canceled task done=%v, err=%v", done, recoverErr)
		}
	}
	outcome, err := runtime.SharedPhotoOutcome(ctx, msg, route)
	if err != nil || outcome != adapter.DeliveryFailed || len(transport.sends) != 0 || classifier.calls != 0 {
		t.Fatalf("outcome=%q, err=%v, sends=%d, classifier=%d", outcome, err, len(transport.sends), classifier.calls)
	}
}

func verifySharedK12PhotoPages(t *testing.T, outcome adapter.DeliveryStatus) {
	t.Helper()
	ctx := t.Context()
	deps, _ := statusNoticeDeps(t)
	coordinator := k12usecase.NewInboundPhotoCoordinator(deps.Records)
	lookups := 0
	rt := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		Inbound: coordinator, Router: k12PhotoTestRouter(t, true, k12TutorScenario),
		Check: func(context.Context, *adapter.Message) error {
			t.Fatal("public admission already checked this source")
			return nil
		},
		ResolveRoute: func(k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
			t.Fatal("shared prepare reached the business capability probe")
			return k12.ImageTaskRouteSnapshot{}, nil
		},
		ResolveSharedRoute: func(k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
			lookups++
			return k12.ImageTaskRouteSnapshot{Provider: "test-provider", Model: "original-vision"}, nil
		},
	})
	msg := k12PhotoTestMessage()
	early := *msg
	early.Attachments = nil
	route, handled, err := rt.PrepareSharedPhoto(ctx, &early, &early)
	if err != nil || !handled || route.Kind != "k12" || route.Model != "original-vision" || lookups != 1 {
		t.Fatalf("prepare=%+v handled=%v err=%v", route, handled, err)
	}
	if _, found, err := rt.LookupInboundPhotoReceipt(ctx, &early, &early); err != nil || found {
		t.Fatalf("unexpected legacy intake found=%v err=%v", found, err)
	}
	checkDone := func(want adapter.DeliveryStatus) {
		t.Helper()
		done, err := rt.SharedPhotoOutcome(ctx, &early, route)
		if err != nil || done != want {
			t.Fatalf("terminal=%v want=%v err=%v", done, want, err)
		}
	}
	checkDone("")
	_, other := originalPhotoReportFixture(t)
	page := msg.Attachments[0]
	page.Data = base64.StdEncoding.EncodeToString(other)
	msg.Attachments = append(msg.Attachments, page)
	for range 2 {
		if err := rt.AdmitSharedPhoto(ctx, msg, route); err != nil {
			t.Fatal(err)
		}
	}
	if lookups != 1 {
		t.Fatalf("frozen commit repeated route selection %d times", lookups)
	}
	changed := *msg
	changed.Attachments = []adapter.Attachment{page, msg.Attachments[0]}
	if err := rt.AdmitSharedPhoto(ctx, &changed, route); !errors.Is(err, k12storage.ErrInboundPhotoConflict) {
		t.Fatalf("reordered pages error=%v", err)
	}
	checkDone("")
	receipt, found, err := rt.LookupInboundPhotoReceipt(ctx, &early, &early)
	if err != nil || !found || receipt.PageCount != 2 || receipt.BindingID != route.BindingID || receipt.AgentName != route.AgentName || receipt.CommandJSON != string(route.DomainData) {
		t.Fatalf("frozen source lost: %+v %v", receipt, err)
	}
	var command k12DingtalkInboundPhotoCommand
	if json.Unmarshal(route.DomainData, &command) != nil || command.StatusNoticeVersion != k12DingtalkSharedPhotoStatusNoticeVersion {
		t.Fatal("notice owner was not frozen")
	}
	for i := range 2 {
		key := receipt.Identity
		key.PageIndex = i
		current, err := coordinator.ResumeByIdentity(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		submitted, err := coordinator.RecordImageTask(ctx, receipt.AgentName, current.Receipt.ReceiptID, current.Dispatch.Version, "task-"+current.Receipt.ReceiptID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 || outcome == adapter.DeliveryDelivered {
			id := current.Receipt.ReceiptID
			decided, err := coordinator.RecordRoutingDecision(ctx, receipt.AgentName, id, submitted.Version, k12usecase.InboundPhotoRouteNewSubmission)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := coordinator.RecordFinalArtifact(ctx, receipt.AgentName, id, decided.Version, "final-artifact-"+id)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := coordinator.BindReplyBatch(ctx, receipt.AgentName, id, artifact.Version, "delivery-batch-"+id)
			if err != nil {
				t.Fatal(err)
			}
			checkDone("")
			if _, err = coordinator.CompleteReply(ctx, receipt.AgentName, id, bound.Version); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err = coordinator.FailTerminal(ctx, receipt.AgentName, current.Receipt.ReceiptID, submitted.Version, k12usecase.InboundPhotoTerminalStageImageTask, "known_failure"); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			checkDone("")
		} else {
			checkDone(outcome)
		}
	}
	drifted := route
	drifted.BindingID += "x"
	if done, err := rt.SharedPhotoOutcome(ctx, &early, drifted); err == nil || done != "" {
		t.Fatalf("target drift accepted: %v %v", done, err)
	}
}

func TestSharedK12PhotoKeepsOneNoticeOwnerAndOriginalFailureFeedback(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	b := terminalImageTaskBundle()
	var cmd k12DingtalkInboundPhotoCommand
	if err := json.Unmarshal([]byte(b.Receipt.CommandJSON), &cmd); err != nil {
		t.Fatal(err)
	}
	cmd.StatusNoticeVersion = k12DingtalkSharedPhotoStatusNoticeVersion
	raw, _ := json.Marshal(cmd)
	b.Receipt.CommandJSON = string(raw)
	rt := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: d, RecallStatusNotice: func(context.Context, string, string) error {
		t.Fatal("shared source owns accepted cleanup")
		return nil
	}})
	for range 2 {
		if err := rt.sendStatusNotice(t.Context(), b, "accepted"); err != nil {
			t.Fatal(err)
		}
		if err := rt.recallAcceptedStatusNotice(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	if len(transport.sends) != 0 {
		t.Fatal("K12 duplicated shared accepted")
	}
	for _, state := range []string{"failed", "outcome_unknown"} {
		for range 2 {
			if err := rt.sendStatusNotice(t.Context(), b, state); err != nil {
				t.Fatalf("%s: %v", state, err)
			}
		}
	}
	if len(transport.sends) != 2 {
		t.Fatalf("independent failed/unknown notices sent %d times", len(transport.sends))
	}
}

func TestSharedK12LegacyReceiptRequiresSamePhysicalInstance(t *testing.T) {
	frozen := inboundPhotoBundleFixture([]byte("legacy"))
	lookup := &inboundPhotoCoordinatorFake{resumeByIdentity: func(k k12usecase.InboundPhotoIdentity) (k12usecase.InboundPhotoBundle, error) {
		if k == frozen.Receipt.Identity {
			return frozen, nil
		}
		return k12usecase.InboundPhotoBundle{}, records.ErrNotFound
	}}
	name := &adapter.Message{Platform: adapter.PlatformDingtalk, InstanceID: frozen.Receipt.Identity.InstanceID, ID: frozen.Receipt.Identity.ProviderMessageID, ChatID: frozen.Receipt.Identity.ChatID, UserID: "parent"}
	stable := *name
	stable.InstanceID = "instance-A"
	for _, resolved := range []string{"instance-A", "instance-B"} {
		rt := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: lookup, ResolveInstanceID: func(string, string) (string, error) { return resolved, nil }})
		got, found, err := rt.LookupInboundPhotoReceipt(t.Context(), name, &stable)
		if err != nil || found != (resolved == stable.InstanceID) || (found && got.ReceiptID != frozen.Receipt.ReceiptID) {
			t.Fatalf("resolved=%s got=%+v found=%v err=%v", resolved, got, found, err)
		}
	}
}
