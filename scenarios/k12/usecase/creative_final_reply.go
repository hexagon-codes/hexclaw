package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// CreativeFinalReplyObjectID 以成功点评的精确代次和来源区分新投递，旧作品批次不改写。
func CreativeFinalReplyObjectID(view CreativeWorkView, mode string) (string, error) {
	if mode != "inbound_reply" && mode != "send_to_phone" {
		return "", errors.New("creative reply mode is invalid")
	}
	if view.Record == nil || view.GenerationState.Latest == nil {
		return "", errors.New("creative final reply source is missing")
	}
	g := view.GenerationState.Latest
	if g.WorkID != view.Record.RecordID || g.AgentName != view.Record.AgentName || g.Status != k12.WorkFeedbackSucceeded || g.Feedback == nil {
		return "", errors.New("creative final reply generation mismatch")
	}
	raw, err := json.Marshal(g.Source)
	if err != nil {
		return "", err
	}
	return view.Record.RecordID + ":" + g.GenerationID + ":" + strings.TrimPrefix(deliveryDigest(string(raw)), "sha256:") + ":" + mode + ":" + k12.DeliveryProjectionVersionIMFinalPDFV1, nil
}

// PrepareAndSendCreativeFinalReply 共享长内容规则，自动入口仍不重复附上刚收到的原图。
func (d Deps) PrepareAndSendCreativeFinalReply(ctx context.Context, view CreativeWorkView, message DeliveryMessage, targets []ResolvedDeliveryTarget, locale string) (k12.DeliveryBatch, bool, error) {
	mode := "send_to_phone"
	if targets != nil {
		mode = "inbound_reply"
	}
	objectID, err := CreativeFinalReplyObjectID(view, mode)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	ctx = d.WithCreativeWorkDeliveryDisclosure(ctx, view, message.Content)
	frozenLocale, disclosure := CreativeWorkDeliveryDisclosure(ctx, message.Content)
	if locale == "" {
		locale = frozenLocale
	}
	if locale == "" {
		return d.PrepareAndSendMessageBatchForTargets(ctx, view.Record.AgentName, "creative_work", view.Record.RecordID, message, targets)
	}
	title := strings.TrimSpace(view.Fields.DisplayName)
	if view.Fields.WorkTitle != "" && view.Fields.WorkTitle != title {
		title += " · " + view.Fields.WorkTitle
	}
	return d.PrepareAndSendFinalReply(ctx, FinalReplyDeliveryRequest{AgentName: view.Record.AgentName, ObjectKind: "creative_work", ObjectID: objectID, SourceKind: k12.PrintSourceCreativeObservation, SourceRef: objectID, Title: title, Message: message, Producer: messagecontent.ProducerK12, Locale: locale, DingTalkSuffix: disclosure, WritingFeedback: view.Fields.WorkType == k12.WorkTypeWriting}, targets)
}
