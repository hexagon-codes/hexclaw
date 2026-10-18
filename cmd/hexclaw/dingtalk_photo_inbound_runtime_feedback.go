package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/records"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const k12DingtalkPhotoStatusNoticeKind = "dingtalk_photo_status_notice"
const k12DingtalkPhotoStatusNoticeVersion = "v1"

var errK12DingtalkPhotoWaiting = errors.New("DingTalk photo task is waiting for explicit recovery")

type k12DingtalkPhotoStatusBatchPort interface {
	GetDeliveryBatchForObject(context.Context, string, string, string) (k12.DeliveryBatch, error)
	ResumePendingDeliveryBatch(context.Context, string, string) (k12.DeliveryBatch, error)
}

func k12DingtalkPhotoStatusText(locale, state string) string {
	language := strings.ToLower(strings.TrimSpace(locale))
	if strings.HasPrefix(language, "en") {
		switch state {
		case "accepted":
			return "Image received. Recognition and processing are underway."
		case "failed":
			return "This image could not be processed. A complete result has not been generated."
		case "outcome_unknown":
			return "The processing result is not yet confirmed. The task has been retained; please do not send the image again."
		}
	}
	if strings.HasPrefix(language, "ug") {
		switch state {
		case "accepted":
			return "رەسىم تاپشۇرۇۋېلىندى، تونۇش ۋە بىر تەرەپ قىلىش داۋاملىشىۋاتىدۇ."
		case "failed":
			return "بۇ رەسىمنى بىر تەرەپ قىلىش مەغلۇپ بولدى، تېخى تولۇق نەتىجە ھاسىل قىلىنمىدى."
		case "outcome_unknown":
			return "بۇ قېتىملىق رەسىمنى بىر تەرەپ قىلىش نەتىجىسى تېخى جەزملەشتۈرۈلمىدى. ۋەزىپە ساقلاپ قېلىندى، رەسىمنى قايتا ئەۋەتمەڭ."
		}
	}
	switch state {
	case "accepted":
		return "已收到图片，正在识别和处理。"
	case "failed":
		return "这张图片处理失败，暂未生成完整结果。"
	case "outcome_unknown":
		return "本次图片处理结果暂未确认，任务已保留，请勿重复发送。"
	default:
		return ""
	}
}

// 状态通知单独冻结，不占用终稿批次；重启优先读取原载荷，不能由新语言或错误正文另发一份。
func (r *k12DingtalkPhotoInboundRuntime) sendStatusNotice(ctx context.Context, bundle k12usecase.InboundPhotoBundle, state string) error {
	var command k12DingtalkInboundPhotoCommand
	if err := json.Unmarshal([]byte(bundle.Receipt.CommandJSON), &command); err != nil {
		return fmt.Errorf("decode DingTalk photo status command: %w", err)
	}
	// 通知合同随首次接纳冻结；旧任务恢复不补发迟到通知，也不改变其业务推进。
	if command.StatusNoticeVersion == "" {
		return nil
	}
	if command.StatusNoticeVersion != k12DingtalkPhotoStatusNoticeVersion {
		return fmt.Errorf("DingTalk photo status notice version is unsupported")
	}
	port, ok := r.replyBatches.(k12DingtalkPhotoStatusBatchPort)
	if !ok {
		return fmt.Errorf("DingTalk photo status delivery is unavailable")
	}
	objectID := bundle.Receipt.ReceiptID + ":" + state
	if bundle.Receipt.ReceiptID == "" || k12DingtalkPhotoStatusText("", state) == "" {
		return fmt.Errorf("DingTalk photo status identity is invalid")
	}
	batch, err := port.GetDeliveryBatchForObject(ctx, bundle.Receipt.AgentName, k12DingtalkPhotoStatusNoticeKind, objectID)
	if err == nil {
		if batch.AgentName != bundle.Receipt.AgentName || batch.ObjectKind != k12DingtalkPhotoStatusNoticeKind || batch.ObjectID != objectID || len(batch.Receipts) != 1 || batch.Receipts[0].PartKind != messagecontent.PartMarkdown {
			return fmt.Errorf("DingTalk photo status batch identity is invalid")
		}
		// 目标与正文以首次批次为准；恢复不重新解析已变化的绑定或语言。
		switch batch.Receipts[0].Status {
		case k12.DeliveryPending:
			_, err = port.ResumePendingDeliveryBatch(ctx, bundle.Receipt.AgentName, batch.BatchID)
		case k12.DeliverySending, k12.DeliveryOutcomeUnknown:
			_, err = r.replyBatches.QueryDeliveryBatch(ctx, bundle.Receipt.AgentName, batch.BatchID)
		}
		if errors.Is(err, k12usecase.ErrDeliveryQueryUnavailable) {
			// 无查询句柄的未知回执保持原样，不能反复报错或重发状态消息。
			return nil
		}
		return err
	}
	if !errors.Is(err, records.ErrNotFound) {
		return err
	}
	target, err := r.resolveInboundPhotoFrozenTarget(bundle)
	if err != nil {
		return err
	}
	_, _, err = r.replyBatches.PrepareAndSendMessageBatchForTargets(ctx, bundle.Receipt.AgentName,
		k12DingtalkPhotoStatusNoticeKind, objectID,
		k12usecase.DeliveryMessage{Content: k12DingtalkPhotoStatusText(command.MessageLocale, state)},
		[]k12usecase.ResolvedDeliveryTarget{target})
	return err
}

func (r *k12DingtalkPhotoInboundRuntime) waitWithStatusNotice(ctx context.Context, bundle k12usecase.InboundPhotoBundle, state string) (bool, error) {
	if state == "outcome_unknown" {
		var command k12DingtalkInboundPhotoCommand
		if err := json.Unmarshal([]byte(bundle.Receipt.CommandJSON), &command); err != nil {
			return false, fmt.Errorf("decode DingTalk photo status command: %w", err)
		}
		// 旧未知调用不重新纳入渠道观察；已知失败仍可沿原任务显式恢复并投递。
		if command.StatusNoticeVersion == "" {
			return true, nil
		}
	}
	if err := r.sendStatusNotice(ctx, bundle, state); err != nil {
		return false, err
	}
	return false, errK12DingtalkPhotoWaiting
}
