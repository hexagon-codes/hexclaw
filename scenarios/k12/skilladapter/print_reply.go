package skilladapter

import (
	"context"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

// PrintableArtifactResult 把已冻结 PDF 的可信引用交给原工具回合，不自行发送或结束回复。
func PrintableArtifactResult(ctx context.Context, deps usecase.Deps, agent, artifactID, content string) (*skill.Result, error) {
	ref, _, err := deps.ResolvePrintReplyArtifact(ctx, agent, artifactID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal([]any{ref})
	if err != nil {
		return nil, err
	}
	return &skill.Result{Content: content, Metadata: map[string]string{"print_artifacts": string(encoded)}}, nil
}
