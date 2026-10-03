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
	if ev.EventType == k12storage.EventAssessmentCorrected {
		return c.publishCorrectedAsset(ctx, ev)
	}
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
	if p.Verification.Kind == k12.ProblemAnswerDeterministic {
		payload, _, _, err := decodeGroundedPhysicalPayload(p.AnswerResultJSON, nil)
		if err != nil {
			return err
		}
		// 资产保存平面解法，原事件与成功调用封套仍作为完整来源保留。
		p.AnswerResultJSON = payload
	}
	_, _, err := c.Records.PublishAssessedProblemAsset(ctx, p, ev.AggregateID)
	if errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		// 已纠正的批改和已停用资产不接受迟到的后台发布。
		return nil
	}
	return err
}
