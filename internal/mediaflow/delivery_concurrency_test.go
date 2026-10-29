package mediaflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/storage/sqlite"
)

type concurrentDeliveryRepository struct{ *explicitDeliveryRepository }

// 分页发现也交给显式编排，避免定时推进器成为未计入的竞争调用。
func (*concurrentDeliveryRepository) PendingDeliveriesAfter(context.Context, string, int) ([]*mediaflow.Delivery, error) {
	return nil, nil
}

func TestConcurrentMediaDeliveryPreservesImagePreparation(t *testing.T) {
	svc, store, _, id, generations := deliveryFixture(t, 2, func(repo *sqlite.MediaRepository) mediaflow.Repository {
		return &concurrentDeliveryRepository{&explicitDeliveryRepository{repo}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	imagePreparing, releaseImage := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseImage) }) }
	var workers sync.WaitGroup
	defer func() {
		release()
		cancel()
		workers.Wait()
	}()
	var textPrepares, imagePrepares, textSends, imageSends, queries atomic.Int32
	partIndex := func(payload json.RawMessage) (int, error) {
		var value struct {
			Part int `json:"part"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return 0, err
		}
		if value.Part < 0 || value.Part > 1 {
			return 0, fmt.Errorf("unexpected delivery part %d", value.Part)
		}
		return value.Part, nil
	}
	transport := controlledDelivery{
		prepare: func(ctx context.Context, payload json.RawMessage) (string, error) {
			index, err := partIndex(payload)
			if err != nil {
				return "", err
			}
			if index == 0 {
				textPrepares.Add(1)
				return "text-resource", nil
			}
			if count := imagePrepares.Add(1); count != 1 {
				return "", fmt.Errorf("image preparation repeated: %d", count)
			}
			close(imagePreparing)
			select {
			case <-releaseImage:
				return "prepared-image", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		send: func(_ context.Context, payload json.RawMessage, prepared string) (mediaflow.DeliveryOutcome, error) {
			index, err := partIndex(payload)
			if err != nil {
				return mediaflow.DeliveryOutcome{}, err
			}
			if index == 0 {
				textSends.Add(1)
				return mediaflow.DeliveryOutcome{State: "accepted", ReceiptRef: "text-receipt"}, nil
			}
			if prepared != "prepared-image" {
				return mediaflow.DeliveryOutcome{}, fmt.Errorf("prepared image reference changed: %q", prepared)
			}
			imageSends.Add(1)
			return mediaflow.DeliveryOutcome{State: "delivered", ReceiptRef: "image-receipt"}, nil
		},
		query: func(_ context.Context, receipt string) (mediaflow.DeliveryOutcome, error) {
			queries.Add(1)
			if receipt != "text-receipt" {
				return mediaflow.DeliveryOutcome{}, fmt.Errorf("unexpected receipt %q", receipt)
			}
			return mediaflow.DeliveryOutcome{State: "delivered"}, nil
		},
	}
	svc.SetDeliveryResolver(func(context.Context, *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
		return transport, nil, nil
	})
	first, second := make(chan error, 1), make(chan error, 1)
	workers.Add(1)
	go func() { defer workers.Done(); first <- svc.Deliver(ctx, "owner", id) }()
	select {
	case <-imagePreparing:
	case <-ctx.Done():
		t.Fatal("image preparation was not reached")
	}
	workers.Add(1)
	go func() { defer workers.Done(); second <- svc.Deliver(ctx, "owner", id) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("concurrent delivery advance failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("concurrent delivery advance did not return")
	}
	// 旧推进器会在此写回文字回执；单推进者实现可跳过忙碌调用，两者都不应丢图片准备结果。
	release()
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("image preparation owner failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("image preparation owner did not finish")
	}
	// 并发调用若被跳过，下一次正常推进仍须查询同一文字回执，而不是重发任何部分。
	if err := svc.Deliver(ctx, "owner", id); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.MediaRepository().Delivery(ctx, "owner", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivery.Parts) != 2 || delivery.State != "delivered" ||
		delivery.Parts[0].State != "delivered" || delivery.Parts[0].ReceiptRef != "text-receipt" ||
		delivery.Parts[1].State != "delivered" || delivery.Parts[1].PreparedRef != "prepared-image" ||
		delivery.Parts[1].ReceiptRef != "image-receipt" || textPrepares.Load() != 1 || imagePrepares.Load() != 1 ||
		textSends.Load() != 1 || imageSends.Load() != 1 || queries.Load() != 1 || generations.Load() != 1 {
		t.Fatalf("concurrent delivery lost preparation or repeated work: state=%s revision=%d parts=%+v text_prepare=%d image_prepare=%d text_send=%d image_send=%d queries=%d fixture_generations=%d",
			delivery.State, delivery.Revision, delivery.Parts, textPrepares.Load(), imagePrepares.Load(), textSends.Load(), imageSends.Load(), queries.Load(), generations.Load())
	}
}
