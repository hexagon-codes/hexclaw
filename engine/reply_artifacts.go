package engine

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

// completeRequiredReplyContent 补齐冻结正文，保留本轮其他工具的结果与模型说明。
func completeRequiredReplyContent(content string, required []string) string {
	for _, body := range required {
		body = strings.TrimSpace(body)
		if body == "" || strings.Contains(content, body) {
			continue
		}
		if strings.TrimSpace(content) == "" {
			content = body
		} else {
			content = strings.TrimSpace(content) + "\n\n" + body
		}
	}
	return content
}

// ReplyArtifactResolver 由应用装配；领域负责读取冻结资产并接入原消息事务。
type ReplyArtifactResolver func(context.Context, *adapter.Message, map[string]string) (context.Context, error)

func (e *ReActEngine) SetReplyArtifactResolver(resolve ReplyArtifactResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.replyArtifactResolver = resolve
}

// AddReplyArtifactResolver 让独立产物提供者共享原回合的完成事务。
func (e *ReActEngine) AddReplyArtifactResolver(resolve ReplyArtifactResolver) {
	if resolve == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.replyArtifactResolver
	e.replyArtifactResolver = func(ctx context.Context, msg *adapter.Message, metadata map[string]string) (context.Context, error) {
		var err error
		if previous != nil {
			ctx, err = previous(ctx, msg, metadata)
			if err != nil {
				return ctx, err
			}
		}
		return resolve(ctx, msg, metadata)
	}
}

func (e *ReActEngine) prepareReplyArtifacts(ctx context.Context, msg *adapter.Message, metadata map[string]string) (context.Context, error) {
	e.mu.RLock()
	resolve := e.replyArtifactResolver
	e.mu.RUnlock()
	if resolve == nil {
		return ctx, nil
	}
	return resolve(ctx, msg, metadata)
}

func (e *ReActEngine) domainReplyAttachments(ctx context.Context, msg *adapter.Message, metadata map[string]string) ([]adapter.Attachment, error) {
	if completion, ok := storage.AssistantCompletionFromContext(ctx); ok {
		return completion.Attachments, nil
	}
	ctx, err := e.prepareReplyArtifacts(ctx, msg, metadata)
	if err != nil {
		return nil, err
	}
	completion, _ := storage.AssistantCompletionFromContext(ctx)
	return completion.Attachments, nil
}
