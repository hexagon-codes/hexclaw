// Package mediabridge 把媒体投递状态接到应用已有渠道准备与回执接口。
package mediabridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/adapter/dingtalk"
	"github.com/hexagon-codes/hexclaw/instances"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/storage"
)

type Transport struct {
	manager  *instances.Manager
	flow     *mediaflow.Service
	delivery *mediaflow.Delivery
}

func Resolve(ctx context.Context, store storage.Store, manager *instances.Manager, flow *mediaflow.Service, d *mediaflow.Delivery) (mediaflow.DeliveryTransport, []json.RawMessage, error) {
	if d.Platform != "dingtalk" {
		return nil, nil, fmt.Errorf("media delivery is not supported by this channel")
	}
	transport := &Transport{manager: manager, flow: flow, delivery: d}
	if len(d.Parts) > 0 {
		return transport, nil, nil
	}
	message, err := store.GetMessage(ctx, d.MessageID)
	if err != nil {
		return nil, nil, err
	}
	if message.SessionID != d.SessionID {
		return nil, nil, fmt.Errorf("media delivery source message mismatch")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message.Metadata), &meta); err != nil {
		return nil, nil, err
	}
	reply := &adapter.Reply{Content: message.Content, Metadata: make(map[string]string)}
	for _, key := range []string{"model", "provider", "locale", "producer_kind", "request_runtime", "teaching_context_receipts"} {
		var value string
		if json.Unmarshal(meta[key], &value) == nil {
			reply.Metadata[key] = value
		}
	}
	_ = json.Unmarshal(meta["tool_calls"], &reply.ToolCalls)
	assets, err := flow.ResolveAssets(ctx, d.Owner, d.Assets)
	if err != nil {
		return nil, nil, err
	}
	for _, asset := range assets {
		reply.Attachments = append(reply.Attachments, transport.attachment(asset))
	}
	parts, err := dingtalk.ProjectDeliveryParts(reply)
	if err != nil {
		return nil, nil, err
	}
	var plan []json.RawMessage
	for _, part := range parts {
		encoded, err := json.Marshal(part)
		if err != nil {
			return nil, nil, err
		}
		plan = append(plan, encoded)
	}
	return transport, plan, nil
}

func (t *Transport) attachment(asset mediaflow.Asset) adapter.Attachment {
	return adapter.Attachment{ID: asset.ID, Type: asset.Kind, Name: path.Base(asset.Path), Mime: asset.MIME, ContentDigest: "sha256:" + asset.Digest, Size: asset.Size, Source: &adapter.AttachmentSource{OpenReader: func(ctx context.Context) (io.ReadCloser, error) {
		return t.flow.OpenAsset(ctx, t.delivery.Owner, asset)
	}}}
}

func (t *Transport) hydrate(ctx context.Context, payload json.RawMessage) (adapter.DeliveryPart, error) {
	var part adapter.DeliveryPart
	if err := json.Unmarshal(payload, &part); err != nil {
		return part, err
	}
	if part.Attachment != nil {
		id := part.Attachment.ID
		found := false
		for _, asset := range t.delivery.Assets {
			if asset.ID == id {
				resolved, err := t.flow.ResolveAssets(ctx, t.delivery.Owner, []mediaflow.Asset{asset})
				if err != nil {
					return part, err
				}
				if len(resolved) != 1 {
					return part, mediaflow.ErrNotFound
				}
				attachment := t.attachment(resolved[0])
				part.Attachment = &attachment
				found = true
				break
			}
		}
		if !found {
			return part, mediaflow.ErrNotFound
		}
	}
	return part, nil
}
func (t *Transport) Prepare(ctx context.Context, payload json.RawMessage) (string, error) {
	part, err := t.hydrate(ctx, payload)
	if err != nil {
		return "", &adapter.ResourcePreparationError{Known: true, Cause: err}
	}
	return t.manager.PrepareDeliveryPartResource(ctx, t.delivery.InstanceID, part)
}
func (t *Transport) Send(ctx context.Context, payload json.RawMessage, prepared string) (mediaflow.DeliveryOutcome, error) {
	part, err := t.hydrate(ctx, payload)
	if err != nil {
		return mediaflow.DeliveryOutcome{State: "failed"}, err
	}
	part.PreparedResourceID = prepared
	ack, err := t.manager.SendPreparedPartWithReceipt(ctx, t.delivery.InstanceID, t.delivery.ChatID, part)
	return outcome(ack), err
}
func (t *Transport) Query(ctx context.Context, ref string) (mediaflow.DeliveryOutcome, error) {
	ack, err := t.manager.QueryReceipt(ctx, t.delivery.InstanceID, ref)
	return outcome(ack), err
}
func outcome(ack adapter.DeliveryAck) mediaflow.DeliveryOutcome {
	state := "unknown"
	switch ack.Status {
	case adapter.DeliveryAccepted:
		state = "accepted"
	case adapter.DeliveryDelivered:
		state = "delivered"
	case adapter.DeliveryFailed:
		state = "failed"
	}
	return mediaflow.DeliveryOutcome{State: state, ReceiptRef: ack.ExternalMessageID}
}
