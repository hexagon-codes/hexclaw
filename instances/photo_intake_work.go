package instances

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

// Text can be sent independently after restart. Files remain owned by the
// original conversation/document store; reopen them there with the frozen identity.
type ordinaryPhotoOutput struct {
	MessageID   string                         `json:"message_id,omitempty"`
	Text        string                         `json:"text,omitempty"`
	Original    bool                           `json:"original,omitempty"`
	Delegated   bool                           `json:"delegated,omitempty"`
	Reference   string                         `json:"reference,omitempty"`
	DocumentIDs []string                       `json:"document_ids,omitempty"`
	Content     *messagecontent.MessageContent `json:"content,omitempty"`
	Manifest    *messagecontent.RenderManifest `json:"manifest,omitempty"`
	Interactive *adapter.InteractivePayload    `json:"interactive,omitempty"`
}

func freezeOrdinaryPhoto(reply *adapter.Reply) string {
	out := ordinaryPhotoOutput{Delegated: reply == nil}
	if reply != nil {
		out.MessageID = reply.AssistantMessageID
		out.Original = len(reply.Attachments) > 0
		if !out.Original {
			out.Text, out.Content, out.Manifest, out.Interactive = reply.Content, reply.MessageContent, reply.RenderManifest, reply.Interactive
		}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// The original partial-document path has already verified ownership. Freeze only
// the outgoing IDs so a restart cannot replace these files with an entire reply.
func (r *instancePhotoReceiver) sendPartialPhotoDocuments(ctx context.Context, key PhotoIntakeKey, target, chat string, reply *adapter.Reply) error {
	if target != key.InstanceID || chat != key.ChatID || reply == nil || reply.AssistantMessageID == "" || len(reply.Attachments) == 0 {
		return ErrPhotoIntakeConflict
	}
	out := ordinaryPhotoOutput{MessageID: reply.AssistantMessageID, Original: true}
	for _, a := range reply.Attachments {
		if a.ID == "" {
			return ErrPhotoIntakeConflict
		}
		out.DocumentIDs = append(out.DocumentIDs, a.ID)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return err
	}
	won, err := r.change(ctx, key, "work", "running", PhotoIntakeState{Status: "completed", ResultRef: string(encoded)})
	if err != nil || !won {
		return err
	}
	return r.sendFinal(ctx, key, reply)
}

func partialPhotoDocuments(original *adapter.Reply, ids []string) (*adapter.Reply, error) {
	if len(ids) == 0 {
		return original, nil
	}
	out := &adapter.Reply{AssistantMessageID: original.AssistantMessageID, BackendMessageID: original.BackendMessageID, MessageID: original.MessageID}
	for _, id := range ids {
		found := false
		for _, a := range original.Attachments {
			if a.ID == id {
				out.Attachments = append(out.Attachments, a)
				found = true
				break
			}
		}
		if !found {
			return nil, ErrPhotoIntakeConflict
		}
	}
	return out, nil
}

func (r *instancePhotoReceiver) advanceWork(ctx context.Context, k PhotoIntakeKey) error {
	var direct *adapter.Reply
	delay := time.Second
	for {
		row, err := r.store.Get(ctx, k)
		if err != nil || row.AssetsReadyAtMS == 0 {
			return err
		}
		switch row.Work.Status {
		case "pending":
			won, err := r.change(ctx, k, "work", "pending", PhotoIntakeState{Status: "running"})
			if err != nil {
				return err
			}
			if !won {
				continue
			}
			row.Work.Status = "running"
			if row.Route.Kind == "ordinary" {
				direct, err = r.processOrdinary(ctx, row)
				if err != nil {
					return err
				}
				continue
			}
			fallthrough
		case "running", "unknown":
			switch row.Route.Kind {
			case "k12":
				if r.routes.CommitK12 == nil {
					return errors.New("K12 photo handoff unavailable")
				}
				msg, err := r.message(ctx, row, true)
				if err != nil {
					return err
				}
				// K12's grouped admission is idempotent against the original identity and pixels.
				if err = r.routes.CommitK12(ctx, msg, row.Route); err != nil {
					break
				}
				if _, err = r.change(ctx, k, "work", row.Work.Status, PhotoIntakeState{Status: "completed", ResultRef: row.ID}); err != nil {
					return err
				}
				continue
			case "ordinary":
				if r.routes.RecoverOrdinary != nil {
					msg, err := r.message(ctx, row, true)
					if err != nil {
						return err
					}
					var delegated string
					direct, delegated, err = r.routes.RecoverOrdinary(ctx, msg, row.Route)
					if err != nil {
						return err
					}
					if direct != nil || delegated != "" {
						result := freezeOrdinaryPhoto(direct)
						if delegated != "" {
							b, _ := json.Marshal(ordinaryPhotoOutput{Delegated: true, Reference: delegated})
							result = string(b)
							direct = nil
						}
						if _, err = r.change(ctx, k, "work", row.Work.Status, PhotoIntakeState{Status: "completed", ResultRef: result}); err != nil {
							return err
						}
						continue
					}
				}
				if row.Work.Status == "running" {
					_, err = r.change(ctx, k, "work", "running", PhotoIntakeState{Status: "unknown", FailureKind: "request_unconfirmed"})
				}
				return err
			default:
				return errors.New("unknown IM photo business route")
			}
		case "completed":
			repeat, err := r.finishPhoto(ctx, row, direct)
			direct = nil
			if err != nil || !repeat {
				return err
			}
		default:
			return nil
		}
		if err := photoWait(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, 20*time.Second)
	}
}

func (r *instancePhotoReceiver) processOrdinary(ctx context.Context, row PhotoIntakeReceipt) (*adapter.Reply, error) {
	msg, err := r.message(ctx, row, true)
	if err != nil {
		return nil, err
	}
	delegation := &photoDelegation{receiver: r, key: row.Key}
	auth := admittedPhoto{message: msg, route: row.Route, source: photoMessageDigest(msg), instanceID: row.Key.InstanceID, initialSession: msg.SessionID, delegation: delegation}
	ctx = context.WithValue(ctx, admittedPhotoKey{}, auth)
	var rawReply *adapter.Reply
	wrapped := r.manager.wrapHandler(&r.inst, func(ctx context.Context, in *adapter.Message) (*adapter.Reply, error) {
		reply, err := r.handler(ctx, in)
		rawReply = reply
		return reply, err
	})
	reply, handleErr := wrapped(ctx, msg)
	resCtx := context.WithoutCancel(ctx)
	delegation.mu.Lock()
	ref := delegation.ref
	delegation.mu.Unlock()
	if ref != "" {
		encoded, _ := json.Marshal(ordinaryPhotoOutput{Delegated: true, Reference: ref})
		_, err = r.change(resCtx, row.Key, "work", "running", PhotoIntakeState{Status: "completed", ResultRef: string(encoded)})
		return nil, err
	}
	if handleErr != nil {
		if ctx.Err() != nil || errors.Is(handleErr, context.Canceled) || errors.Is(handleErr, context.DeadlineExceeded) {
			_, err = r.change(resCtx, row.Key, "work", "running", PhotoIntakeState{Status: "unknown", FailureKind: "request_unconfirmed"})
			return nil, err
		}
		if rawReply.DeliveryAttempted(msg) {
			// Its original document sender already crossed the external boundary.
			reply = nil
		} else {
			reply = &adapter.Reply{Content: "处理消息时出现错误，请稍后重试。"}
		}
	}
	if reply==nil {
		// 没有回传正文也没有可核对的交付引用时，留存原请求给只读恢复。
		_,err=r.change(resCtx,row.Key,"work","running",PhotoIntakeState{Status:"unknown",FailureKind:"reply_unconfirmed"})
		return nil,err
	}
	next := PhotoIntakeState{Status: "completed", ResultRef: freezeOrdinaryPhoto(reply)}
	if handleErr != nil {
		next.FailureKind = "original_handler_failed"
	}
	_, err = r.change(resCtx, row.Key, "work", "running", next)
	return reply, err
}

func (r *instancePhotoReceiver) finishPhoto(ctx context.Context, row PhotoIntakeReceipt, direct *adapter.Reply) (bool, error) {
	k := row.Key
	switch row.Final.Status {
	case "pending":
		if row.Route.Kind == "k12" {
			_, err := r.change(ctx, k, "final", "pending", PhotoIntakeState{Status: "delegated", ResultRef: row.Work.ResultRef})
			return true, err
		}
		var frozen ordinaryPhotoOutput
		if err := json.Unmarshal([]byte(row.Work.ResultRef), &frozen); err != nil {
			return false, err
		}
		if frozen.Delegated {
			_, err := r.change(ctx, k, "final", "pending", PhotoIntakeState{Status: "delegated", ResultRef: frozen.Reference})
			return true, err
		}
		if direct == nil {
			if r.routes.RecoverOrdinary != nil {
				msg, err := r.message(ctx, row, false)
				if err != nil {
					return false, err
				}
				stored, ref, err := r.routes.RecoverOrdinary(ctx, msg, row.Route)
				if err != nil {
					return false, err
				}
				if ref != "" {
					_, err = r.change(ctx, k, "final", "pending", PhotoIntakeState{Status: "delegated", ResultRef: ref})
					return true, err
				}
				if frozen.Original {
					direct = stored
				}
			}
			if frozen.Original {
				if direct == nil {
					return false, nil
				}
				if frozen.MessageID != "" && frozen.MessageID != direct.AssistantMessageID {
					return false, ErrPhotoIntakeConflict
				}
				var err error
				direct, err = partialPhotoDocuments(direct, frozen.DocumentIDs)
				if err != nil {
					return false, err
				}
			} else {
				direct = &adapter.Reply{AssistantMessageID: frozen.MessageID, Content: frozen.Text, MessageContent: frozen.Content, RenderManifest: frozen.Manifest, Interactive: frozen.Interactive}
			}
		}
		return true, r.sendFinal(ctx, k, direct)
	case "sending":
		_, err := r.change(ctx, k, "final", "sending", PhotoIntakeState{Status: "unknown", FailureKind: "send_unconfirmed"})
		return false, err
	case "accepted", "unknown":
		if row.Final.ExternalID == "" {
			return false, nil
		}
		wait, err := r.queryFinal(ctx, row)
		if wait || err != nil {
			return wait, err
		}
		latest, err := r.store.Get(ctx, k)
		if err != nil {
			return false, err
		}
		return false, r.recallFinishedNotice(ctx, latest)
	case "delegated":
		msg, err := r.message(ctx, row, false)
		if err != nil {
			return false, err
		}
		var outcome adapter.DeliveryStatus
		switch {
		case row.Route.Kind == "k12" && r.routes.K12Outcome != nil:
			outcome, err = r.routes.K12Outcome(ctx, msg, row.Route)
		case row.Route.Kind == "ordinary" && row.Final.ResultRef != "" && r.routes.OrdinaryOutcome != nil:
			outcome, err = r.routes.OrdinaryOutcome(ctx, msg, row.Route, row.Final.ResultRef)
		default:
			return false, nil
		}
		if err != nil || (outcome != adapter.DeliveryDelivered && outcome != adapter.DeliveryFailed) {
			return true, nil
		}
		_, err = r.change(ctx, k, "final", "delegated", PhotoIntakeState{Status: string(outcome)})
		if err != nil {
			return false, err
		}
		latest, err := r.store.Get(ctx, k)
		if err != nil {
			return false, err
		}
		return false, r.recallFinishedNotice(ctx, latest)
	default:
		return false, r.recallFinishedNotice(ctx, row)
	}
}
