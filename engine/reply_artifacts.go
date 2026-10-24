package engine

import (
	"context"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

// ReplyArtifactResolver 由应用装配；领域负责读取冻结资产并接入原消息事务。
type ReplyArtifactResolver func(context.Context, *adapter.Message, map[string]string) (context.Context, error)

func (e *ReActEngine) SetReplyArtifactResolver(resolve ReplyArtifactResolver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.replyArtifactResolver = resolve
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
