package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

var errMaterialModelPreparationRequired = errors.New("material requires model preparation")
var errMaterialForegroundDeferred = errors.New("material preparation deferred for foreground work")

// MaterialPreparationWorker 复用确定性求解和冻结模型路径，独立验证后才发布。
// 每个任务独立持久化，资料不能写入学生作答或学情。
type MaterialPreparationWorker struct {
	PreparePDFSource   func(context.Context, k12storage.MaterialPreparation) ([]byte, string, error)
	ResolveVisualModel func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error)
	ReadVisual         func(context.Context, []byte, string) (string, error)
	Records            *k12storage.Store
	Solver             Solver
	ResolveModel       func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error)
}

func (w *MaterialPreparationWorker) RunOnce(ctx context.Context) (did bool, err error) {
	ctx = k12.WithMaterialPreparation(ctx)
	busy, err := w.Records.MaterialForegroundBusy(ctx)
	if err != nil || busy {
		return false, err
	}
	p, err := w.Records.NextMaterialPreparation(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		if errors.Is(err, k12storage.ErrMaterialPreparationFenced) {
			err = errors.Join(err, w.Records.StopStaleMaterialPreparation(context.WithoutCancel(ctx), p))
		}
	}()
	if p.State == "verified" {
		return true, w.publish(ctx, p.TaskID)
	}
	if err = w.Records.ClaimMaterialPreparation(ctx, p); err != nil {
		return false, err
	}
	// 在真正调用前检查前台状态；一旦发送就不取消重发。
	busy, err = w.Records.MaterialForegroundBusy(ctx)
	if err != nil || busy {
		finishErr := w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "queued", "Foreground work has priority", "{}")
		return false, errors.Join(err, finishErr)
	}
	if len(p.Candidate.VisualObjectIDs) > 0 || len(p.Candidate.VisualPDFPages) > 0 {
		p, err = w.prepareVisual(ctx, p)
		if err != nil {
			if errors.Is(err, k12storage.ErrMaterialPreparationFenced) {
				return true, err
			}
			state, reason := "needs_review", "Source image interpretation is incomplete"
			if errors.Is(err, k12storage.ErrMaterialPreparationUnknown) {
				state, reason = "outcome_unknown", "Execution requires reconciliation"
			}
			if errors.Is(err, errMaterialForegroundDeferred) {
				state, reason = "queued", "Foreground work has priority"
			}
			finishErr := w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, state, reason, "{}")
			if state == "needs_review" || state == "queued" {
				return true, finishErr
			}
			return true, errors.Join(err, finishErr)
		}
	}
	facts := p.Candidate.Facts
	if p.VisualEvidence != nil {
		facts = p.VisualEvidence.Facts
	}
	recovery, recoveryErr := w.Records.MaterialHasRecovery(ctx, p)
	if recoveryErr != nil {
		if errors.Is(recoveryErr, k12storage.ErrMaterialPreparationFenced) {
			return true, recoveryErr
		}
		return true, errors.Join(recoveryErr, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "needs_review", "Material recovery plan is no longer current", "{}"))
	}
	if !recovery {
		asset, lookupErr := w.Records.FindExactProblemAsset(ctx, p.OwnerID, facts)
		if lookupErr == nil {
			err = w.Records.ReuseMaterialProblemAsset(ctx, p, asset)
			if errors.Is(err, k12storage.ErrMaterialPreparationUnknown) {
				return true, errors.Join(err, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "outcome_unknown", "Execution requires reconciliation", "{}"))
			}
			if errors.Is(err, k12storage.ErrProblemAssetUnavailable) || errors.Is(err, k12storage.ErrProblemAssetEvidence) {
				return true, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "needs_review", "Matching answer asset is no longer available", "{}")
			}
			return true, err
		}
		if !errors.Is(lookupErr, k12storage.ErrProblemAssetUnavailable) {
			return true, lookupErr
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// 不把尚未冻结 Provider 策略的资料请求直接发送到可变模型路由。
	callCtx = WithGradingPhysicalCallExecutor(callCtx, materialLocalOnlyExecutor{})
	problem := materialSolverProblem(facts)
	var result SolveResult
	modelPath := false
	if recovery {
		// 明确恢复只消费原冻结调用序列，不能改走新资产或本地另解。
		err = errMaterialModelPreparationRequired
	} else if solver, ok := w.Solver.(SubjectSolver); ok {
		result, err = solver.SolveSubject(callCtx, p.Candidate.Facts.Subject, problem, p.Candidate.Facts.AnswerContext["grade_term"], "")
	} else {
		result, err = w.Solver.Solve(callCtx, problem, p.Candidate.Facts.AnswerContext["grade_term"], "")
	}
	if errors.Is(err, errMaterialModelPreparationRequired) && w.ResolveModel != nil {
		modelPath = true
		var policy k12storage.MaterialModelPolicy
		if json.Unmarshal([]byte(p.Policy), &policy) != nil {
			snapshot, resolveErr := w.ResolveModel(ctx, k12.GradingModelSnapshot{})
			if resolveErr != nil {
				return true, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "needs_review", "Model route is unavailable", "{}")
			}
			policy, err = w.Records.FreezeMaterialModel(ctx, p, snapshot)
			if err != nil {
				return true, err
			}
		}
		// 冻结模型的超时属于单次调用，不能提前截断后续独立验证。
		modelCtx := k12.WithGradingModelSnapshot(ctx, policy.Model)
		executor := &materialPhysicalExecutor{records: w.Records, task: p, timeout: time.Duration(policy.Model.TimeoutMS) * time.Millisecond}
		modelCtx = WithGradingPhysicalCallExecutor(modelCtx, executor)
		if solver, ok := w.Solver.(SubjectSolver); ok {
			result, err = solver.SolveSubject(modelCtx, p.Candidate.Facts.Subject, problem, p.Candidate.Facts.AnswerContext["grade_term"], "")
		} else {
			result, err = w.Solver.Solve(modelCtx, problem, p.Candidate.Facts.AnswerContext["grade_term"], "")
		}
		if executor.deferred.Load() {
			err = errors.Join(err, errMaterialForegroundDeferred)
		}
	}
	unknown, receiptErr := w.Records.MaterialHasUnknownInvocation(context.WithoutCancel(ctx), p.TaskID)
	if receiptErr != nil {
		return true, receiptErr
	}
	if unknown {
		err = errors.Join(err, k12storage.ErrMaterialPreparationUnknown)
	}
	pendingRecovery := false
	if recovery && errors.Is(err, errMaterialForegroundDeferred) {
		var pendingErr error
		pendingRecovery, pendingErr = w.Records.MaterialRecoveryPending(context.WithoutCancel(ctx), p)
		if pendingErr != nil {
			return true, pendingErr
		}
	}
	if errors.Is(err, errMaterialForegroundDeferred) && (!unknown || pendingRecovery) {
		return true, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "queued", "Foreground work has priority", "{}")
	}
	if err != nil {
		state, reason := "outcome_unknown", "Execution requires reconciliation"
		if errors.Is(err, egress.ErrProviderNotSent) && !unknown {
			state, reason = "needs_review", "Material model request was not sent"
		}
		if errors.Is(err, errMaterialModelPreparationRequired) {
			state, reason = "needs_review", "Model-based material preparation is not available"
		}
		finishErr := w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, state, reason, "{}")
		if state == "needs_review" {
			return true, finishErr
		}
		return true, errors.Join(err, finishErr)
	}
	if result.Evidence.Verdict != VerdictAgree || result.Evidence.EvidenceType != EvidenceNumericExec || result.OutOfScopeKP != "" {
		return true, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "needs_review", "No independent executable verification", "{}")
	}
	raw, err := json.Marshal(SolveHomeworkResult{Solution: result.Solution, Evidence: result.Evidence})
	if err != nil {
		return true, err
	}
	if modelPath || result.Evidence.SolverOutputDigest != "" {
		_, err = w.Records.SaveMaterialModelVerification(context.WithoutCancel(ctx), p, string(raw))
	} else {
		err = w.Records.SaveMaterialLocalVerification(context.WithoutCancel(ctx), p, string(raw))
	}
	if err != nil {
		if errors.Is(err, k12storage.ErrProblemAssetEvidence) {
			return true, w.Records.FinishMaterialPreparation(context.WithoutCancel(ctx), p, "needs_review", "Independent verification evidence is incomplete", "{}")
		}
		return true, err
	}
	return true, w.publish(context.WithoutCancel(ctx), p.TaskID)
}

type materialLocalOnlyExecutor struct{}

func (materialLocalOnlyExecutor) ExecuteGradingPhysicalCall(context.Context, GradingPhysicalCallSpec, func(context.Context) (string, error)) (GradingPhysicalCallResult, error) {
	return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: errMaterialModelPreparationRequired}
}

// Run 串行消费准备任务；重启后未知执行留在原回执，不自动重发。
func (w *MaterialPreparationWorker) Run(ctx context.Context, onError func(error)) {
	if err := w.Records.RecoverMaterialPreparations(ctx); err != nil {
		onError(err)
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := w.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				onError(err)
			}
		}
	}
}

// materialPhysicalExecutor 只复用调用拦截接口，实际回执始终属于资料候选。
type materialPhysicalExecutor struct {
	records  *k12storage.Store
	task     k12storage.MaterialPreparation
	timeout  time.Duration
	deferred atomic.Bool
}

func (e *materialPhysicalExecutor) ExecuteGradingPhysicalCall(ctx context.Context, spec GradingPhysicalCallSpec, send func(context.Context) (string, error)) (GradingPhysicalCallResult, error) {
	busy, err := e.records.MaterialForegroundBusy(ctx)
	if err != nil {
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: err}
	}
	if busy {
		e.deferred.Store(true)
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: errMaterialForegroundDeferred}
	}
	invocation, fresh, err := e.records.ClaimMaterialInvocation(ctx, e.task, string(spec.Operation), spec.RequestDigest)
	if err != nil {
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: err}
	}
	if !fresh {
		return GradingPhysicalCallResult{Payload: invocation.ResultJSON, InvocationID: invocation.ID}, nil
	}
	timeout := e.timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, callErr := send(callCtx)
	if err = e.records.FinishMaterialInvocation(context.WithoutCancel(ctx), invocation, payload, callErr); err != nil {
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: err}
	}
	if callErr != nil {
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: callErr}
	}
	return GradingPhysicalCallResult{Payload: payload, InvocationID: invocation.ID}, nil
}

func (w *MaterialPreparationWorker) publish(ctx context.Context, task string) error {
	_, err := w.Records.PublishPreparedMaterialAsset(ctx, task)
	switch {
	case errors.Is(err, k12storage.ErrProblemAssetUnavailable):
		return w.Records.ReviewMaterialPublication(ctx, task, "Matching answer asset is unavailable")
	case errors.Is(err, k12storage.ErrProblemAssetConflict):
		return w.Records.ReviewMaterialPublication(ctx, task, "Verified answer differs from the published asset")
	case errors.Is(err, k12storage.ErrProblemAssetEvidence):
		return w.Records.ReviewMaterialPublication(ctx, task, "Independent verification evidence is incomplete")
	default:
		return err
	}
}

// materialSolverProblem 把决定答案的公共材料和选项一并交给独立求解，不携带参考答案。
func materialSolverProblem(facts k12.ProblemAssetFacts) string {
	var parts []string
	if len(facts.SharedMaterial) > 0 {
		parts = append(parts, "Shared material:", strings.Join(facts.SharedMaterial, "\n"))
	}
	parts = append(parts, facts.Stem)
	if len(facts.Options) > 0 {
		parts = append(parts, "Options:")
		for _, option := range facts.Options {
			parts = append(parts, option.Label+". "+option.Text)
		}
	}
	parts = append(parts, facts.VisualFacts...)
	return strings.Join(parts, "\n")
}
