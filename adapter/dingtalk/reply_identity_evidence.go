package dingtalk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
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
	if quoted, exists := text["repliedMsg"]; exists {
		shape, truncated := dingtalkReplyContentShape(quoted)
		logger.Info("[dingtalk] reply content shape",
			"fields", shape,
			"truncated", truncated,
		)
	}
}

var (
	dingtalkReplyShapeField = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	dingtalkReplyHomeworkID = regexp.MustCompile(`\bHW-([A-Za-z0-9_-]+)`)
)

// dingtalkReplyContentShape 只记录引用载荷结构，不保留字段值；采样上限不参与消息处理。
func dingtalkReplyContentShape(raw json.RawMessage) ([]map[string]any, bool) {
	const maxFields, maxDepth, maxStringBytes = 48, 6, 16 * 1024
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	fields := make([]map[string]any, 0, 12)
	truncated := false
	var visit func(string, any, int)
	visit = func(path string, value any, depth int) {
		if len(fields) >= maxFields || depth > maxDepth {
			truncated = true
			return
		}
		field := map[string]any{"path": path}
		fields = append(fields, field)
		switch typed := value.(type) {
		case map[string]any:
			field["type"] = "object"
			keys := make([]string, 0, len(typed))
			for key := range typed {
				if dingtalkReplyShapeField.MatchString(key) {
					keys = append(keys, key)
				} else {
					truncated = true
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(path+"."+key, typed[key], depth+1)
				if len(fields) >= maxFields {
					truncated = true
					break
				}
			}
		case []any:
			field["type"] = "array"
			for index, item := range typed {
				visit(path+"["+strconv.Itoa(index)+"]", item, depth+1)
				if len(fields) >= maxFields {
					truncated = true
					break
				}
			}
		case string:
			field["type"] = "string"
			if len(typed) > maxStringBytes {
				field["hw_scan_complete"] = false
				truncated = true
				return
			}
			field["hw_scan_complete"] = true
			unique := map[string]struct{}{}
			for _, match := range dingtalkReplyHomeworkID.FindAllString(typed, -1) {
				unique[match] = struct{}{}
				if len(unique) == 2 {
					break
				}
			}
			field["hw_id_count_up_to_two"] = len(unique)
			var decoded any
			if json.Unmarshal([]byte(typed), &decoded) == nil {
				switch decoded.(type) {
				case map[string]any, []any:
					field["json_container"] = true
					visit(path+"{json}", decoded, depth+1)
				}
			}
		case float64:
			field["type"] = "number"
		case bool:
			field["type"] = "boolean"
		case nil:
			field["type"] = "null"
		}
	}
	visit("text.repliedMsg", value, 0)
	return fields, truncated
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
