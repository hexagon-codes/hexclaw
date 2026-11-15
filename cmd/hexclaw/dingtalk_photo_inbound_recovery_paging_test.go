package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type pagedPhotoRecoveryCoordinator struct {
	*inboundPhotoCoordinatorFake
	pages   map[string][]k12usecase.InboundPhotoBundle
	cursors []string
	resumed chan string
}

func (f *pagedPhotoRecoveryCoordinator) RecoverableAfter(_ context.Context, after string, _ int) ([]k12usecase.InboundPhotoBundle, error) {
	f.cursors = append(f.cursors, after)
	return append([]k12usecase.InboundPhotoBundle(nil), f.pages[after]...), nil
}

func (f *pagedPhotoRecoveryCoordinator) Resume(ctx context.Context, _, receipt string) (k12usecase.InboundPhotoBundle, error) {
	f.resumed <- receipt
	<-ctx.Done()
	return k12usecase.InboundPhotoBundle{}, ctx.Err()
}

func TestDingTalkPhotoRecoveryContinuesAfterParkedFirstPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	first := inboundPhotoBundleFixture([]byte("first"))
	first.Dispatch.DispatchID, first.Receipt.ReceiptID = "dispatch-a", "parked-original"
	first.Dispatch.ReceiptID = first.Receipt.ReceiptID
	second := inboundPhotoBundleFixture([]byte("second"))
	second.Dispatch.DispatchID, second.Receipt.ReceiptID = "dispatch-b", "later-receipt"
	second.Dispatch.ReceiptID = second.Receipt.ReceiptID
	inbound := &pagedPhotoRecoveryCoordinator{
		inboundPhotoCoordinatorFake: &inboundPhotoCoordinatorFake{},
		pages: map[string][]k12usecase.InboundPhotoBundle{
			"": {first}, "dispatch-a": {second},
		},
		resumed: make(chan string, 2),
	}
	images := &fakeK12ImageTaskFacade{}
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{
		BaseContext: ctx, Inbound: inbound, ImageTasks: images,
	})
	count, err := runtime.Recover(ctx)
	if err != nil || count != 2 || !reflect.DeepEqual(inbound.cursors, []string{"", "dispatch-a", "dispatch-b"}) {
		t.Fatalf("recovery did not traverse each page once: count=%d cursors=%v err=%v", count, inbound.cursors, err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case receipt := <-inbound.resumed:
			if seen[receipt] {
				t.Fatalf("recovery dispatched the same receipt twice: %s", receipt)
			}
			seen[receipt] = true
		case <-ctx.Done():
			t.Fatal("parked first page prevented a later receipt from being scheduled")
		}
	}
	if !seen["parked-original"] || !seen["later-receipt"] || images.startCalls != 0 {
		t.Fatalf("recovery replaced existing work instead of observing it: seen=%v starts=%d", seen, images.startCalls)
	}
}

func TestDingTalkPhotoRecoveryRejectsNonAdvancingCursor(t *testing.T) {
	page := inboundPhotoBundleFixture([]byte("parked"))
	page.Dispatch.DispatchID = "dispatch-a"
	inbound := &pagedPhotoRecoveryCoordinator{
		inboundPhotoCoordinatorFake: &inboundPhotoCoordinatorFake{},
		pages: map[string][]k12usecase.InboundPhotoBundle{
			"": {page}, "dispatch-a": {page},
		},
	}
	runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound})
	count, err := runtime.Recover(t.Context())
	if err == nil || count != 1 || len(inbound.cursors) != 2 {
		t.Fatalf("non-advancing paging was retried: count=%d cursors=%v err=%v", count, inbound.cursors, err)
	}
}
