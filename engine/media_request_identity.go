package engine

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
)

// ValidateMediaReplay 先验证已接纳任务的归属和原始意图，不要求当前生成路由仍启用。
func (e *ReActEngine) ValidateMediaReplay(ctx context.Context, msg *adapter.Message) (bool, error) {
	if e.mediaFlow == nil || msg == nil || msg.SessionID == "" || messageRequestID(msg) == "" {
		return false, nil
	}
	ops, err := e.mediaFlow.RequestOperations(ctx, msg.UserID, mediaflow.Origin{Kind: "conversation", SessionID: msg.SessionID, RequestID: messageRequestID(msg)})
	if err != nil {
		return false, err
	}
	digest := mediaflow.SourceRequestDigest(msg.Content, msg.Attachments)
	for _, op := range ops {
		if op.Request.Origin.RequestDigest != "" && op.Request.Origin.RequestDigest != digest {
			return false, mediaflow.ErrConflict
		}
		if op.Request.Origin.Ordinal != "direct-media" {
			continue
		}
		if model := strings.TrimSpace(msg.Metadata["model"]); model != "" && model != op.Request.Model {
			return false, mediaflow.ErrConflict
		}
		provider := strings.TrimSpace(msg.Metadata["provider_instance_id"])
		if provider == "" {
			provider = strings.TrimSpace(msg.Metadata["provider"])
		}
		if provider != "" && provider != op.Request.Provider && provider != op.Route.ProviderID && provider != op.Route.ProviderKey && provider != op.Route.LegacyProviderAlias {
			return false, mediaflow.ErrConflict
		}
	}
	return len(ops) > 0, nil
}
