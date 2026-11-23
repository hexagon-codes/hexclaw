package dingtalk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alibabacloud-go/tea/tea"

	"github.com/hexagon-codes/hexclaw/adapter"
)

func (event dtEvent) photoCodes() []string {
	if len(event.PictureDownloadCodes) != 0 {
		return event.PictureDownloadCodes
	}
	return []string{event.Content.DownloadCode}
}

// photoFrameDigest 按原消息正文、引用和有序媒体凭据取摘要，各种回调走同一投影。
// 平台可更新下载码，指纹不同只表示需要实际比对图片字节。
func photoFrameDigest(msg *adapter.Message, codes []string) string {
	fields := []string{
		string(msg.Platform), msg.InstanceID, msg.ID, msg.ChatID, msg.UserID,
		msg.Metadata["conversation_id"], msg.Metadata["conversation_type"], msg.Content,
		msg.ReplyTo, msg.Metadata["quoted_homework_id"], msg.Metadata["quoted_homework_ambiguous"],
	}
	encoded, _ := json.Marshal(append(fields, codes...)) // 字符串序列以 JSON 明确分界。
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (a *DingtalkAdapter) inboundPhotoLogger(event dtEvent) *slog.Logger {
	return slog.With(
		"source_instance_ref", dingtalkIdentityHash(a.Name()),
		"chat_ref", dingtalkIdentityHash(event.SenderStaffId),
		"provider_message_ref", dingtalkIdentityHash(event.MsgID),
	)
}

// admitInboundPhotoBeforeACK 在下载前交接共享收件；已由原通道接管的旧消息沿历史合同恢复。
func (a *DingtalkAdapter) admitInboundPhotoBeforeACK(ctx context.Context, event *dtEvent) (bool, error) {
	if event == nil || event.MsgType != dtMsgTypePicture || strings.TrimSpace(event.Content.DownloadCode) == "" {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	codes := event.photoCodes()
	log := a.inboundPhotoLogger(*event)
	created := event.photoCreatedMillis(event.receivedAt)
	arrivalLog := log
	if created != 0 {
		arrivalLog = log.With("platform_created_at_ms", created)
	}
	arrivalLog.Info("DingTalk inbound photo received", "received_at_ms", event.receivedAt.UnixMilli(), "pages", len(codes))
	intake, historical := a.photoAdmissionPorts()
	if intake != nil {
		msg := a.messageFromEvent(*event, nil)
		msg.Timestamp = event.receivedAt
		arrival := adapter.PhotoArrival{
			Message: msg, PageCount: len(codes), ReceivedAt: event.receivedAt,
			FrameDigest: photoFrameDigest(msg, codes),
		}
		if created != 0 {
			arrival.ProviderCreatedAt = time.UnixMilli(created)
		}
		handled, err := a.receivePhotoPages(ctx, intake, arrival, codes, log)
		if handled || err != nil {
			return handled, err
		}
	}
	if historical == nil {
		return false, nil
	}
	attachments, err := a.downloadLegacyPhotoPages(ctx, *event)
	if err != nil {
		return false, fmt.Errorf("DingTalk inbound photo download before durable admission: %w", err)
	}
	event.Attachments = attachments
	handled, err := historical.AdmitInboundPhoto(ctx, a.messageFromEvent(*event, attachments))
	if err != nil {
		return false, fmt.Errorf("DingTalk inbound photo durable admission failed: %w", err)
	}
	return handled, nil
}

func (a *DingtalkAdapter) receivePhotoPages(
	ctx context.Context, intake adapter.PhotoIntake, arrival adapter.PhotoArrival, codes []string, log *slog.Logger,
) (bool, error) {
	callback, cancel := context.WithCancel(ctx)
	defer cancel()
	return intake.ReceivePhotos(callback, arrival, func(pageCtx context.Context, index int) (adapter.Attachment, error) {
		if err := callback.Err(); err != nil {
			return adapter.Attachment{}, err
		}
		if index < 0 || index >= len(codes) {
			return adapter.Attachment{}, errors.New("DingTalk inbound photo page index is out of range")
		}
		if err := pageCtx.Err(); err != nil {
			return adapter.Attachment{}, err
		}
		loadCtx, cancelLoad := context.WithCancel(pageCtx)
		stop := context.AfterFunc(callback, cancelLoad)
		defer func() {
			stop()
			cancelLoad()
		}()
		return a.readInboundPhotoPage(loadCtx, log, index, codes[index])
	})
}

func (a *DingtalkAdapter) downloadLegacyPhotoPages(ctx context.Context, event dtEvent) ([]adapter.Attachment, error) {
	codes := event.photoCodes()
	log := a.inboundPhotoLogger(event)
	attachments := make([]adapter.Attachment, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for index, code := range codes {
		// 历史接纳按不同下载码分配页号，已有消息仍须复用原来的页身份。
		if seen[code] {
			continue
		}
		seen[code] = true
		attachment, err := a.readInboundPhotoPage(ctx, log, index, code)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func (a *DingtalkAdapter) readInboundPhotoPage(ctx context.Context, log *slog.Logger, index int, code string) (adapter.Attachment, error) {
	started := time.Now()
	log.Info("DingTalk inbound photo download started", "page_index", index, "download_started_at_ms", started.UnixMilli())
	attachment, err := a.downloadPictureAttachment(ctx, code)
	finished := time.Now()
	if err != nil {
		// 下载过程只返回已脱敏的阶段、HTTP 状态或本地字节校验错误。
		log = log.With("download_error", err.Error())
	}
	log.Info("DingTalk inbound photo download finished", "page_index", index,
		"download_finished_at_ms", finished.UnixMilli(), "duration_ms", finished.Sub(started).Milliseconds(),
		"success", err == nil, "bytes", attachment.Size)
	return attachment, err
}

// photoTransportError 保留取消、截止和可信状态码。SDK 错误和 HTTP 错误可能含临时下载地址。
func photoTransportError(stage string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", stage, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", stage, context.DeadlineExceeded)
	}
	var sdkErr *tea.SDKError
	if errors.As(err, &sdkErr) {
		if sdkErr.Code != nil && *sdkErr.Code == "invalidParameter.robotCode.downloadCode" {
			return fmt.Errorf("%s: invalidParameter.robotCode.downloadCode", stage)
		}
		if sdkErr.StatusCode != nil {
			return fmt.Errorf("%s: HTTP %d", stage, *sdkErr.StatusCode)
		}
	}
	return errors.New(stage)
}
