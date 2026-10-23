package mediaflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// DeliveryPart 的冻结载荷由现有渠道协议解释，媒体领域不依赖具体适配器。
type DeliveryPart struct {
	Payload      json.RawMessage `json:"payload"`
	PreparedRef  string          `json:"prepared_ref,omitempty"`
	ReceiptRef   string          `json:"receipt_ref,omitempty"`
	State        string          `json:"state"`
	FailureStage string          `json:"failure_stage,omitempty"`
}

type Delivery struct {
	ReplyCompletion
	ID        string         `json:"id"`
	MessageID string         `json:"message_id"`
	State     string         `json:"state"`
	Revision  int64          `json:"revision"`
	Parts     []DeliveryPart `json:"parts,omitempty"`
}

type DeliveryOutcome struct {
	State      string
	ReceiptRef string
}

type DeliveryStatus struct {
	ID         string   `json:"delivery_id"`
	MessageID  string   `json:"message_id"`
	Platform   string   `json:"platform"`
	State      string   `json:"state"`
	PartStates []string `json:"part_states"`
}

// DeliveryStatuses 只投影已保存的交付事实，不把生成就绪解释为渠道已送达。
func (s *Service) DeliveryStatuses(ctx context.Context, owner, operationID string) ([]DeliveryStatus, error) {
	states := make([]DeliveryStatus, 0)
	query, ok := s.repo.(DeliveryResumeRepository)
	if !ok {
		return states, nil
	}
	records, err := query.DeliveriesForOperation(ctx, owner, operationID)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		state := DeliveryStatus{ID: record.ID, MessageID: record.MessageID, Platform: record.Platform, State: record.State, PartStates: make([]string, 0, len(record.Parts))}
		for _, part := range record.Parts {
			state.PartStates = append(state.PartStates, part.State)
		}
		states = append(states, state)
	}
	return states, nil
}

type DeliveryRepository interface {
	Delivery(context.Context, string, string) (*Delivery, error)
	SaveDelivery(context.Context, *Delivery, int64) error
	PendingDeliveries(context.Context, int) ([]*Delivery, error)
	InterruptDeliveries(context.Context) error
}

type DeliveryResumeRepository interface {
	DeliveriesForOperation(context.Context, string, string) ([]*Delivery, error)
}

type DeliveryTransport interface {
	Prepare(context.Context, json.RawMessage) (string, error)
	Send(context.Context, json.RawMessage, string) (DeliveryOutcome, error)
	Query(context.Context, string) (DeliveryOutcome, error)
}

type DeliveryResolver func(context.Context, *Delivery) (DeliveryTransport, []json.RawMessage, error)

type deliveryRuntime struct {
	mu      sync.RWMutex
	resolve DeliveryResolver
}

// SetDeliveryResolver 只接线原渠道准备/发送/回执能力，不建立第二发送队列。
func (s *Service) SetDeliveryResolver(resolve DeliveryResolver) {
	s.deliveries.mu.Lock()
	s.deliveries.resolve = resolve
	s.deliveries.mu.Unlock()
}

func (s *Service) Deliver(ctx context.Context, owner, id string) error {
	repo, ok := s.repo.(DeliveryRepository)
	if !ok {
		return fmt.Errorf("media delivery repository is unavailable")
	}
	d, err := repo.Delivery(ctx, owner, id)
	if err != nil {
		return err
	}
	err = s.advanceDelivery(ctx, repo, d)
	if !errors.Is(err, ErrConflict) {
		return err
	}
	// 另一推进者已取得同一交付，读回现有记录，不把竞争当失败或再次发送。
	_, err = repo.Delivery(ctx, owner, id)
	return err
}

func (s *Service) runDeliveries() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		repo, ok := s.repo.(DeliveryRepository)
		if ok {
			if records, err := repo.PendingDeliveries(s.ctx, 16); err == nil {
				for _, d := range records {
					if s.ctx.Err() != nil {
						return
					}
					_ = s.advanceDelivery(s.ctx, repo, d)
				}
			}
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) advanceDelivery(ctx context.Context, repo DeliveryRepository, d *Delivery) error {
	if d.State == "delivered" || d.State == "cancelled" {
		return nil
	}
	queryOnly := d.State == "unknown" || d.State == "failed"
	s.deliveries.mu.RLock()
	resolve := s.deliveries.resolve
	s.deliveries.mu.RUnlock()
	if resolve == nil {
		return fmt.Errorf("original media delivery transport is not available")
	}
	transport, plan, err := resolve(ctx, d)
	if err != nil {
		return err
	}
	if len(d.Parts) == 0 {
		if queryOnly {
			return nil
		}
		if len(plan) == 0 {
			return fmt.Errorf("media delivery plan is empty")
		}
		for _, payload := range plan {
			d.Parts = append(d.Parts, DeliveryPart{Payload: payload, State: "pending"})
		}
		if err := repo.SaveDelivery(ctx, d, d.Revision); err != nil {
			return err
		}
	}
	for i := range d.Parts {
		part := &d.Parts[i]
		if part.State == "delivered" {
			continue
		}
		if (part.State == "accepted" || part.State == "unknown") && part.ReceiptRef != "" {
			outcome, err := transport.Query(ctx, part.ReceiptRef)
			if err != nil {
				return err
			}
			if outcome.State == "delivered" || outcome.State == "failed" {
				part.State = outcome.State
				if outcome.State == "failed" {
					part.FailureStage = "send"
					queryOnly = true
				}
				d.State = deliveryState(d.Parts)
				if err := repo.SaveDelivery(ctx, d, d.Revision); err != nil {
					return err
				}
			}
			continue
		}
		if part.State == "unknown" || part.State == "failed" {
			queryOnly = true
			continue
		}
		if queryOnly {
			continue
		}
		if part.State == "preparing" || part.State == "sending" {
			return nil
		}
		if part.State == "pending" {
			part.State = "preparing"
			if err := repo.SaveDelivery(ctx, d, d.Revision); err != nil {
				return err
			}
			part = &d.Parts[i]
			prepared, err := transport.Prepare(ctx, part.Payload)
			if err != nil {
				var known interface{ ResourcePreparationKnown() bool }
				part.State = "unknown"
				if errors.As(err, &known) && known.ResourcePreparationKnown() {
					part.State = "failed"
					part.FailureStage = "prepare"
				}
				d.State = deliveryState(d.Parts)
				_ = repo.SaveDelivery(ctx, d, d.Revision)
				return err
			}
			part.PreparedRef = prepared
			part.State = "prepared"
			if err := repo.SaveDelivery(ctx, d, d.Revision); err != nil {
				return err
			}
			part = &d.Parts[i]
		}
		if part.State == "prepared" {
			for _, operationID := range d.Operations {
				op, err := s.repo.Get(ctx, d.Owner, operationID)
				if err != nil {
					return err
				}
				if op.CancelRequested && !slices.Contains(d.ResendOperations, op.ID) {
					d.State = "cancelled"
					return repo.SaveDelivery(ctx, d, d.Revision)
				}
			}
			part.State = "sending"
			if err := repo.SaveDelivery(ctx, d, d.Revision); err != nil {
				return err
			}
			if d.State == "cancelled" {
				return nil
			}
			part = &d.Parts[i]
			outcome, sendErr := transport.Send(ctx, part.Payload, part.PreparedRef)
			part.State = outcome.State
			part.ReceiptRef = outcome.ReceiptRef
			if part.State != "accepted" && part.State != "delivered" && part.State != "failed" {
				part.State = "unknown"
			}
			if part.State == "accepted" && part.ReceiptRef == "" {
				part.State = "unknown"
			}
			if sendErr != nil && part.State != "failed" {
				part.State = "unknown"
			}
			if part.State == "failed" {
				part.FailureStage = "send"
			}
			d.State = deliveryState(d.Parts)
			if err := repo.SaveDelivery(ctx, d, d.Revision); err != nil {
				return err
			}
			if sendErr != nil {
				return sendErr
			}
			if part.State == "failed" || part.State == "unknown" {
				return fmt.Errorf("media delivery did not complete")
			}
		}
	}
	d.State = deliveryState(d.Parts)
	return repo.SaveDelivery(ctx, d, d.Revision)
}

func deliveryState(parts []DeliveryPart) string {
	if len(parts) == 0 {
		return "pending"
	}
	failed, pending, accepted := false, false, false
	for _, part := range parts {
		switch part.State {
		case "unknown":
			return "unknown"
		case "failed":
			failed = true
		case "accepted":
			accepted = true
		case "delivered":
		default:
			pending = true
		}
	}
	if failed {
		return "failed"
	}
	if pending {
		return "pending"
	}
	if accepted {
		return "accepted"
	}
	return "delivered"
}

// resumeDeliveries 只重试已确认失败的阶段，保留已发送和结果未知的 part。
func (s *Service) resumeDeliveries(ctx context.Context, op *Operation) error {
	query, ok := s.repo.(DeliveryResumeRepository)
	if !ok {
		return nil
	}
	repo, ok := s.repo.(DeliveryRepository)
	if !ok {
		return nil
	}
	records, err := query.DeliveriesForOperation(ctx, op.Owner, op.ID)
	if err != nil {
		return err
	}
	for _, delivery := range records {
		if delivery.State == "cancelled" || delivery.State == "delivered" || op.CancelRequested && !slices.Contains(delivery.ResendOperations, op.ID) {
			continue
		}
		changed := false
		for i := range delivery.Parts {
			part := &delivery.Parts[i]
			if part.State != "failed" {
				continue
			}
			part.State = "prepared"
			if part.FailureStage == "prepare" || part.PreparedRef == "" {
				part.State = "pending"
			}
			part.ReceiptRef, part.FailureStage = "", ""
			changed = true
		}
		if changed {
			delivery.State = deliveryState(delivery.Parts)
			if err := repo.SaveDelivery(ctx, delivery, delivery.Revision); err != nil {
				return err
			}
		}
	}
	return nil
}
