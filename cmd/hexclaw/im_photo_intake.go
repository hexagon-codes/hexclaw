package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/instances"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/records"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// IM 收件负责一条物理消息的提示。K12 仅提供自己的冻结业务归属及接续结果。
type k12PhotoIntakeBridge interface {
	LookupInboundPhotoReceipt(context.Context, *adapter.Message, *adapter.Message) (k12usecase.InboundPhotoReceipt, bool, error)
	PrepareSharedPhoto(context.Context, *adapter.Message, *adapter.Message) (instances.PhotoIntakeRoute, bool, error)
	AdmitSharedPhoto(context.Context, *adapter.Message, instances.PhotoIntakeRoute) error
	SharedPhotoOutcome(context.Context, *adapter.Message, instances.PhotoIntakeRoute) (adapter.DeliveryStatus, error)
}

func dingtalkPhotoRoutes(
	dispatcher *agentrouter.Dispatcher, check func(context.Context, *adapter.Message) error,
	displayLocale func() string, k12Photos k12PhotoIntakeBridge,
) instances.PhotoRoutes {
	var routes instances.PhotoRoutes
	if k12Photos != nil {
		routes.CommitK12 = k12Photos.AdmitSharedPhoto
		routes.K12Outcome = k12Photos.SharedPhotoOutcome
	}
	routes.Prepare = func(ctx context.Context, original, canonical *adapter.Message) (instances.PhotoIntakeRoute, error) {
		if k12Photos != nil {
			_, legacy, err := k12Photos.LookupInboundPhotoReceipt(ctx, original, canonical)
			if err != nil {
				return instances.PhotoIntakeRoute{}, err
			}
			if legacy {
				// 旧任务继续使用原先的接纳、权限检查和已经建立的提示回执。
				return instances.PhotoIntakeRoute{Kind: "legacy"}, nil
			}
		}
		locale := displayLocale()
		canonical.Metadata["locale"] = locale
		canonical.Metadata["user_locale"] = locale
		if err := check(ctx, canonical); err != nil {
			return instances.PhotoIntakeRoute{}, err
		}
		if k12Photos != nil {
			route, matched, err := k12Photos.PrepareSharedPhoto(ctx, original, canonical)
			if err != nil {
				return instances.PhotoIntakeRoute{}, err
			}
			if matched {
				route.Locale, route.NoticeText = locale, receivedPhotoMessage(locale, true)
				return route, nil
			}
		}
		route := instances.PhotoIntakeRoute{
			Kind: "ordinary", AgentName: "default", OwnerScope: canonical.UserID, SessionID: original.SessionID,
			Locale: locale, NoticeText: receivedPhotoMessage(locale, false),
		}
		// 钉钉没有命名规则时使用内置 default。稳定实例规则优先于旧实例名规则。
		if dispatcher != nil {
			for _, source := range []*adapter.Message{canonical, original} {
				found := dispatcher.Route(agentrouter.RouteRequest{
					Platform: string(source.Platform), InstanceID: source.InstanceID, UserID: source.UserID, ChatID: source.ChatID,
				})
				if found != nil && found.Rule != nil && found.AgentConfig != nil {
					route.AgentName, route.BindingID = found.AgentName, stableBindingID(*found.Rule)
					break
				}
			}
		}
		return route, nil
	}
	return routes
}

func receivedPhotoMessage(locale string, tutoring bool) string {
	language := strings.ToLower(strings.TrimSpace(locale))
	switch {
	case strings.HasPrefix(language, "en"):
		return "Image message received. Processing is underway."
	case strings.HasPrefix(language, "ug"):
		return "رەسىم ئۇچۇرى تاپشۇرۇۋېلىندى، بىر تەرەپ قىلىنىۋاتىدۇ."
	}
	const received = "已收到图片消息，正在处理。"
	if tutoring {
		return "📷 " + received + k12PhotoNoticeEstimateZH
	}
	return received
}

const k12PhotoNoticeEstimateZH = "通常预计 2–5 分钟；题量或图片内容较多时约 5–10 分钟。完成后会把处理结果（如有批改图）发回当前对话。"

// 已从受信收件回执进入的普通任务沿原 Agent 处理，不再消费一次网关配额，也不被后来的 K12 规则接管。
func processAdmittedOrdinaryPhoto(ctx context.Context, msg *adapter.Message, route instances.PhotoIntakeRoute,
	dispatcher *agentrouter.Dispatcher, process adapter.MessageHandler,
) (*adapter.Reply, error) {
	if route.Kind != "ordinary" || route.AgentName == "" {
		return nil, errors.New("ordinary photo route is unavailable")
	}
	if !strings.EqualFold(route.AgentName, "default") {
		if dispatcher == nil {
			return nil, errors.New("original photo agent is unavailable")
		}
		cfg, found := dispatcher.GetAgent(route.AgentName)
		if !found || cfg == nil || cfg.Metadata["scenario"] == k12TutorScenario {
			return nil, errors.New("original photo agent is unavailable")
		}
	}
	msg.Metadata["pinned_agent"] = route.AgentName
	msg.Metadata["routed_agent"] = route.AgentName
	msg.Metadata["locale"] = route.Locale
	msg.Metadata["user_locale"] = route.Locale
	return process(ctx, msg)
}

// 普通对话可能已把文件或长回复交给原有投递器。恢复只读取其持久事实。
type photoReplyObserver struct {
	reports *k12usecase.Deps
	media   interface {
		Delivery(context.Context, string, string) (*mediaflow.Delivery, error)
	}
}

func (o photoReplyObserver) outcome(ctx context.Context, msg *adapter.Message, route instances.PhotoIntakeRoute, ref string) (adapter.DeliveryStatus, error) {
	identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx)
	if !ok || identity.Platform != string(msg.Platform) || msg.ChatID != msg.UserID || route.RequestID == "" {
		return "", errors.New("IM photo reply source is unavailable")
	}
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" {
		return "", errors.New("invalid IM photo reply reference")
	}
	switch kind {
	case "media":
		if o.media == nil {
			return "", errors.New("media delivery reader is unavailable")
		}
		d, err := o.media.Delivery(ctx, msg.UserID, id)
		if err != nil {
			return "", err
		}
		if d == nil || d.ID != id || d.Owner != msg.UserID || d.RequestID != route.RequestID ||
			d.Platform != identity.Platform || d.ChatID != msg.ChatID || (d.InstanceID != identity.ID && d.InstanceID != msg.InstanceID) ||
			(msg.SessionID != "" && d.SessionID != msg.SessionID) {
			return "", errors.New("media delivery source differs from the original photo")
		}
		if d.State == "cancelled" {
			return adapter.DeliveryFailed, nil
		}
		states := make([]adapter.DeliveryStatus, 0, len(d.Parts))
		for _, p := range d.Parts {
			states = append(states, adapter.DeliveryStatus(p.State))
		}
		return finalPhotoPartStates(states), nil
	case "print":
		if o.reports == nil {
			return "", errors.New("document delivery reader is unavailable")
		}
		s, err := o.reports.Records.GetConversationPrintReply(ctx, route.AgentName, id)
		if err != nil {
			return "", err
		}
		if s.Owner != msg.UserID || s.AssistantMessageID != id || s.RequestID != route.RequestID ||
			s.Platform != identity.Platform || s.ChatID != msg.ChatID || (s.InstanceID != identity.ID && s.InstanceID != msg.InstanceID) ||
			(msg.SessionID != "" && s.SessionID != msg.SessionID) {
			return "", errors.New("document delivery source differs from the original photo")
		}
		view, err := o.reports.GetConversationPrintReply(ctx, s)
		if err != nil {
			return "", err
		}
		if view.State == "cancelled" {
			return adapter.DeliveryFailed, nil
		}
		if view.Batch == nil {
			return "", nil
		}
		return finalPhotoBatchState(*view.Batch), nil
	case "batch":
		if o.reports == nil {
			return "", errors.New("final reply batch reader is unavailable")
		}
		batch, err := o.reports.GetDeliveryBatch(ctx, route.AgentName, id)
		if err != nil {
			return "", err
		}
		if batch.ObjectKind != "im_final_reply" || len(batch.Receipts) == 0 {
			return "", errors.New("unexpected photo final reply batch")
		}
		var payload struct {
			Source *k12.DeliveryProjectionSource `json:"delivery_source"`
		}
		if err := json.Unmarshal([]byte(batch.Receipts[0].PayloadJSON), &payload); err != nil || payload.Source == nil {
			return "", errors.New("photo final reply is missing its source proof")
		}
		proof := photoPrivateReplySource(msg, route, identity.ID, payload.Source.SourceRef)
		expected, err := k12.PrivateReplyObjectID(proof)
		if err != nil || expected != batch.ObjectID {
			return "", errors.New("final reply batch does not belong to the original photo")
		}
		if err := k12.ValidateDeliveryProjectionTarget(batch, batch.Receipts); err != nil {
			return "", err
		}
		return finalPhotoBatchState(batch), nil
	default:
		return "", fmt.Errorf("unrecognized IM photo delivery reference kind %q", kind)
	}
}

func photoPrivateReplySource(msg *adapter.Message, route instances.PhotoIntakeRoute, instanceID, assistantID string) k12.AcceptedPrivateReplySource {
	return k12.AcceptedPrivateReplySource{AgentName: route.AgentName, Platform: string(msg.Platform), InstanceID: instanceID,
		SenderID: msg.UserID, MessageID: msg.ID, AssistantMessageID: assistantID}
}

func finalPhotoBatchState(batch k12.DeliveryBatch) adapter.DeliveryStatus {
	states := make([]adapter.DeliveryStatus, 0, len(batch.Receipts))
	for _, receipt := range batch.Receipts {
		if receipt.Status == k12.DeliveryDelivered && receipt.ExternalMessageID == "" {
			return ""
		}
		states = append(states, adapter.DeliveryStatus(receipt.Status))
	}
	return finalPhotoPartStates(states)
}

func finalPhotoPartStates(states []adapter.DeliveryStatus) adapter.DeliveryStatus {
	if len(states) == 0 {
		return ""
	}
	result := adapter.DeliveryDelivered
	for _, state := range states {
		switch state {
		case adapter.DeliveryDelivered:
		case adapter.DeliveryFailed:
			result = adapter.DeliveryFailed
		default:
			return ""
		}
	}
	return result
}

func (o photoReplyObserver) existing(ctx context.Context, msg *adapter.Message, route instances.PhotoIntakeRoute, reply *adapter.Reply) (string, error) {
	if id := reply.Metadata["media_delivery_id"]; id != "" {
		ref := "media:" + id
		_, err := o.outcome(ctx, msg, route, ref)
		return ref, err
	}
	if o.reports == nil || reply.AssistantMessageID == "" {
		return "", nil
	}
	if reply.Metadata["print_artifacts"] != "" {
		ref := "print:" + reply.AssistantMessageID
		if _, err := o.outcome(ctx, msg, route, ref); err == nil {
			return ref, nil
		} else if !errors.Is(err, records.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx)
	if !ok || identity.Platform != string(msg.Platform) {
		return "", errors.New("photo reply source instance is missing")
	}
	object, err := k12.PrivateReplyObjectID(photoPrivateReplySource(msg, route, identity.ID, reply.AssistantMessageID))
	if err != nil {
		return "", err
	}
	batch, err := o.reports.Records.GetLatestDeliveryBatchForObject(ctx, route.AgentName, "im_final_reply", object)
	if errors.Is(err, records.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	ref := "batch:" + batch.BatchID
	_, err = o.outcome(ctx, msg, route, ref)
	return ref, err
}
