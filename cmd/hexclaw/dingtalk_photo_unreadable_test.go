package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type unreadablePhotoImageTasks struct {
	*fakeK12ImageTaskFacade
	view   k12usecase.ImageTaskView
	result k12usecase.ImageTaskResult
}

func (f *unreadablePhotoImageTasks) Get(context.Context, string, string) (k12usecase.ImageTaskView, error) {
	return f.view, nil
}

func (f *unreadablePhotoImageTasks) Result(context.Context, string, string) (k12usecase.ImageTaskResult, error) {
	return f.result, nil
}

type unreadableReplyBindingFailure struct {
	k12InboundPhotoCoordinatorPort
	failOnce bool
}

var errUnreadableReplyBindingInterrupted = errors.New("controlled unreadable reply binding interruption")

func (p *unreadableReplyBindingFailure) BindReplyBatch(ctx context.Context, agent, receipt string, version int64, batch string) (k12usecase.InboundPhotoDispatch, error) {
	if p.failOnce {
		p.failOnce = false
		return k12usecase.InboundPhotoDispatch{}, errUnreadableReplyBindingInterrupted
	}
	return p.k12InboundPhotoCoordinatorPort.BindReplyBatch(ctx, agent, receipt, version, batch)
}

func TestDingTalkUnreadableCreativeResultCompletesOriginalReceipt(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "normal"
		if interrupted {
			name = "sent_before_binding_restart"
		}
		t.Run(name, func(t *testing.T) { testDingTalkUnreadableCreativeReceipt(t, interrupted) })
	}
}

func testDingTalkUnreadableCreativeReceipt(t *testing.T, interrupted bool) {
	ctx := context.Background()
	d, transport := statusNoticeDeps(t)
	inbound := k12usecase.NewInboundPhotoCoordinator(d.Records)
	fixture := terminalImageTaskBundle()
	command := strings.Replace(fixture.Receipt.CommandJSON, `"status_notice_version":"v1",`, "", 1)
	bundle, _, err := inbound.Admit(ctx, k12usecase.InboundPhotoAdmission{
		OwnerScope: fixture.Receipt.OwnerScope, AgentName: fixture.Receipt.AgentName,
		BindingID: fixture.Receipt.BindingID, Identity: fixture.Receipt.Identity,
		AssetName: fixture.Asset.Name, AssetMIME: fixture.Asset.MIME,
		AssetBytes: fixture.Asset.Bytes, CommandJSON: command,
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle.Dispatch, err = inbound.RecordImageTask(ctx, bundle.Receipt.AgentName, bundle.Receipt.ReceiptID, bundle.Dispatch.Version, "unreadable-image-task")
	if err != nil {
		t.Fatal(err)
	}
	intake := &k12.CreativeWorkIntake{
		IntakeID: "unreadable-intake", DispatchID: bundle.Dispatch.ImageTaskID,
		AgentName: bundle.Receipt.AgentName, WorkType: k12.WorkTypeWriting,
		Status:      k12.CreativeWorkIntakeUnreadable,
		OCREvidence: &k12.CreativeWorkIntakeOCREvidence{Outcome: "unreadable"},
	}
	dispatch := k12.ImageTaskDispatch{DispatchID: bundle.Dispatch.ImageTaskID, AgentName: bundle.Receipt.AgentName,
		Status: k12.ImageTaskStatusRouted, TaskIntent: k12.ImageTaskIntentWriting}
	images := &unreadablePhotoImageTasks{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{},
		view:   k12usecase.ImageTaskView{Dispatch: dispatch, Creative: intake},
		result: k12usecase.ImageTaskResult{Kind: "creative", Dispatch: dispatch, Creative: intake}}
	coordinator := &unreadableReplyBindingFailure{k12InboundPhotoCoordinatorPort: inbound, failOnce: interrupted}
	config := k12DingtalkPhotoInboundRuntimeConfig{Inbound: coordinator, ImageTasks: images, ReplyBatches: d}
	runtime := newK12DingtalkPhotoInboundRuntime(config)
	done := false
	for range 6 {
		bundle, err = inbound.Resume(ctx, bundle.Receipt.AgentName, bundle.Receipt.ReceiptID)
		if err != nil {
			t.Fatal(err)
		}
		done, err = runtime.advance(ctx, bundle)
		if interrupted && errors.Is(err, errUnreadableReplyBindingInterrupted) {
			runtime = newK12DingtalkPhotoInboundRuntime(config)
			continue
		}
		if err != nil || done {
			break
		}
	}
	if err != nil || !done || len(transport.sends) != 1 || images.startCalls != 0 {
		t.Fatalf("unreadable content did not complete once: done=%v sends=%d starts=%d err=%v", done, len(transport.sends), images.startCalls, err)
	}
	after, err := inbound.Resume(ctx, bundle.Receipt.AgentName, bundle.Receipt.ReceiptID)
	if err != nil || after.Dispatch.ReplyStatus != k12usecase.InboundPhotoReplyDelivered || after.Dispatch.TerminalStatus != "" {
		t.Fatalf("content outcome became a technical failure: %+v err=%v", after.Dispatch, err)
	}
	if after.Dispatch.FinalArtifactID != intake.IntakeID {
		t.Fatalf("content outcome identity was not frozen: %q", after.Dispatch.FinalArtifactID)
	}
	batch, err := d.QueryDeliveryBatch(ctx, after.Receipt.AgentName, after.Dispatch.DeliveryBatchID)
	if err != nil || len(batch.Receipts) != 1 || batch.Receipts[0].Target != inboundPhotoFrozenTarget(after).Target {
		t.Fatalf("content reply lost original target: %+v err=%v", batch, err)
	}
	restarted := newK12DingtalkPhotoInboundRuntime(config)
	if replayed, err := restarted.advance(ctx, after); err != nil || !replayed || len(transport.sends) != 1 {
		t.Fatalf("completed content replay sent again: done=%v sends=%d err=%v", replayed, len(transport.sends), err)
	}
}
