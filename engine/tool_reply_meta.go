package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"sync"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon/observe/trace"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/skill"
)

// BUG-1 plumb：把 skill 执行产出的结构化「reply-safe」元数据从工具边界带到最终回复。
//
// 为什么需要：ToolExecutor.Execute 的返回类型是 (string, error)，只回传 skill.Result.Content，
// skill.Result.Metadata 在工具边界被整个丢弃。K12 错题入库徽章（record chip）等需要把结构化
// 元数据带到 reply.Metadata → 前端消息 metadata.record。改返回签名会波及大量调用点，故用一个
// ctx 作用域的收集器：ToolExecutor 执行 skill 后把白名单键收进 sink，runner 跑完后 runner 路径
// 排空进 msg.Metadata，由 buildReplyMetadata 转发。
//
// AP-1：engine 不认识具体键的领域语义，只透传领域中立白名单（record）。领域字面量由场景包
// （scenarios/k12）产出。

type replyMetaSinkKeyType struct{}

var replyMetaSinkKey = replyMetaSinkKeyType{}

// replySafeToolMetaKeys 是允许从工具结果透传到回复元数据的键白名单。
// 只收结构化、面向前端渲染的领域中立键；杜绝内部 skill 元数据泄漏到回复。
var replySafeToolMetaKeys = map[string]bool{
	"record":                           true, // 记录本入库徽章（{collection,fields,status} JSON），前端 messageRecordChip 消费
	"artifacts":                        true, // 只透传持久产物引用，不携带文件字节或内部调用参数。
	"print_artifacts":                  true,
	"text_solve_run_id":                true,
	"text_solve_refs":                  true,
	"text_solve_finalization_id":       true,
	"text_solve_input_digest":          true,
	"answer_source":                    true,
	"media_resend_operations":          true,
	"media_operations":                 true,
	teachingContextReceiptsMetadataKey: true,
}

type replyMetaSink struct {
	mu                   sync.Mutex
	m                    map[string]string
	pendingSkillContexts []pendingToolSkillContext
}

type pendingToolSkillContext struct {
	toolContentDigest [32]byte
	receipt           skill.ContextReceipt
	consumed          bool
}

// 成功读到的工具正文仅准备快照，不直接进入回复或历史。
func prepareToolSkillContextReceipt(ctx context.Context, content string, receipt skill.ContextReceipt) {
	s := replyMetaSinkFrom(ctx)
	if s == nil || content == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := sha256.Sum256([]byte(content))
	for _, pending := range s.pendingSkillContexts {
		if pending.toolContentDigest == digest && pending.receipt.Name == receipt.Name {
			return
		}
	}
	receipt.Status = "prepared"
	s.pendingSkillContexts = append(s.pendingSkillContexts, pendingToolSkillContext{toolContentDigest: digest, receipt: receipt})
}

// Provider 成功接纳的本次请求必须包含同一冻结正文，才确认其实际进入上下文。
func confirmConsumedToolSkillContexts(ctx context.Context, messages []llm.Message) {
	s := replyMetaSinkFrom(ctx)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer func() {
		raw := s.m[skillContextReceiptsMetadataKey]
		s.mu.Unlock()
		var receipts []skill.ContextReceipt
		if json.Unmarshal([]byte(raw), &receipts) != nil {
			return
		}
		for _, receipt := range receipts {
			trace.L(ctx).Info("Skill context consumed by provider", "skill", receipt.Name, "skill_version", receipt.Version, "content_digest", receipt.ContentDigest, "status", receipt.Status)
		}
	}()
	if prepared, _ := ctx.Value(preparedSkillContextKey{}).(*preparedSkillContext); prepared != nil && prepared.prompt != "" {
		for _, message := range messages {
			if message.Role != llm.RoleSystem || !strings.Contains(message.Content, prepared.prompt) {
				continue
			}
			receipts := cloneSkillContextReceipts(prepared.receipts)
			for i := range receipts {
				receipts[i].Status = "loaded"
			}
			encoded, _ := json.Marshal(receipts)
			s.m[skillContextReceiptsMetadataKey] = mergeSkillContextReceiptReferences(s.m[skillContextReceiptsMetadataKey], string(encoded))
			break
		}
	}
	for _, message := range messages {
		if message.Role != llm.RoleTool {
			continue
		}
		digest := sha256.Sum256([]byte(message.Content))
		for i := range s.pendingSkillContexts {
			pending := &s.pendingSkillContexts[i]
			if pending.consumed || pending.toolContentDigest != digest {
				continue
			}
			receipt := pending.receipt
			receipt.Status = "loaded"
			encoded, _ := json.Marshal([]skill.ContextReceipt{receipt})
			s.m[skillContextReceiptsMetadataKey] = mergeSkillContextReceiptReferences(s.m[skillContextReceiptsMetadataKey], string(encoded))
			pending.consumed = true
		}
	}
}

// ConsumedSkillContextReceipts 只复制本次成功 Provider 请求实际消费的冻结快照。
// 默认挂载、准备正文和历史消息不能独立产生本题教学依据。
func ConsumedSkillContextReceipts(ctx context.Context) []skill.ContextReceipt {
	s := replyMetaSinkFrom(ctx)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	encoded := s.m[skillContextReceiptsMetadataKey]
	s.mu.Unlock()
	var receipts []skill.ContextReceipt
	if json.Unmarshal([]byte(encoded), &receipts) != nil {
		return nil
	}
	return receipts
}

func cloneSkillContextReceipts(receipts []skill.ContextReceipt) []skill.ContextReceipt {
	encoded, err := json.Marshal(receipts)
	if err != nil {
		return nil
	}
	var copied []skill.ContextReceipt
	if json.Unmarshal(encoded, &copied) != nil {
		return nil
	}
	return copied
}

func mergeSkillContextReceiptReferences(previous, next string) string {
	var merged []skill.ContextReceipt
	seen := make(map[string]bool)
	for _, encoded := range []string{previous, next} {
		var receipts []skill.ContextReceipt
		if json.Unmarshal([]byte(encoded), &receipts) != nil {
			continue
		}
		for _, receipt := range receipts {
			identity := receipt.Name + "\x00" + receipt.Version + "\x00" + receipt.ContentDigest
			if receipt.Status != "loaded" || receipt.Name == "" || seen[identity] {
				continue
			}
			seen[identity] = true
			merged = append(merged, receipt)
		}
	}
	if len(merged) == 0 {
		return ""
	}
	data, _ := json.Marshal(merged)
	return string(data)
}

// withToolReplyMetaSink 在 ctx 上安装「工具→回复」元数据收集器（幂等）。
// 在 runner 跑工具循环前调用，使工具执行期间 skill 产出的白名单元数据可被收集。
func withToolReplyMetaSink(ctx context.Context) context.Context {
	if ctx.Value(replyMetaSinkKey) != nil {
		return ctx // 已装：避免嵌套覆盖丢失已收集项
	}
	return context.WithValue(ctx, replyMetaSinkKey, &replyMetaSink{m: map[string]string{}})
}

func replyMetaSinkFrom(ctx context.Context) *replyMetaSink {
	s, _ := ctx.Value(replyMetaSinkKey).(*replyMetaSink)
	return s
}

// stampToolReplyMeta 由 ToolExecutor 在 skill 执行后调用，收集白名单内的 reply-safe 元数据。
// ctx 无 sink（旧路径/无工具循环）时安全 no-op。
func stampToolReplyMeta(ctx context.Context, meta map[string]string) {
	if len(meta) == 0 {
		return
	}
	// 已冻结的领域文件复用宿主原回合生命周期，不创建媒体操作。
	if meta["print_artifacts"] != "" {
		if err := mediaflow.RetainRequest(ctx); err == nil {
			requestcancel.Retain(ctx)
		}
	}
	s := replyMetaSinkFrom(ctx)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range meta {
		if v != "" && replySafeToolMetaKeys[k] {
			if k == "artifacts" || k == "print_artifacts" || k == "text_solve_refs" {
				s.m[k] = mergeArtifactReplyReferences(s.m[k], v)
				continue
			}
			if k == "media_operations" || k == "media_resend_operations" {
				s.m[k] = mergeMediaOperationReferences(s.m[k], v)
				continue
			}
			if k == teachingContextReceiptsMetadataKey {
				s.m[k] = mergeSkillContextReceiptReferences(s.m[k], v)
				continue
			}
			// 后写覆盖：同一轮工具循环内多次入库取最后一条（对齐「一次批改一道题」语义）。
			s.m[k] = v
		}
	}
}

// 同轮多个工具回执按稳定对象身份合并，避免后一次动作遮蔽前一次已发布引用。
func mergeMediaOperationReferences(previous, next string) string {
	var merged []string
	seen := make(map[string]bool)
	for _, encoded := range []string{previous, next} {
		var ids []string
		if json.Unmarshal([]byte(encoded), &ids) != nil {
			continue
		}
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				merged = append(merged, id)
			}
		}
	}
	encoded, _ := json.Marshal(merged)
	return string(encoded)
}

func mergeArtifactReplyReferences(previous, next string) string {
	var merged []json.RawMessage
	seen := make(map[string]bool)
	for _, encoded := range []string{previous, next} {
		var items []json.RawMessage
		if json.Unmarshal([]byte(encoded), &items) != nil {
			continue
		}
		for _, item := range items {
			var fields map[string]any
			if json.Unmarshal(item, &fields) != nil {
				continue
			}
			identity := string(item)
			for _, key := range []string{"revision_id", "artifact_id", "attempt_id", "solve_run_id"} {
				if value, ok := fields[key].(string); ok && value != "" {
					kind, _ := fields["kind"].(string)
					identity = kind + ":" + key + ":" + value
					break
				}
			}
			if !seen[identity] {
				seen[identity] = true
				merged = append(merged, item)
			}
		}
	}
	if len(merged) == 0 {
		return ""
	}
	data, _ := json.Marshal(merged)
	return string(data)
}

// applyToolReplyMeta 把 sink 收集的 reply-safe 元数据落到 msg.Metadata，
// 供 buildReplyMetadata 转发进最终回复。在 runner 跑完后、组装回复前调用。
func applyToolReplyMeta(ctx context.Context, msg *adapter.Message) {
	s := replyMetaSinkFrom(ctx)
	if s == nil || msg == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) == 0 {
		return
	}
	ensureMessageMetadata(msg)
	for k, v := range s.m {
		if k == skillContextReceiptsMetadataKey || k == teachingContextReceiptsMetadataKey {
			v = mergeSkillContextReceiptReferences(msg.Metadata[k], v)
		}
		msg.Metadata[k] = v
	}
}
