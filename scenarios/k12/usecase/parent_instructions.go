package usecase

import (
	"context"

	"github.com/hexagon-codes/hexclaw/config"
)

// 空快照明确保留旧调用语义，不能在恢复过程中偷偷补入当前规则。
func withParentInstructions(ctx context.Context, snapshot config.AgentInstructionsSnapshot) context.Context {
	if snapshot.Digest == "" {
		return config.WithAgentInstructions(ctx, config.AgentInstructionsSnapshot{})
	}
	return config.WithAgentInstructions(ctx, snapshot)
}

func requestDigestWithParentInstructions(digest string, snapshot config.AgentInstructionsSnapshot) string {
	if snapshot.Digest == "" {
		return digest
	}
	return modelInvocationDigest([]byte("parent-instructions-v1"), []byte(digest), []byte(snapshot.Digest))
}
