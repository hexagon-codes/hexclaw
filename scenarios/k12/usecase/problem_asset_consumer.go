package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// ProblemAssetConsumer 整理已提交批改的验证结果；只做本地发布，不发送模型请求。
type ProblemAssetConsumer struct {
	Records *k12storage.Store
}

func (c ProblemAssetConsumer) Name() string { return "problem-assets" }

func (c ProblemAssetConsumer) Handle(ctx context.Context, ev k12storage.OutboxEvent) error {
	if ev.EventType != k12storage.EventProblemAssetPrepare {
		return nil
	}
	if c.Records == nil {
		return fmt.Errorf("problem asset store is unavailable")
	}
	var p k12.ProblemAssetPublication
	if ev.PayloadVersion != 1 || json.Unmarshal([]byte(ev.Payload), &p) != nil || p.Verification.AgentName != ev.AgentName {
		return fmt.Errorf("invalid problem asset preparation event")
	}
	_, _, err := c.Records.PublishAssessedProblemAsset(ctx, p, ev.AggregateID)
	if errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		// 已纠正的批改和已停用资产不接受迟到的后台发布。
		return nil
	}
	return err
}
