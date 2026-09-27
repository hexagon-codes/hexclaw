package dingtalk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/toolkit/util/logger"
)

// logCallbackFrameReceived 在解析和引用筛选前记录回调边界，不保留载荷内容。
func (a *DingtalkAdapter) logCallbackFrameReceived(raw []byte, frameMessageID string) {
	digest := sha256.Sum256(raw)
	logger.Info("[dingtalk] callback frame received",
		"instance_sha256", dingtalkIdentityHash(a.Name()),
		"frame_message_id_sha256", dingtalkIdentityHash(frameMessageID),
		"payload_bytes", len(raw),
		"payload_sha256", hex.EncodeToString(digest[:]),
	)
}

// logCallbackFrameParsed 仅记录固定引用容器的存在性，不推导引用身份。
func (a *DingtalkAdapter) logCallbackFrameParsed(raw []byte, event dtEvent) {
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(raw, &envelope)
	var text, content map[string]json.RawMessage
	_ = json.Unmarshal(envelope["text"], &text)
	_ = json.Unmarshal(envelope["content"], &content)
	_, textReplyPresent := text["repliedMsg"]
	_, contentReplyPresent := content["repliedMsg"]
	logger.Info("[dingtalk] callback frame parsed",
		"msgtype", event.MsgType,
		"text_reply_container_present", textReplyPresent,
		"content_reply_container_present", contentReplyPresent,
	)
}

// logReplyIdentityEvidence 仅保留平台引用身份的摘要，不推导不同字段间的映射。
func (a *DingtalkAdapter) logReplyIdentityEvidence(raw []byte, event dtEvent) {
	if !event.Text.IsReplyMsg || event.ConversationType == "2" || strings.TrimSpace(event.MsgID) == "" {
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	fields := map[string]string{}
	collectDingtalkIdentityHashes(fields, "", envelope)
	var text, replied, content map[string]json.RawMessage
	if json.Unmarshal(envelope["text"], &text) == nil {
		collectDingtalkIdentityHashes(fields, "text.", text)
		if json.Unmarshal(text["repliedMsg"], &replied) == nil {
			collectDingtalkIdentityHashes(fields, "text.repliedMsg.", replied)
			if json.Unmarshal(replied["content"], &content) == nil {
				collectDingtalkIdentityHashes(fields, "text.repliedMsg.content.", content)
			}
		}
	}
	logger.Info("[dingtalk] reply identity evidence",
		"instance_sha256", dingtalkIdentityHash(a.Name()),
		"chat_sha256", dingtalkIdentityHash(event.SenderStaffId),
		"conversation_sha256", dingtalkIdentityHash(event.ConversationId),
		"identity_fields_sha256", fields,
	)
}

func collectDingtalkIdentityHashes(out map[string]string, path string, object map[string]json.RawMessage) {
	// 字段路径固定，正文、媒体及任意扩展属性均不进入日志。
	for _, field := range []string{"msgId", "messageId", "originalMsgId", "openMsgId", "processQueryKey", "senderId"} {
		var value string
		if json.Unmarshal(object[field], &value) == nil && strings.TrimSpace(value) != "" {
			out[path+field] = dingtalkIdentityHash(value)
		}
	}
}

func dingtalkIdentityHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
