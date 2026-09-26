package k12storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ImageTaskClassificationResponse 保留模型完整回复，与解析结果摘要分开。
type ImageTaskClassificationResponse struct {
	RawResponse        string `json:"raw_response,omitempty"`
	RawResponseDigest  string `json:"raw_response_digest,omitempty"`
	ResponseReceivedAt int64  `json:"response_received_at,omitempty"`
}

// ImageTaskClassificationParseFailure 保留本地重解析前的失败事实。
type ImageTaskClassificationParseFailure struct {
	Kind            string `json:"kind"`
	FinishedAt      int64  `json:"finished_at"`
	DispatchVersion int    `json:"dispatch_version"`
}

func classificationResponseDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// GetLatestClassificationInvocation 返回当前尝试，不复用旧尝试的分类指针。
func (s *Store) GetLatestClassificationInvocation(ctx context.Context, agentName, dispatchID string) (k12.ImageTaskInvocation, error) {
	return getLatestImageTaskInvocation(ctx, s.db, agentName, k12.ImageTaskOperationClassification, dispatchID, "")
}

// ReadImageTaskClassificationResponse 只接纳摘要与原文一致的已留存回复。
func ReadImageTaskClassificationResponse(inv k12.ImageTaskInvocation) (ImageTaskClassificationResponse, bool, error) {
	var response ImageTaskClassificationResponse
	if inv.ResultJSON == "" {
		return response, false, nil
	}
	if err := json.Unmarshal([]byte(inv.ResultJSON), &response); err != nil {
		return response, false, fmt.Errorf("%w: invalid classification response receipt", ErrImageTaskConflict)
	}
	if response.RawResponseDigest == "" {
		return response, false, nil
	}
	if response.RawResponseDigest != classificationResponseDigest(response.RawResponse) || response.ResponseReceivedAt <= 0 {
		return response, false, fmt.Errorf("%w: classification response digest mismatch", ErrImageTaskConflict)
	}
	return response, true, nil
}

// SaveImageTaskClassificationResponse 在任何解析前落盘，不改变已发送调用的身份。
func (s *Store) SaveImageTaskClassificationResponse(ctx context.Context, agentName, invocationID, raw string) error {
	inv, err := s.GetImageTaskInvocation(ctx, agentName, invocationID)
	if err != nil {
		return err
	}
	if inv.Operation != k12.ImageTaskOperationClassification {
		return ErrImageTaskInvalidState
	}
	prior, present, err := ReadImageTaskClassificationResponse(inv)
	if err != nil {
		return err
	}
	if present {
		if prior.RawResponse != raw {
			return ErrImageTaskConflict
		}
		return nil
	}
	if inv.Status != k12.ImageTaskInvocationSent || inv.ResultJSON != "" {
		return ErrImageTaskInvalidState
	}
	now := nowUnix()
	response := ImageTaskClassificationResponse{RawResponse: raw, RawResponseDigest: classificationResponseDigest(raw), ResponseReceivedAt: now}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE k12_image_task_invocations
        SET result_json=?,updated_at=? WHERE agent_name=? AND invocation_id=?
          AND operation='classification' AND status='sent' AND result_json=''`, string(encoded), now, agentName, invocationID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrImageTaskConflict
	}
	return nil
}
