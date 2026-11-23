package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestDingTalkPhotoPageIsNotParkedWhileAutomaticRetryIsAvailable(t *testing.T) {
	view := usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusFailed, RetrySafe: true}}
	if inboundPhotoPageParked(view, usecase.InboundPhotoRouteNewSubmission) {
		t.Fatal("a later page can overtake an automatically retried page")
	}
	if !inboundPhotoPageParked(view, usecase.InboundPhotoRoutePending) {
		t.Fatal("a failure without an automatic route should release later pages")
	}
	view.ClassificationInvocationStatus = k12.ImageTaskInvocationOutcomeUnknown
	if !inboundPhotoPageParked(view, usecase.InboundPhotoRouteNewSubmission) {
		t.Fatal("an unresolved provider call is parked")
	}
}

func TestDingTalkPhotoPagesKeepOnePhysicalSourceAndSeparateImages(t *testing.T) {
	ctx := t.Context()
	deps, _ := statusNoticeDeps(t)
	inbound := usecase.NewInboundPhotoCoordinator(deps.Records)
	router := k12PhotoTestRouter(t, true, k12TutorScenario)
	if err := router.UpdateAgent(agentrouter.AgentConfig{Name: "child-tutor", Provider: "fixture", Model: "vision", Metadata: map[string]string{"scenario": k12TutorScenario}}); err != nil {
		t.Fatal(err)
	}
	rt := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound, Router: router})
	msg := k12PhotoTestMessage()
	_, second := originalPhotoReportFixture(t)
	other := msg.Attachments[0]
	other.Data = base64.StdEncoding.EncodeToString(second)
	msg.Attachments = append(msg.Attachments, other)
	identity, err := k12DingtalkInboundIdentity(msg)
	if err != nil {
		t.Fatal(err)
	}
	var firstIDs [2]string
	var pages [2]usecase.InboundPhotoBundle
	for attempt := range 2 {
		if handled, admitErr := rt.AdmitInboundPhoto(ctx, msg); !handled || admitErr != nil {
			t.Fatalf("attempt %d: handled=%v err=%v", attempt, handled, admitErr)
		}
		for i := range pages {
			identity.PageIndex = i
			pages[i], err = inbound.ResumeByIdentity(ctx, identity)
			if err != nil {
				t.Fatal(err)
			}
			page := pages[i]
			if page.Receipt.Identity != identity || page.Receipt.PageCount != len(pages) ||
				base64.StdEncoding.EncodeToString(page.Asset.Bytes) != msg.Attachments[i].Data {
				t.Fatalf("page %d did not preserve its photo and original message", i)
			}
			if attempt == 0 {
				firstIDs[i] = page.Receipt.ReceiptID
			} else if firstIDs[i] != page.Receipt.ReceiptID {
				t.Fatalf("replay created a new receipt for page %d", i)
			}
		}
	}
	if firstIDs[0] == firstIDs[1] || pages[0].Asset.Digest == pages[1].Asset.Digest {
		t.Fatal("two different pages shared a receipt or content digest")
	}
	ready, err := rt.earlierPhotoPagesSettled(ctx, pages[1])
	if err != nil || ready {
		t.Fatalf("second page skipped the first pending receipt: ready=%v err=%v", ready, err)
	}
	first, err := inbound.RecordImageTask(ctx, pages[0].Receipt.AgentName, firstIDs[0], pages[0].Dispatch.Version, "dispatch-page-one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = inbound.FailTerminal(ctx, pages[0].Receipt.AgentName, firstIDs[0], first.Version, usecase.InboundPhotoTerminalStageImageTask, "model_capability_unverified")
	if err != nil {
		t.Fatal(err)
	}
	if ready, err = rt.earlierPhotoPagesSettled(ctx, pages[1]); err != nil || !ready {
		t.Fatalf("failed first page blocked the next result: ready=%v err=%v", ready, err)
	}
	query := newK12DingtalkPhotoInboundQueryHandler(http.NotFoundHandler(), inbound, usecase.DefaultLocalOwnerScope)
	path := "/dingtalk-inbound?platform=dingtalk&instance_id=bot-1&chat_id=family-group&provider_message_id=msg-1&agent=child-tutor"
	for _, check := range []struct {
		suffix        string
		status, index int
	}{
		{"", 200, 0}, {"&page_index=1", 200, 1}, {"&page_index=2", 404, 0}, {"&page_index=-1", 400, 0},
	} {
		out := httptest.NewRecorder()
		query.ServeHTTP(out, httptest.NewRequestWithContext(ctx, http.MethodGet, path+check.suffix, nil))
		if out.Code != check.status {
			t.Fatalf("query %q: %d", check.suffix, out.Code)
		}
		if out.Code == 200 {
			var got k12InboundPhotoBundleProjection
			if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			page := got.Receipt.Identity
			if page.PageIndex == nil || *page.PageIndex != check.index || page.PageCount != 2 || page.ProviderMessageID != msg.ID || got.Receipt.ReceiptID != firstIDs[check.index] {
				t.Fatalf("wrong source page for %q: %+v", check.suffix, page)
			}
		}
	}
	msg.Attachments = msg.Attachments[:1]
	if _, err := rt.AdmitInboundPhoto(ctx, msg); !errors.Is(err, usecase.ErrInboundPhotoConflict) {
		t.Fatalf("changed page count reused a multi-page receipt: %v", err)
	}
}

func TestDingTalkPhotoBoundShortAnswerUsesFrozenAction(t *testing.T) {
	for _, action := range []string{"solve", "grade"} {
		t.Run(action, func(t *testing.T) {
			bundle := inboundPhotoBundleFixture([]byte("original"))
			bundle.Receipt.AgentName = "child-tutor"
			bundle.Dispatch.ProcessingStatus = usecase.InboundPhotoFinalArtifactReady
			bundle.Dispatch.RoutingDecision = usecase.InboundPhotoRouteNewSubmission
			bundle.Dispatch.ImageTaskID = "dispatch-1"
			bundle.Dispatch.DeliveryBatchID = "bound"
			bundle.Dispatch.ReplyStatus = usecase.InboundPhotoReplyBound
			target := inboundPhotoFrozenTarget(bundle)
			batches := &finalReplyBatchFake{existing: k12.DeliveryBatch{BatchID: "bound", Status: k12.DeliveryBatchDelivered,
				Receipts: []k12.DeliveryReceipt{{DeliveryID: "markdown", BindingID: target.BindingID, Target: target.Target, PartKind: messagecontent.PartMarkdown, PartOrdinal: 1, Status: k12.DeliveryDelivered}}}}
			coordinator := &inboundPhotoCoordinatorFake{bundle: bundle}
			rt := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: coordinator, ReplyBatches: batches,
				ImageTasks: &scopedSolveImageTasks{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, action: action}})
			done, err := rt.completeBoundReply(context.Background(), bundle)
			if (action == "solve") != done || (err == nil) != done || batches.prepareCalls != 0 || batches.queryCalls != 1 {
				t.Fatalf("bound %s: done=%v err=%v reads=%d sends=%d", action, done, err, batches.queryCalls, batches.prepareCalls)
			}
		})
	}
}
