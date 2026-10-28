package usecase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

type textSolveOriginKey struct{}

const textSolveFinalizeOperation k12.GradingItemOperation = "text_finalize"

// WithTextSolveOrigin 只供实际认证入口传递文字求解身份，不从工具业务参数读取。
func WithTextSolveOrigin(ctx context.Context, origin k12.TextSolveOrigin) context.Context {
	return context.WithValue(ctx, textSolveOriginKey{}, origin)
}

// WithAuthorizedTextSolveOrigin 供已校验owner和当前assistant的直接入口使用，不补造回合或学生身份。
func WithAuthorizedTextSolveOrigin(ctx context.Context, origin k12.TextSolveOrigin, agent string) context.Context {
	return WithTextSolveOrigin(k12storage.WithTextSolveActorScope(ctx, origin.OwnerID, agent), origin)
}

func (d Deps) textSolveOrigin(ctx context.Context, req GradeRequest) (k12.TextSolveOrigin, bool) {
	if origin, ok := ctx.Value(textSolveOriginKey{}).(k12.TextSolveOrigin); ok {
		return origin, true
	}
	i := skill.CurrentInvocation(ctx)
	if i.AuthenticatedUserID == "" {
		return k12.TextSolveOrigin{}, false
	}
	owner := i.AuthenticatedUserID
	if d.TextbookOwnerID != "" {
		owner = d.TextbookOwnerID
	}
	var run string
	if i.RequestID != "" {
		// 同一回合重发同题工具调用仍是原求解，不能用新的工具ordinal绕过未知调用。
		run = "text-" + modelInvocationDigest([]byte(i.RequestID), []byte(strings.TrimSpace(req.Problem)))
	}
	return k12.TextSolveOrigin{OwnerID: owner, RunID: run, SourceSessionID: i.SessionID, SourceMessageID: i.SourceMessageID, InputRevision: 1}, true
}

type textSolvePhysicalExecutor struct {
	records *k12storage.Store
	run     k12.TextSolveRun
}

func textSolveInvocationID(run k12.TextSolveRun, spec GradingPhysicalCallSpec) string {
	return "text-inv-" + modelInvocationDigest([]byte(run.OwnerID), []byte(run.AgentName), []byte(run.RunID), []byte(spec.Operation), []byte(spec.ExecutionKind), []byte(spec.RequestDigest))
}

func (e *textSolvePhysicalExecutor) ExecuteGradingProgramCall(ctx context.Context, spec GradingPhysicalCallSpec, next func(context.Context) (string, error)) (GradingPhysicalCallResult, error) {
	spec.ExecutionKind = k12.GradingExecutionLocalDeterministic
	return e.ExecuteGradingPhysicalCall(ctx, spec, next)
}

// ExecuteGradingPhysicalCall 在实际调用前CAS登记；恢复只读取成功回包，未知结果不重发。
func (e *textSolvePhysicalExecutor) ExecuteGradingPhysicalCall(ctx context.Context, spec GradingPhysicalCallSpec, next func(context.Context) (string, error)) (GradingPhysicalCallResult, error) {
	if spec.ExecutionKind == "" {
		spec.ExecutionKind = k12.GradingExecutionProvider
	}
	if (!spec.Operation.Valid() && spec.Operation != textSolveFinalizeOperation) || spec.RequestDigest == "" {
		return GradingPhysicalCallResult{}, ErrInvalidInput
	}
	if spec.ExecutionKind == k12.GradingExecutionProvider && (e.run.RouteSnapshot.Provider == "" || e.run.RouteSnapshot.Model == "" || e.run.RouteSnapshot.Route == "") {
		return GradingPhysicalCallResult{}, fmt.Errorf("text solve frozen provider route is unavailable")
	}
	inv, err := e.records.PrepareTextSolveInvocation(ctx, k12.TextSolveInvocation{InvocationID: textSolveInvocationID(e.run, spec), TextSolveOrigin: e.run.TextSolveOrigin, AgentName: e.run.AgentName, FactsDigest: e.run.FactsDigest, InputDigest: e.run.InputDigest, Operation: spec.Operation, ExecutionKind: spec.ExecutionKind, RequestDigest: spec.RequestDigest, RouteSnapshot: e.run.RouteSnapshot})
	if err != nil {
		return GradingPhysicalCallResult{}, err
	}
	if inv.Status == k12.ModelInvocationSucceeded {
		if modelInvocationDigest([]byte(inv.ResultJSON)) != inv.ResultDigest || !json.Valid([]byte(inv.ResultJSON)) {
			return GradingPhysicalCallResult{}, k12storage.ErrTextSolveConflict
		}
		if spec.KnownExecutionFailure != nil {
			if err := spec.KnownExecutionFailure(inv.ResultJSON); err != nil {
				return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: err}
			}
		}
		return GradingPhysicalCallResult{Payload: inv.ResultJSON, InvocationID: inv.InvocationID}, nil
	}
	if inv.Status != k12.ModelInvocationPrepared {
		if inv.Status == k12.ModelInvocationFailed {
			return GradingPhysicalCallResult{Payload: inv.ResultJSON, InvocationID: inv.InvocationID}, gradingPhysicalNoRetryError{cause: fmt.Errorf("text solve invocation failed: solve_run_id=%s invocation=%s", e.run.RunID, inv.InvocationID)}
		}
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: fmt.Errorf("%w: solve_run_id=%s invocation=%s status=%s", k12storage.ErrTextSolveUnknown, e.run.RunID, inv.InvocationID, inv.Status)}
	}
	inv, claimed, err := e.records.ClaimTextSolveInvocation(ctx, e.run.OwnerID, e.run.AgentName, inv.InvocationID)
	if err != nil {
		return GradingPhysicalCallResult{}, err
	}
	if !claimed {
		if inv.Status == k12.ModelInvocationSucceeded && inv.ResultDigest == modelInvocationDigest([]byte(inv.ResultJSON)) {
			return GradingPhysicalCallResult{Payload: inv.ResultJSON, InvocationID: inv.InvocationID}, nil
		}
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: fmt.Errorf("%w: solve_run_id=%s invocation=%s", k12storage.ErrTextSolveUnknown, e.run.RunID, inv.InvocationID)}
	}
	payload, callErr := next(k12.WithGradingModelSnapshot(ctx, e.run.RouteSnapshot))
	status := k12.ModelInvocationSucceeded
	failureClass, failureCode := "", ""
	if callErr != nil {
		status = k12.ModelInvocationFailed
		failureClass, failureCode = "execution", "execution_failed"
		if spec.ExecutionKind == k12.GradingExecutionProvider && !errors.Is(callErr, egress.ErrProviderNotSent) && sentProviderOutcomeUnknown(callErr, ctx.Err()) {
			status = k12.ModelInvocationOutcomeUnknown
			failureClass, failureCode = "provider_transport", "outcome_unknown"
		}
	} else if !json.Valid([]byte(payload)) {
		status = k12.ModelInvocationFailed
		failureClass, failureCode = "response", "invalid_json"
		callErr = fmt.Errorf("text solve physical response is not valid JSON")
	} else if spec.KnownExecutionFailure != nil {
		if failure := spec.KnownExecutionFailure(payload); failure != nil {
			status = k12.ModelInvocationFailed
			failureClass, failureCode = "local", "verification_execution_failed"
			callErr = errors.Join(egress.ErrProviderResponseProcessed, failure)
		}
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	inv, saveErr := e.records.FinishTextSolveInvocation(commitCtx, e.run.OwnerID, e.run.AgentName, inv.InvocationID, status, payload, failureClass, failureCode)
	if saveErr != nil {
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: errors.Join(ErrModelInvocationRequiresReconciliation, callErr, saveErr)}
	}
	if callErr != nil {
		if status == k12.ModelInvocationOutcomeUnknown {
			return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: fmt.Errorf("%w: solve_run_id=%s invocation=%s: %w", k12storage.ErrTextSolveUnknown, e.run.RunID, inv.InvocationID, callErr)}
		}
		return GradingPhysicalCallResult{Payload: inv.ResultJSON, InvocationID: inv.InvocationID}, gradingPhysicalNoRetryError{cause: callErr}
	}
	return GradingPhysicalCallResult{Payload: payload, InvocationID: inv.InvocationID}, nil
}

// SolveTextProblem 是普通API、K12工具与钉钉文字求解共用的持久执行入口。
func (d Deps) SolveTextProblem(ctx context.Context, origin k12.TextSolveOrigin, req GradeRequest) (SolveHomeworkResult, error) {
	solved, err := d.solveTextProblem(ctx, origin, req)
	if err != nil || solved.Finalization == nil {
		return solved, err
	}
	report, err := d.textSolveTeachingReport(ctx, *solved.Finalization)
	if err != nil {
		return solved, fmt.Errorf("text report projection failed; solve_run_id=%s: %w", solved.Finalization.RunID, err)
	}
	solved.TeachingReport = report
	return solved, nil
}

func (d Deps) solveTextProblem(ctx context.Context, origin k12.TextSolveOrigin, req GradeRequest) (SolveHomeworkResult, error) {
	if d.Records == nil || strings.TrimSpace(origin.OwnerID) == "" || req.AgentName == "" || req.Problem == "" {
		return SolveHomeworkResult{}, fmt.Errorf("%w: text solve store and trusted owner are required", ErrInvalidInput)
	}
	if invocation := skill.CurrentInvocation(ctx); invocation.AuthenticatedUserID != "" {
		// HTTP身份只有认证用户；实际工具回合仍必须携带并核对其路由、会话和原消息。
		toolTurn := invocation.RequestID != "" || invocation.RoutedAgentName != "" || invocation.SessionID != "" || invocation.SourceMessageID != "" || invocation.ToolCallID != ""
		if toolTurn || !k12storage.TextSolveActorScopeMatches(ctx, origin.OwnerID, req.AgentName) {
			if invocation.RoutedAgentName != req.AgentName || invocation.SessionID != origin.SourceSessionID || invocation.SourceMessageID != origin.SourceMessageID {
				return SolveHomeworkResult{}, k12storage.ErrTextSolveConflict
			}
			if err := d.Records.ValidateTextSolveRequest(ctx, origin, invocation.AuthenticatedUserID, invocation.RequestID); err != nil {
				return SolveHomeworkResult{}, err
			}
		}
	}
	if origin.RunID == "" {
		origin.RunID = "text-" + idgen.NanoID()
	}
	if origin.InputRevision == 0 {
		origin.InputRevision = 1
	}
	if err := validateGradeInput(req.Grade); err != nil {
		return SolveHomeworkResult{}, err
	}
	subject, err := normalizeSubject(req.Subject)
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	req.Subject = subject
	if subject == "" {
		subject = "数学"
	}
	req.SourceSession = origin.SourceSessionID
	// 先读原输入的兼容快照；热更新、日期变化和卸载不能改变同一run的输入身份。
	prior, priorErr := d.Records.GetTextSolveRun(ctx, origin.OwnerID, req.AgentName, origin.RunID)
	if priorErr == nil {
		var frozen GradeRequest
		if json.Unmarshal([]byte(prior.RequestJSON), &frozen) != nil {
			return SolveHomeworkResult{}, k12storage.ErrTextSolveConflict
		}
		// 默认值属于原任务输入；档案更新只影响新任务，显式年级仍参与冲突核对。
		if req.GradeFromProfile {
			req.Grade = frozen.Grade
		}
		incomingBase, storedBase := req, frozen
		clearTextReportSnapshot(&incomingBase)
		clearTextReportSnapshot(&storedBase)
		if modelInvocationResultDigest(incomingBase) != modelInvocationResultDigest(storedBase) {
			return SolveHomeworkResult{}, k12storage.ErrTextSolveConflict
		}
		req.TeachingReport = frozen.TeachingReport
		req.SolveOutputVersion = frozen.SolveOutputVersion
		req.ParentTeachingContract = frozen.ParentTeachingContract
		req.ParentTeachingSkillSnapshot = frozen.ParentTeachingSkillSnapshot
	} else if !errors.Is(priorErr, sql.ErrNoRows) {
		return SolveHomeworkResult{}, priorErr
	} else {
		clearTextReportSnapshot(&req)
		if d.TeachingReportResources != nil {
			snapshot, err := d.freezeTeachingReportTask(ctx, req.AgentName, subject, req.Grade, "解题报告")
			if err != nil {
				return SolveHomeworkResult{}, err
			}
			if snapshot != nil && snapshot.Bundle.InputSchema == "homework-report-render-v2" {
				freezer, ok := d.Solver.(ReportSolveTeachingContextFreezer)
				if !ok {
					return SolveHomeworkResult{}, fmt.Errorf("report teaching contract freezer is unavailable")
				}
				contract, receipts, err := freezer.FreezeReportSolveTeachingContext(ctx, subject, req.Grade)
				if err != nil {
					return SolveHomeworkResult{}, err
				}
				req.TeachingReport = snapshot
				req.SolveOutputVersion = SolveOutputWithTeachingReportV2
				req.ParentTeachingContract = contract
				req.ParentTeachingSkillSnapshot = receipts
			}
		}
	}
	facts := k12.ProblemAssetFacts{Subject: subject, Stem: req.Problem, AnswerContext: map[string]string{"grade_term": req.Grade}}
	identity, err := facts.ExactIdentity(origin.OwnerID)
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	inputDigest := modelInvocationDigest(requestJSON)
	// 同一run先读冻结路由；热更新不能改变恢复时的请求身份。
	run, err := d.Records.GetTextSolveRun(ctx, origin.OwnerID, req.AgentName, origin.RunID)
	if errors.Is(err, sql.ErrNoRows) {
		route, _ := k12.GradingModelSnapshotFromContext(ctx)
		if d.TextSolveRoute != nil {
			resolved, routeErr := d.TextSolveRoute(ctx, req.AgentName)
			if routeErr == nil {
				route = resolved
			} else {
				// 本地计算和已有答案采用不依赖当前模型，缺路由只在真实Provider分支阻断。
				route = k12.GradingModelSnapshot{}
			}
		}
		run = k12.TextSolveRun{TextSolveOrigin: origin, AgentName: req.AgentName, Facts: facts, FactsDigest: identity.FactsDigest, InputDigest: inputDigest, RequestJSON: string(requestJSON), RouteSnapshot: k12.NormalizeGradingModelSnapshot(route)}
	} else if err != nil {
		return SolveHomeworkResult{}, err
	}
	if run.InputDigest != inputDigest || run.FactsDigest != identity.FactsDigest || run.TextSolveOrigin != origin {
		return SolveHomeworkResult{}, k12storage.ErrTextSolveConflict
	}
	run, _, err = d.Records.PrepareTextSolveRun(ctx, run)
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	if f, err := d.Records.GetTextSolveFinalization(ctx, origin.OwnerID, req.AgentName, origin.RunID); err == nil {
		return d.restoreTextSolveResult(ctx, f)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SolveHomeworkResult{}, err
	}
	calls, err := d.Records.ListTextSolveInvocations(ctx, origin.OwnerID, req.AgentName, origin.RunID)
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	if len(calls) > 0 {
		// 已成功的本地结果先补原最终事务，不能退回新的求解动作。
		if finalized, recoverErr := d.RecoverTextSolveFinalization(ctx, run.OwnerID, run.AgentName, run.RunID); recoverErr == nil {
			return d.restoreTextSolveResult(ctx, finalized)
		} else if !errors.Is(recoverErr, sql.ErrNoRows) {
			if errors.Is(recoverErr, k12storage.ErrTextSolveUnknown) {
				return SolveHomeworkResult{}, gradingPhysicalNoRetryError{cause: recoverErr}
			}
			return SolveHomeworkResult{}, recoverErr
		}
	}
	previousAdoption, adoptionErr := d.Records.FindProblemAssetAdoption(ctx, run.OwnerID, k12.TextSolveAdoptionTaskID(run.AgentName, run.RunID), run.FactsDigest, run.InputRevision)
	if adoptionErr == nil {
		if previousAdoption.InputDigest != run.InputDigest {
			return SolveHomeworkResult{}, k12storage.ErrTextSolveConflict
		}
		if err := d.Records.ValidateProblemAssetAdoption(ctx, run.OwnerID, previousAdoption.AdoptionID); err != nil {
			return SolveHomeworkResult{}, errors.Join(k12storage.ErrTextSolveConflict, err)
		}
		original, err := d.Records.GetProblemAssetVersion(ctx, run.OwnerID, previousAdoption.AssetID, previousAdoption.AssetVersion)
		if err != nil {
			return SolveHomeworkResult{}, err
		}
		return d.finalizeAdoptedTextSolve(ctx, run, req, original, previousAdoption)
	} else if !errors.Is(adoptionErr, sql.ErrNoRows) {
		return SolveHomeworkResult{}, adoptionErr
	}
	reuseEligible := len(calls) == 0 && textSolveAssetEligible(req)
	if reuseEligible {
		// 答案复用仍消费本次课程边界，知识点映射不能沿用旧求解结果。
		outOfScope, _, _ := d.outOfScope(ctx, req)
		reuseEligible = !outOfScope
	}
	if reuseEligible {
		v, lookupErr := d.Records.FindExactProblemAsset(ctx, origin.OwnerID, facts)
		if errors.Is(lookupErr, k12storage.ErrProblemAssetUnavailable) {
			v, lookupErr = findSourceFormatProblemAsset(ctx, d.Records, origin.OwnerID, facts, true)
		}
		if errors.Is(lookupErr, k12storage.ErrProblemAssetUnavailable) {
			v, lookupErr = d.Records.FindEquivalentProblemAsset(ctx, origin.OwnerID, facts)
		}
		if lookupErr == nil {
			a, _, adoptErr := d.Records.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{OwnerID: origin.OwnerID, JobID: k12.TextSolveAdoptionTaskID(req.AgentName, origin.RunID), ProblemID: identity.FactsDigest, InputRevision: origin.InputRevision, InputDigest: inputDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, FactsDigest: v.FactsDigest})
			if adoptErr == nil {
				return d.finalizeAdoptedTextSolve(ctx, run, req, v, a)
			}
			if !errors.Is(adoptErr, k12storage.ErrProblemAssetUnavailable) {
				return SolveHomeworkResult{}, adoptErr
			}
		} else if !errors.Is(lookupErr, k12storage.ErrProblemAssetUnavailable) {
			// 查询暂时失败不改变原求解行为，也不能声称采用成功。
		}
	}
	executor := &textSolvePhysicalExecutor{records: d.Records, run: run}
	solveCtx := WithGradingPhysicalCallExecutor(k12.WithGradingModelSnapshot(ctx, run.RouteSnapshot), executor)
	// 外层本地求解动作在执行前登记；嵌套模型/程序调用拥有独立实际回执。
	physical, err := executor.ExecuteGradingProgramCall(solveCtx, GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolve, RequestDigest: modelInvocationDigest([]byte("text-solve-execution-v1"), []byte(run.InputDigest))}, func(callCtx context.Context) (string, error) {
		solved, err := d.solveHomeworkProblem(callCtx, req)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(solved)
		return string(raw), err
	})
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	var solved SolveHomeworkResult
	if json.Unmarshal([]byte(physical.Payload), &solved) != nil {
		return solved, k12storage.ErrTextSolveConflict
	}
	return d.FinalizeTextSolve(ctx, run, solved)
}

// finalizeAdoptedTextSolve 对首次采用和恢复使用同一原答案及本次教学适配链。
func (d Deps) finalizeAdoptedTextSolve(ctx context.Context, run k12.TextSolveRun, req GradeRequest, version k12.ProblemAssetVersion, adoption k12.ProblemAssetAdoption) (SolveHomeworkResult, error) {
	solved, _, err := adoptedSolveResult(version, adoption)
	if err != nil {
		return SolveHomeworkResult{}, err
	}
	// 旧答案的教学候选不属于本次输入，只有后续本次适配产生的候选可以保留。
	solved.Generation = nil
	_, _, solved.CurriculumUnmapped = d.outOfScope(ctx, req)
	if req.TeachingReport != nil {
		executor := &textSolvePhysicalExecutor{records: d.Records, run: run}
		solved, err = d.adaptReusedReportTeaching(WithGradingPhysicalCallExecutor(k12.WithGradingModelSnapshot(ctx, run.RouteSnapshot), executor), req, solved)
		if err != nil {
			return solved, err
		}
	}
	return d.FinalizeTextSolve(ctx, run, solved)
}

func textSolveAssetEligible(req GradeRequest) bool {
	if req.Subject != "" && req.Subject != "数学" {
		return false
	}
	for _, marker := range []string{"如图", "下图", "上图", "图中", "图示", "表中", "下表", "上表", "![", "<img", "根据材料"} {
		if strings.Contains(req.Problem, marker) {
			return false
		}
	}
	return true
}

// FinalizeTextSolve 保存原结果；只为已有真实资格建立发布事件，不重新调用求解器。
func (d Deps) FinalizeTextSolve(ctx context.Context, run k12.TextSolveRun, solved SolveHomeworkResult) (SolveHomeworkResult, error) {
	calls, err := d.Records.ListTextSolveInvocations(ctx, run.OwnerID, run.AgentName, run.RunID)
	if err != nil {
		return solved, err
	}
	localID := "text-finalize-" + modelInvocationDigest([]byte(run.OwnerID), []byte(run.AgentName), []byte(run.RunID))
	finalSpec := GradingPhysicalCallSpec{Operation: textSolveFinalizeOperation, ExecutionKind: k12.GradingExecutionLocalDeterministic, RequestDigest: modelInvocationDigest([]byte("text-finalization-v1"), []byte(run.InputDigest))}
	finalInvocationID := textSolveInvocationID(run, finalSpec)
	proof := k12.ProblemAssetVerification{SourceKind: k12.ProblemAssetSourceTextSolve, AgentName: run.AgentName, InputDigest: run.InputDigest, FactsDigest: run.FactsDigest}
	var generator, verifier, localSolve *k12.TextSolveInvocation
	physicalCount := 0
	for i := range calls {
		call := &calls[i]
		if call.Operation != k12.GradingItemOperationSolve && call.Operation != textSolveFinalizeOperation {
			physicalCount++
		}
		if call.Status != k12.ModelInvocationSucceeded {
			continue
		}
		if call.Operation == k12.GradingItemOperationSolve && call.ExecutionKind == k12.GradingExecutionLocalDeterministic {
			localSolve = call
		}
		if call.Operation == k12.GradingItemOperationSolveGenerate && call.ExecutionKind == k12.GradingExecutionProvider {
			var result struct{ Output string }
			if json.Unmarshal([]byte(call.ResultJSON), &result) == nil {
				h := sha256.Sum256([]byte(result.Output))
				if hex.EncodeToString(h[:]) == solved.Evidence.SolverOutputDigest {
					generator = call
				}
			}
		}
		if call.Operation == k12.GradingItemOperationSolveVerify {
			var result struct {
				Receipt struct {
					RunID       string `json:"run_id"`
					InputDigest string `json:"input_digest"`
				} `json:"execution_receipt"`
			}
			if json.Unmarshal([]byte(call.ResultJSON), &result) == nil && result.Receipt.RunID != "" && result.Receipt.RunID == solved.Evidence.VerificationRunID && result.Receipt.InputDigest == solved.Evidence.VerificationInputDigest {
				verifier = call
			}
		}
	}
	eligible := solved.Evidence.Verdict == VerdictAgree && solved.Evidence.EvidenceType == EvidenceNumericExec && !solved.OutOfScope && textSolveAssetEligible(GradeRequest{Subject: run.Facts.Subject, Problem: run.Facts.Stem})
	adopted := solved.AnswerSource != nil && solved.AnswerSource.Kind == k12.ProblemAnswerAsset
	if adopted && !d.validTextSolveAdaptation(ctx, run, solved, calls) {
		solved.Generation = nil
	}
	localVerified := false
	if capable, ok := d.Solver.(interface{ UsesGradingPhysicalCalls() bool }); ok {
		localVerified = capable.UsesGradingPhysicalCalls() && eligible && !adopted
		programProof := localSolve != nil && physicalCount == 0
		for _, call := range calls {
			if call.ExecutionKind == k12.GradingExecutionProvider {
				localVerified = false
			}
			if call.ExecutionKind == k12.GradingExecutionLocalDeterministic && validTextSolveProgramReceipt(call, solved.Evidence) {
				programProof = true
			}
		}
		localVerified = localVerified && programProof && localSolve != nil
	}
	if generator != nil && verifier != nil && eligible {
		proof.Kind = k12.ProblemAnswerModel
		proof.Policy = "model-with-execution-v1"
		proof.InvocationID = verifier.InvocationID
		proof.ResultDigest = verifier.ResultDigest
		proof.GenerationInvocationID = generator.InvocationID
		proof.GenerationResultDigest = generator.ResultDigest
		proof.SolverOutputDigest = solved.Evidence.SolverOutputDigest
		proof.VerificationInputDigest = solved.Evidence.VerificationInputDigest
		proof.VerificationRunID = solved.Evidence.VerificationRunID
		solved.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerModel, FactsDigest: run.FactsDigest, InvocationID: verifier.InvocationID}
	} else if localVerified {
		solved.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerDeterministic, FactsDigest: run.FactsDigest, InvocationID: localSolve.InvocationID}
	}
	solved.Finalization = nil
	raw, err := json.Marshal(solved)
	if err != nil {
		return solved, err
	}
	executor := &textSolvePhysicalExecutor{records: d.Records, run: run}
	// 最终结算单独记为本地动作，不冒充前面的求解或 Provider 请求。
	inv, err := executor.ExecuteGradingProgramCall(ctx, finalSpec, func(context.Context) (string, error) { return string(raw), nil })
	if err != nil {
		return solved, err
	}
	if inv.InvocationID != finalInvocationID || inv.Payload != string(raw) {
		return solved, k12storage.ErrTextSolveConflict
	}
	f := k12.TextSolveFinalization{SourceKind: k12.ProblemAssetSourceTextSolve, FinalizationID: localID, TextSolveOrigin: run.TextSolveOrigin, AgentName: run.AgentName, InputDigest: run.InputDigest, FactsDigest: run.FactsDigest, InvocationID: inv.InvocationID, ResultJSON: string(raw), AnswerSource: solved.AnswerSource}
	if localVerified {
		proof.InvocationID = localSolve.InvocationID
		proof.ResultDigest = localSolve.ResultDigest
		proof.Kind = k12.ProblemAnswerDeterministic
		proof.Policy = "local-deterministic-v1"
		proof.SolverOutputDigest, proof.VerificationInputDigest, proof.VerificationRunID = solved.Evidence.SolverOutputDigest, solved.Evidence.VerificationInputDigest, solved.Evidence.VerificationRunID
	}
	if (localVerified || generator != nil && verifier != nil && eligible) && solved.AnswerSource != nil && solved.AnswerSource.Kind != k12.ProblemAnswerAsset {
		assetResult := solved
		assetResult.Generation = nil
		answer := solved.Solution
		if generator != nil {
			var result struct{ Output string }
			_ = json.Unmarshal([]byte(generator.ResultJSON), &result)
			answer = result.Output
			assetResult.Solution = answer
		}
		answerRaw, _ := json.Marshal(assetResult)
		if localVerified {
			answerRaw = []byte(localSolve.ResultJSON)
		}
		f.Publication = &k12.ProblemAssetPublication{OwnerID: run.OwnerID, PublicationID: "text-solve:" + localID, Facts: run.Facts, Answer: answer, AnswerResultJSON: string(answerRaw), Verification: proof}
	}
	f, _, err = d.Records.FinalizeTextSolve(ctx, f)
	if err != nil {
		return solved, err
	}
	solved.Finalization = &f
	return solved, nil
}

// validTextSolveAdaptation 仅接纳本次输入对应的成功生成及审核回包，不信任资产携带的旧候选。
func (d Deps) validTextSolveAdaptation(ctx context.Context, run k12.TextSolveRun, solved SolveHomeworkResult, calls []k12.TextSolveInvocation) bool {
	generation := solved.Generation
	if generation == nil || generation.OutputVersion != SolveOutputWithTeachingReportV2 || generation.ParentGuideCandidate == nil || generation.AdaptationGenerationID == "" || generation.AdaptationAuditID == "" {
		return false
	}
	var req GradeRequest
	if json.Unmarshal([]byte(run.RequestJSON), &req) != nil || req.SolveOutputVersion != generation.OutputVersion {
		return false
	}
	var generated, audited *k12.TextSolveInvocation
	for i := range calls {
		call := &calls[i]
		if call.OwnerID != run.OwnerID || call.AgentName != run.AgentName || call.RunID != run.RunID || call.InputDigest != run.InputDigest || call.FactsDigest != run.FactsDigest || call.InputRevision != run.InputRevision || call.Operation != k12.GradingItemOperationParentGuide || call.ExecutionKind != k12.GradingExecutionProvider || call.Status != k12.ModelInvocationSucceeded || call.ResultDigest != modelInvocationDigest([]byte(call.ResultJSON)) {
			continue
		}
		if call.InvocationID == generation.AdaptationGenerationID {
			generated = call
		}
		if call.InvocationID == generation.AdaptationAuditID {
			audited = call
		}
	}
	if generated == nil || audited == nil {
		return false
	}
	request := d.parentTeachingGuideRequest(ctx, req, solved, GradeOutcome{})
	if generated.RequestDigest != modelInvocationResultDigest(struct {
		Phase   string
		Request ParentTeachingGuideRequest
		Source  *k12.ProblemAnswerSource
	}{"report_current_teaching", request, solved.AnswerSource}) {
		return false
	}
	var guide ParentTeachingGuide
	if json.Unmarshal([]byte(generated.ResultJSON), &guide) != nil {
		return false
	}
	guide, err := finalizeParentTeachingGuide(guide, solved.Solution)
	if err != nil || guide.ReportContent == nil || modelInvocationResultDigest(guide) != modelInvocationResultDigest(*generation.ParentGuideCandidate) || modelInvocationResultDigest(guide.SkillContextReceipts) != modelInvocationResultDigest(generation.SkillContextReceipts) {
		return false
	}
	sourceDigest := solved.Evidence.SolverOutputDigest
	if sourceDigest == "" {
		sourceDigest = teachingReportResourceDigest([]byte(solved.Solution))
	}
	auditRequest := ParentTeachingGuideAuditRequest{Request: request, Guide: guide, SourceSolutionDigest: sourceDigest}
	if audited.RequestDigest != modelInvocationResultDigest(struct {
		Phase   string
		Request ParentTeachingGuideAuditRequest
	}{"report_current_teaching_audit", auditRequest}) {
		return false
	}
	var audit ParentTeachingGuideAudit
	return json.Unmarshal([]byte(audited.ResultJSON), &audit) == nil && audit.Verdict == "VALID" && generation.GuideAudit == audit.Verdict && audit.SourceSolutionDigest == sourceDigest && generation.SourceSolutionDigest == sourceDigest
}

func validTextSolveProgramReceipt(call k12.TextSolveInvocation, evidence SolveEvidence) bool {
	if call.Status != k12.ModelInvocationSucceeded || call.ResultDigest != modelInvocationDigest([]byte(call.ResultJSON)) {
		return false
	}
	var payload struct {
		Receipt struct {
			RunID           string `json:"run_id"`
			InputDigest     string `json:"input_digest"`
			Status          string `json:"status"`
			ExitCode        int    `json:"exit_code"`
			Error           string `json:"error"`
			Timeout         bool   `json:"timeout"`
			RuntimeMissing  bool   `json:"runtime_missing"`
			Truncated       bool   `json:"truncated"`
			StdoutTruncated bool   `json:"stdout_truncated"`
			StderrTruncated bool   `json:"stderr_truncated"`
			Stdout          string `json:"stdout"`
			StdoutBytes     int64  `json:"stdout_bytes"`
		} `json:"execution_receipt"`
	}
	if json.Unmarshal([]byte(call.ResultJSON), &payload) != nil {
		return false
	}
	r := payload.Receipt
	return r.RunID != "" && r.RunID == evidence.VerificationRunID && r.InputDigest == evidence.VerificationInputDigest && r.Status == "success" && r.ExitCode == 0 && r.Error == "" && !r.Timeout && !r.RuntimeMissing && !r.Truncated && !r.StdoutTruncated && !r.StderrTruncated && r.StdoutBytes > 0 && int64(len(r.Stdout)) == r.StdoutBytes
}

func (d Deps) restoreTextSolveResult(ctx context.Context, f k12.TextSolveFinalization) (SolveHomeworkResult, error) {
	if f.ResultDigest != modelInvocationDigest([]byte(f.ResultJSON)) {
		return SolveHomeworkResult{}, k12storage.ErrTextSolveConflict
	}
	// 已完成任务只核对原冻结来源；资产更新或归档仅影响未来首次采用提交。
	if _, err := d.validateTextSolveFinalization(ctx, f.OwnerID, f.AgentName, f.RunID, false); err != nil {
		return SolveHomeworkResult{}, err
	}
	var solved SolveHomeworkResult
	if json.Unmarshal([]byte(f.ResultJSON), &solved) != nil {
		return solved, k12storage.ErrTextSolveConflict
	}
	solved.Finalization = &f
	return solved, nil
}

func (d Deps) GetTextSolveFinalization(ctx context.Context, owner, agent, run string) (k12.TextSolveFinalization, error) {
	return d.Records.GetTextSolveFinalization(ctx, owner, agent, run)
}

// ValidateTextSolveFinalization 供新报告等消费者在采用原答案前复核身份与当前资产资格。
func (d Deps) ValidateTextSolveFinalization(ctx context.Context, owner, agent, run string) (k12.TextSolveFinalization, error) {
	return d.validateTextSolveFinalization(ctx, owner, agent, run, true)
}

// validateTextSolveFinalization 保留原资格复核；历史只读投影不追溯撤销已冻结内容。
func (d Deps) validateTextSolveFinalization(ctx context.Context, owner, agent, run string, checkCurrentAdoption bool) (k12.TextSolveFinalization, error) {
	f, err := d.Records.GetTextSolveFinalization(ctx, owner, agent, run)
	if err != nil {
		return f, err
	}
	if f.SourceKind != k12.ProblemAssetSourceTextSolve {
		return f, k12storage.ErrTextSolveConflict
	}
	r, err := d.Records.GetTextSolveRun(ctx, owner, agent, run)
	if err != nil {
		return f, err
	}
	if f.InputDigest != r.InputDigest || f.FactsDigest != r.FactsDigest || f.TextSolveOrigin != r.TextSolveOrigin || f.ResultDigest != modelInvocationDigest([]byte(f.ResultJSON)) {
		return f, k12storage.ErrTextSolveConflict
	}
	inv, err := d.Records.GetTextSolveInvocation(ctx, owner, agent, f.InvocationID)
	if err != nil {
		return f, err
	}
	if inv.RunID != run || inv.Status != k12.ModelInvocationSucceeded || inv.ResultJSON != f.ResultJSON || inv.ResultDigest != f.ResultDigest {
		return f, k12storage.ErrTextSolveConflict
	}
	if checkCurrentAdoption && f.AnswerSource != nil && f.AnswerSource.Kind == k12.ProblemAnswerAsset {
		if err = d.Records.ValidateProblemAssetAdoption(ctx, owner, f.AnswerSource.AdoptionID); err != nil {
			return f, err
		}
	}
	if f.Publication != nil {
		if err = d.Records.ValidateTextSolvePublication(ctx, f, *f.Publication); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (d Deps) ResumeTextSolvePublication(ctx context.Context, owner, agent, run string) (*k12.ProblemAssetVersion, error) {
	f, err := d.Records.GetTextSolveFinalization(ctx, owner, agent, run)
	if errors.Is(err, sql.ErrNoRows) {
		f, err = d.RecoverTextSolveFinalization(ctx, owner, agent, run)
	}
	if err != nil {
		return nil, err
	}
	if f.Publication == nil {
		return nil, nil
	}
	v, _, err := d.Records.PublishProblemAsset(ctx, *f.Publication)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// RecoverTextSolveFinalization 仅从已成功保存的完整执行结果恢复本地结算，不调用Solver。
func (d Deps) RecoverTextSolveFinalization(ctx context.Context, owner, agent, runID string) (k12.TextSolveFinalization, error) {
	if f, err := d.Records.GetTextSolveFinalization(ctx, owner, agent, runID); err == nil {
		return f, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return f, err
	}
	run, err := d.Records.GetTextSolveRun(ctx, owner, agent, runID)
	if err != nil {
		return k12.TextSolveFinalization{}, err
	}
	calls, err := d.Records.ListTextSolveInvocations(ctx, owner, agent, runID)
	if err != nil {
		return k12.TextSolveFinalization{}, err
	}
	var source *k12.TextSolveInvocation
	var pending []k12.TextSolveInvocation
	for i := range calls {
		call := &calls[i]
		if call.Status == k12.ModelInvocationSent || call.Status == k12.ModelInvocationOutcomeUnknown {
			pending = append(pending, *call)
		}
		if call.ExecutionKind != k12.GradingExecutionLocalDeterministic || call.Status != k12.ModelInvocationSucceeded || call.ResultDigest != modelInvocationDigest([]byte(call.ResultJSON)) {
			continue
		}
		if call.Operation == textSolveFinalizeOperation {
			source = call
		} else if call.Operation == k12.GradingItemOperationSolve && (source == nil || source.Operation != textSolveFinalizeOperation) {
			source = call
		}
	}
	if source == nil {
		if len(pending) > 0 {
			return k12.TextSolveFinalization{}, fmt.Errorf("%w: solve_run_id=%s invocation=%s", k12storage.ErrTextSolveUnknown, runID, pending[0].InvocationID)
		}
		return k12.TextSolveFinalization{}, sql.ErrNoRows
	}
	var solved SolveHomeworkResult
	if json.Unmarshal([]byte(source.ResultJSON), &solved) != nil {
		return k12.TextSolveFinalization{}, k12storage.ErrTextSolveConflict
	}
	_, degraded := degradedReportParentGuide(solved)
	for _, call := range pending {
		// 只补已持久化的本地降级终稿事务；未知教学回执保留，不再发送。
		if source.Operation != textSolveFinalizeOperation || !degraded || call.Operation != k12.GradingItemOperationParentGuide {
			return k12.TextSolveFinalization{}, fmt.Errorf("%w: solve_run_id=%s invocation=%s", k12storage.ErrTextSolveUnknown, runID, call.InvocationID)
		}
	}
	result, err := d.FinalizeTextSolve(ctx, run, solved)
	if err != nil {
		return k12.TextSolveFinalization{}, err
	}
	if result.Finalization == nil {
		return k12.TextSolveFinalization{}, k12storage.ErrTextSolveConflict
	}
	return *result.Finalization, nil
}
