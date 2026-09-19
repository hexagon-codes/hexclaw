package k12storage

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/sqliteutil"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ReuseSucceededRecognitionPhysicalInvocation 在同任务可恢复失败后复用成功清单、主批次或已授权复核。
// 读取不可变输入证明后，事务内再次确认当前所有权及成功载荷；不进入模型发送边界。
func (s *Store) ReuseSucceededRecognitionPhysicalInvocation(
	ctx context.Context, agentName, physicalID string,
) (k12.ModelPhysicalInvocation, bool, error) {
	current, err := s.GetModelPhysicalInvocation(ctx, agentName, physicalID)
	if err != nil {
		return current, false, err
	}
	if current.Status == k12.ModelInvocationSucceeded && current.ReusedFromPhysicalInvocationID != "" {
		return current, true, nil
	}
	isRepair := strings.HasPrefix(string(current.PhysicalUnit), "layout_repair_")
	if current.Status != k12.ModelInvocationPrepared || current.RecognitionPlanVersion != k12.RecognitionPlanVersionV2 ||
		(current.PhysicalUnit != k12.RecognitionPhysicalUnitWholePage && !strings.HasPrefix(string(current.PhysicalUnit), "layout_batch_") && !isRepair) {
		return current, false, nil
	}
	parent, err := s.getModelInvocationByID(ctx, current.ParentInvocationID)
	if err != nil || parent.Attempt <= 1 {
		return current, false, err
	}
	children, err := s.ListModelPhysicalInvocations(ctx, agentName, current.JobID)
	if err != nil {
		return current, false, err
	}
	for _, source := range children {
		if source.ParentInvocationID == parent.InvocationID || source.PhysicalUnit != current.PhysicalUnit ||
			source.RecognitionPlanVersion != k12.RecognitionPlanVersionV2 || source.Status != k12.ModelInvocationSucceeded {
			continue
		}
		prior, getErr := s.getModelInvocationByID(ctx, source.ParentInvocationID)
		if getErr != nil {
			return current, false, getErr
		}
		partial := prior.Status == k12.ModelInvocationReconciled && prior.FailureKind == "reconciled_partial_succeeded"
		if (prior.Status != k12.ModelInvocationFailed && !partial) || prior.Attempt >= parent.Attempt {
			continue
		}
		if prior.AgentName != parent.AgentName || prior.JobID != parent.JobID || prior.Stage != parent.Stage ||
			prior.RequestDigest != parent.RequestDigest ||
			!reflect.DeepEqual(prior.RouteSnapshot, parent.RouteSnapshot) ||
			!reflect.DeepEqual(prior.RequestPolicySnapshot, parent.RequestPolicySnapshot) ||
			!reflect.DeepEqual(source.RouteSnapshot, current.RouteSnapshot) ||
			!reflect.DeepEqual(source.RequestPolicySnapshot, current.RequestPolicySnapshot) {
			if partial {
				return current, false, fmt.Errorf("%w: partial recognition replay identity changed", ErrModelPhysicalInvocationConflict)
			}
			continue
		}
		currentPlan, loadErr := s.LoadRecognitionLayoutPlanRuntimeV2(ctx, agentName, parent.InvocationID)
		if loadErr != nil {
			return current, false, loadErr
		}
		priorPlan, loadErr := s.LoadRecognitionLayoutPlanRuntimeV2(ctx, agentName, prior.InvocationID)
		if loadErr != nil {
			return current, false, loadErr
		}
		if currentPlan.Header.PageDigest != priorPlan.Header.PageDigest || priorPlan.AuthorizedPlan == nil {
			if partial || isRepair {
				return current, false, fmt.Errorf("%w: recognition replay page or authority changed", ErrModelPhysicalInvocationConflict)
			}
			continue
		}
		if current.PhysicalUnit == k12.RecognitionPhysicalUnitWholePage {
			if currentPlan.ManifestPhysicalInvocationID != current.PhysicalInvocationID ||
				priorPlan.ManifestPhysicalInvocationID != source.PhysicalInvocationID ||
				currentPlan.HeaderDigest != current.PlanDigest || priorPlan.HeaderDigest != source.PlanDigest {
				if partial {
					return current, false, fmt.Errorf("%w: partial recognition replay manifest changed", ErrModelPhysicalInvocationConflict)
				}
				continue
			}
		} else if !isRepair {
			var classified bool
			if queryErr := s.db.QueryRowContext(ctx, `SELECT EXISTS(
                SELECT 1 FROM k12_recognition_layout_batch_settlements
                WHERE source_physical_invocation_id=? AND source_physical_result_digest=? AND classification='classified')`,
				source.PhysicalInvocationID, source.ResultDigest).Scan(&classified); queryErr != nil {
				return current, false, queryErr
			}
			if !classified {
				if partial {
					return current, false, fmt.Errorf("%w: partial recognition replay batch is unsettled", ErrModelPhysicalInvocationConflict)
				}
				continue
			}
		}
		if current.PhysicalUnit != k12.RecognitionPhysicalUnitWholePage {
			if currentPlan.AuthorizedPlan == nil || priorPlan.AuthorizedPlan == nil ||
				current.PlanDigest != currentPlan.AuthorizedPlan.AuthorizedPlanDigest ||
				source.PlanDigest != priorPlan.AuthorizedPlan.AuthorizedPlanDigest {
				if partial || isRepair {
					return current, false, fmt.Errorf("%w: recognition replay plan authority changed", ErrModelPhysicalInvocationConflict)
				}
				continue
			}
			left, right := *currentPlan.AuthorizedPlan, *priorPlan.AuthorizedPlan
			// 父回执身份不同导致计划摘要不同；模型实际消费的格式、像素、题目和顺序必须全同。
			left.ManifestInvocationID, right.ManifestInvocationID = "", ""
			left.AuthorizedPlanDigest, right.AuthorizedPlanDigest = "", ""
			if !reflect.DeepEqual(left, right) {
				if isRepair || partial {
					return current, false, fmt.Errorf("%w: partial recognition replay plan changed", ErrModelPhysicalInvocationConflict)
				}
				continue
			}
			if isRepair {
				if source.CandidateExactSetDigest != current.CandidateExactSetDigest {
					return current, false, fmt.Errorf("%w: repair replay target changed", ErrModelPhysicalInvocationConflict)
				}
				for _, pair := range []struct {
					parent k12.ModelInvocation
					child  k12.ModelPhysicalInvocation
				}{{parent, current}, {prior, source}} {
					var planID string
					if err := s.db.QueryRowContext(ctx, `SELECT plan_id FROM k12_recognition_layout_plans WHERE parent_invocation_id=? AND agent_name=?`, pair.parent.InvocationID, agentName).Scan(&planID); err != nil {
						return current, false, err
					}
					// 复用同一单轮授权的原图复读，不要求旧回执已完成本地结算。
					if err := validateRecognitionLayoutRepairAuthorizationEvidenceVia(ctx, s.db, pair.parent, pair.child, planID, pair.child.PlanDigest, false); err != nil {
						return current, false, err
					}
				}
			}
		}
		var reused k12.ModelPhysicalInvocation
		err = sqliteutil.RetryOnBusy(ctx, func() error {
			var reuseErr error
			reused, reuseErr = s.reuseRecognitionReceiptOnce(ctx, current, source)
			return reuseErr
		})
		return reused, err == nil, err
	}
	return current, false, nil
}

func (s *Store) reuseRecognitionReceiptOnce(
	ctx context.Context, current, source k12.ModelPhysicalInvocation,
) (k12.ModelPhysicalInvocation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.ModelPhysicalInvocation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := getModelPhysicalInvocationByIDVia(ctx, tx, current.AgentName, current.PhysicalInvocationID)
	if err != nil {
		return stored, err
	}
	if stored.Status == k12.ModelInvocationSucceeded && stored.ReusedFromPhysicalInvocationID == source.PhysicalInvocationID {
		return stored, nil
	}
	parent, err := getModelInvocationByIDVia(ctx, tx, current.ParentInvocationID)
	if err != nil {
		return stored, err
	}
	if stored.Status != k12.ModelInvocationPrepared || parent.Status != k12.ModelInvocationSent {
		return stored, fmt.Errorf("%w: recognition reuse requires prepared child and sent parent", records.ErrIllegalTransition)
	}
	var content sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT result_content FROM k12_model_physical_invocations
        WHERE physical_invocation_id=? AND agent_name=? AND job_id=? AND status='succeeded' AND result_digest=?`,
		source.PhysicalInvocationID, current.AgentName, current.JobID, source.ResultDigest).Scan(&content)
	if err != nil {
		return stored, err
	}
	if !content.Valid || content.String == "" || physicalInvocationResultDigest(content.String) != source.ResultDigest {
		return stored, fmt.Errorf("%w: recognition reuse source content is invalid", ErrModelPhysicalInvocationConflict)
	}
	result, err := tx.ExecContext(ctx, `UPDATE k12_model_physical_invocations
        SET status='succeeded',result_digest=?,result_content=?,external_request_id='',failure_kind='',
            reused_from_physical_invocation_id=?,updated_at=?
        WHERE physical_invocation_id=? AND agent_name=? AND status='prepared'`,
		source.ResultDigest, content.String, source.PhysicalInvocationID, nowUnix(), current.PhysicalInvocationID, current.AgentName)
	if err != nil {
		return stored, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return stored, fmt.Errorf("%w: recognition reuse lost prepared child", records.ErrIllegalTransition)
	}
	if current.PhysicalUnit == k12.RecognitionPhysicalUnitWholePage {
		result, err = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans
            SET status='manifest_succeeded',manifest_result_digest=?,updated_at=?
            WHERE parent_invocation_id=? AND agent_name=? AND manifest_physical_invocation_id=?
              AND header_digest=? AND status='prepared_manifest'`,
			source.ResultDigest, nowUnix(), current.ParentInvocationID, current.AgentName, current.PhysicalInvocationID, current.PlanDigest)
		if err != nil {
			return stored, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return stored, fmt.Errorf("%w: recognition reuse lost prepared manifest", records.ErrIllegalTransition)
		}
	} else if err = advanceRecognitionLayoutPlanAfterClaim(ctx, tx, current); err != nil {
		return stored, err
	}
	stored, err = getModelPhysicalInvocationByIDVia(ctx, tx, current.AgentName, current.PhysicalInvocationID)
	if err != nil {
		return stored, err
	}
	if err = tx.Commit(); err != nil {
		return stored, err
	}
	return stored, nil
}
