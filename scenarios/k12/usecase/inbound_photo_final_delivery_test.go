package usecase

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type inboundFinalDeliveryTransport struct {
	gradingFinalDeliveryTransport
	targets []ResolvedDeliveryTarget
}

func (f *inboundFinalDeliveryTransport) ResolveTextTargets(context.Context, string) ([]ResolvedDeliveryTarget, error) {
	return f.targets, nil
}

func TestInboundPhotoFinalDeliveryKeepsSourceAndReplaySQLite(t *testing.T) {
	for _, mode := range []string{"ready", "wrong_owner", "wrong_task", "wrong_task_owner", "target_changed", "missing_digest", "wrong_digest"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			image := validPNGFixture(t, "inbound-final-source")
			f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework, completedSourceImageFixture{data: image})
			target := k12.DeliveryTarget{Platform: "dingtalk", InstanceID: "source-instance", ChatID: "source-chat", Label: "dingtalk"}
			transport := &inboundFinalDeliveryTransport{targets: []ResolvedDeliveryTarget{
				{BindingID: "source-binding", Target: target},
				{BindingID: "other-binding", Target: k12.DeliveryTarget{Platform: "dingtalk", InstanceID: "other-instance", ChatID: "other-chat", Label: "dingtalk"}},
			}}
			f.o.deps.Delivery = transport
			bundle, _, err := f.store.AdmitInboundPhoto(ctx, InboundPhotoAdmission{
				OwnerScope: "guardian-final", AgentName: "mingming", BindingID: "source-binding",
				Identity:  InboundPhotoIdentity{Platform: target.Platform, InstanceID: target.InstanceID, ChatID: target.ChatID, ProviderMessageID: "source-message"},
				AssetName: "homework.png", AssetMIME: "image/png", AssetBytes: image, CommandJSON: `{}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			dispatch := f.dispatch
			if mode == "wrong_task" || mode == "wrong_task_owner" {
				repo := &PageAssetRepository{Records: f.store}
				owner := "guardian-final"
				assetImage := image
				if mode == "wrong_task_owner" {
					owner = "another-guardian"
					assetImage = validPNGFixture(t, "another-owned-source")
				}
				asset, err := repo.Persist(ctx, owner, "mingming", assetImage)
				if err != nil {
					t.Fatal(err)
				}
				coordinator := &ImageTaskCoordinator{Records: f.store, PageAssets: repo, Classifier: &imageTaskClassifierStub{result: ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: 1}}, ResolveRoute: imageTaskRouteForTest}
				in := testCreateImageTaskInput()
				in.OwnerScope, in.SourceRef, in.SourceAssetRefs = owner, "another-message", []string{asset.Metadata.PageAssetID}
				view, _, err := coordinator.Create(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				dispatch = view.Dispatch.DispatchID
			}
			if _, err := NewInboundPhotoCoordinator(f.store).RecordImageTask(ctx, "mingming", bundle.Receipt.ReceiptID, bundle.Dispatch.Version, dispatch); err != nil {
				t.Fatal(err)
			}
			before, err := f.store.GetInboundPhoto(ctx, "mingming", bundle.Receipt.ReceiptID)
			if err != nil {
				t.Fatal(err)
			}
			owner, digest := "guardian-final", f.old.ArtifactDigest
			switch mode {
			case "wrong_owner":
				owner = "another-guardian"
			case "target_changed":
				transport.targets = transport.targets[1:]
			case "missing_digest":
				digest = ""
			case "wrong_digest":
				digest = strings.Repeat("0", 64)
			}
			batch, _, err := f.o.deps.PrepareAndSendInboundPhotoFinalExact(ctx, owner, "mingming", f.old.ArtifactID, digest, bundle.Receipt.ReceiptID)
			if mode != "ready" {
				if err == nil || len(transport.sent) != 0 {
					t.Fatalf("invalid source reached delivery: err=%v sends=%d", err, len(transport.sent))
				}
				return
			}
			if err != nil || batch.Status != k12.DeliveryBatchDelivered || len(transport.sent) == 0 {
				t.Fatalf("source reply failed: %s %v", batch.Status, err)
			}
			for _, receipt := range transport.sent {
				if receipt.Target != target {
					t.Fatal("source reply reached another physical target")
				}
			}
			count := len(transport.sent)
			again, _, err := f.o.deps.PrepareAndSendInboundPhotoFinalExact(ctx, owner, "mingming", f.old.ArtifactID, digest, bundle.Receipt.ReceiptID)
			if err != nil || again.BatchID != batch.BatchID || len(transport.sent) != count {
				t.Fatal("source reply replay repeated a visible send")
			}
			after, err := f.store.GetInboundPhoto(ctx, "mingming", bundle.Receipt.ReceiptID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("source reply rewrote the original inbound receipt")
			}
			all, _, err := f.o.deps.PrepareAndSendGradingFinalArtifactExact(ctx, "mingming", f.old.ArtifactID, digest)
			if err != nil || all.BatchID == batch.BatchID {
				t.Fatalf("default all-target delivery collided with source reply: %v", err)
			}
			seen := map[string]bool{}
			for _, receipt := range transport.sent[count:] {
				seen[receipt.Target.InstanceID] = true
			}
			if len(seen) != 2 || !seen["source-instance"] || !seen["other-instance"] {
				t.Fatal("default delivery no longer reaches both bound targets")
			}
		})
	}
}
