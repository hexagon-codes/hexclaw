package instances

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

func (r *instancePhotoReceiver) change(ctx context.Context, k PhotoIntakeKey, lane, from string, next PhotoIntakeState) (bool, error) {
	at := time.Now().UnixMilli()
	won, err := r.store.Transition(ctx, k, lane, from, next, at)
	if won && err == nil {
		slog.Info("IM photo progress", "source_ref", storage.IMRequestIDForEvent(k.Platform, k.InstanceID, k.ProviderMessageID), "operation", lane, "status", next.Status, "at_ms", at)
	}
	return won, err
}

func photoWait(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (r *instancePhotoReceiver) advanceNotice(ctx context.Context, k PhotoIntakeKey) error {
	delay := time.Second
	for {
		row, err := r.store.Get(ctx, k)
		if err != nil {
			return err
		}
		notice := row.Notice
		switch notice.Status {
		case "pending":
			won, err := r.change(ctx, k, "notice", "pending", PhotoIntakeState{Status: "sending"})
			if err != nil {
				return err
			}
			if !won {
				continue
			}
			port, ok := r.transport.(adapter.DeliveryReceiptAdapter)
			if !ok {
				_, err = r.change(ctx, k, "notice", "sending", PhotoIntakeState{Status: "failed", FailureKind: "receipt_unavailable"})
				return err
			}
			ack, sendErr := port.SendWithReceipt(ctx, k.ChatID, &adapter.Reply{Content: row.Route.NoticeText})
			next := photoAckState(ack, sendErr)
			if _, err = r.change(context.WithoutCancel(ctx), k, "notice", "sending", next); err != nil {
				return err
			}
			continue
		case "sending":
			// A prior process crossed the send boundary without a durable response.
			_, err = r.change(ctx, k, "notice", "sending", PhotoIntakeState{Status: "unknown", FailureKind: "send_unconfirmed"})
			return err
		case "accepted", "unknown":
			if notice.ExternalID == "" {
				return nil
			}
			port, ok := r.transport.(adapter.DeliveryReceiptAdapter)
			if !ok {
				return nil
			}
			ack, queryErr := port.QueryReceipt(ctx, notice.ExternalID)
			next := photoAckState(ack, queryErr)
			if queryErr == nil && (next.Status == "delivered" || next.Status == "failed") {
				if _, err = r.change(ctx, k, "notice", notice.Status, next); err != nil {
					return err
				}
				continue
			}
		case "delivered":
			return r.recallFinishedNotice(ctx, row)
		case "recalling":
			_, err = r.change(ctx, k, "notice", "recalling", PhotoIntakeState{Status: "recall_unknown", FailureKind: "recall_unconfirmed"})
			return err
		default:
			return nil
		}
		if err := photoWait(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func photoAckState(ack adapter.DeliveryAck, err error) PhotoIntakeState {
	next := PhotoIntakeState{ExternalID: ack.ExternalMessageID}
	switch ack.Status {
	case adapter.DeliveryDelivered:
		next.Status = "delivered"
	case adapter.DeliveryFailed:
		next.Status = "failed"
		next.FailureKind = "provider_rejected"
	case adapter.DeliveryAccepted:
		if ack.ExternalMessageID != "" && err == nil {
			next.Status = "accepted"
			break
		}
		fallthrough
	default:
		next.Status = "unknown"
		next.FailureKind = "send_unconfirmed"
	}
	return next
}

func (r *instancePhotoReceiver) recallFinishedNotice(ctx context.Context, row PhotoIntakeReceipt) error {
	if row.Notice.Status != "delivered" || row.Notice.ExternalID == "" ||
		(row.Final.Status != "delivered" && row.Final.Status != "failed") {
		return nil
	}
	recaller, ok := r.transport.(interface {
		RecallMessage(context.Context, string) error
	})
	if !ok {
		return nil
	}
	won, err := r.change(ctx, row.Key, "notice", "delivered", PhotoIntakeState{Status: "recalling"})
	if err != nil || !won {
		return err
	}
	recallCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := PhotoIntakeState{Status: "recalled"}
	if err = recaller.RecallMessage(recallCtx, row.Notice.ExternalID); err != nil {
		state = PhotoIntakeState{Status: "recall_unknown", FailureKind: "recall_unconfirmed"}
	}
	_, err = r.change(context.WithoutCancel(ctx), row.Key, "notice", "recalling", state)
	return err
}

type photoReplyReceipts interface {
	SendPhotoReplyWithReceipts(context.Context, string, *adapter.Reply) ([]adapter.DeliveryAck, error)
}

// All IDs belong to the same original target. An unknown part without an ID
// prevents an acknowledged prefix from proving the whole reply was delivered.
type photoFinalParts struct {
	IDs        []string `json:"ids"`
	Incomplete bool     `json:"incomplete,omitempty"`
}

func photoFinalAck(acks []adapter.DeliveryAck, sendErr error) PhotoIntakeState {
	next := PhotoIntakeState{Status: "delivered"}
	parts := photoFinalParts{}
	for _, ack := range acks {
		if ack.ExternalMessageID != "" {
			parts.IDs = append(parts.IDs, ack.ExternalMessageID)
		}
		switch ack.Status {
		case adapter.DeliveryFailed:
			next.Status = "failed"
			next.FailureKind = "provider_rejected"
		case adapter.DeliveryDelivered:
		case adapter.DeliveryAccepted:
			if ack.ExternalMessageID == "" {
				parts.Incomplete = true
			}
			if next.Status == "delivered" {
				next.Status = "accepted"
			}
		default:
			next.Status = "unknown"
			next.FailureKind = "send_unconfirmed"
			if ack.ExternalMessageID == "" {
				parts.Incomplete = true
			}
		}
	}
	if len(acks) == 0 {
		next.Status = "failed"
		next.FailureKind = "delivery_preparation_failed"
	}
	if sendErr != nil {
		parts.Incomplete = true
	}
	if parts.Incomplete && next.Status != "failed" {
		next.Status = "unknown"
		next.FailureKind = "send_unconfirmed"
	}
	if len(parts.IDs) > 0 {
		raw, _ := json.Marshal(parts)
		next.ExternalID = string(raw)
	}
	return next
}

func (r *instancePhotoReceiver) sendFinal(ctx context.Context, k PhotoIntakeKey, reply *adapter.Reply) error {
	won, err := r.change(ctx, k, "final", "pending", PhotoIntakeState{Status: "sending"})
	if err != nil || !won {
		return err
	}
	var next PhotoIntakeState
	if port, ok := r.transport.(photoReplyReceipts); ok {
		acks, sendErr := port.SendPhotoReplyWithReceipts(ctx, k.ChatID, reply)
		next = photoFinalAck(acks, sendErr)
	} else if port, ok := r.transport.(adapter.DeliveryReceiptAdapter); ok && len(reply.Attachments) == 0 {
		ack, sendErr := port.SendWithReceipt(ctx, k.ChatID, reply)
		next = photoFinalAck([]adapter.DeliveryAck{ack}, sendErr)
	} else {
		// Basic Send returns no query handle for its individual visible parts.
		next = PhotoIntakeState{Status: "unknown", FailureKind: "no_delivery_receipt"}
		if r.transport.Send(ctx, k.ChatID, reply) != nil {
			next.FailureKind = "send_unconfirmed"
		}
	}
	_, err = r.change(context.WithoutCancel(ctx), k, "final", "sending", next)
	return err
}

func (r *instancePhotoReceiver) queryFinal(ctx context.Context, row PhotoIntakeReceipt) (bool, error) {
	if row.Final.ExternalID == "" {
		return false, nil
	}
	var parts photoFinalParts
	if err := json.Unmarshal([]byte(row.Final.ExternalID), &parts); err != nil {
		return false, err
	}
	port, ok := r.transport.(adapter.DeliveryReceiptAdapter)
	if !ok {
		return false, errors.New("IM photo delivery query unavailable")
	}
	delivered := len(parts.IDs) > 0
	for _, id := range parts.IDs {
		ack, err := port.QueryReceipt(ctx, id)
		if ack.ExternalMessageID != "" && ack.ExternalMessageID != id {
			return false, ErrPhotoIntakeConflict
		}
		if err == nil && ack.Status == adapter.DeliveryFailed {
			_, err = r.change(ctx, row.Key, "final", row.Final.Status, PhotoIntakeState{Status: "failed", FailureKind: "provider_rejected"})
			return false, err
		}
		delivered = delivered && err == nil && ack.Status == adapter.DeliveryDelivered
	}
	if delivered && !parts.Incomplete {
		_, err := r.change(ctx, row.Key, "final", row.Final.Status, PhotoIntakeState{Status: "delivered"})
		return false, err
	}
	return !delivered, nil
}
