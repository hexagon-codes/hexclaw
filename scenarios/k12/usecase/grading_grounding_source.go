package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

const gradingGroundingSourceSchema = "k12_grading_grounding_source_v1"

// gradingGroundingSourceRecord 与查询回执共用持久载体，来源变更不改写旧查询。
type gradingGroundingSourceRecord struct {
	Schema               string             `json:"schema"`
	Snapshot             GroundingSnapshot  `json:"snapshot"`
	PreviousSnapshot     *GroundingSnapshot `json:"previous_snapshot,omitempty"`
	SourceInvocationIDs  []string           `json:"source_invocation_ids,omitempty"`
	SourceReceiptsDigest string             `json:"source_receipts_digest,omitempty"`
	RequestDigest        string             `json:"request_digest,omitempty"`
}

func groundingSnapshotDigest(snapshot GroundingSnapshot) string {
	raw, _ := json.Marshal(snapshot)
	return strings.TrimPrefix(modelInvocationDigest(raw), "sha256:")
}

func groundingSourceClaim(owner, agent, job, operation, queryDigest string, snapshot GroundingSnapshot) k12storage.GroundingRetrievalInvocationClaim {
	claim := groundingRetrievalClaim(owner, agent, job, RecognizedQuestion{ProblemID: "textbook-source"}, "", snapshot, operation)
	claim.Operation, claim.QueryDigest = operation, queryDigest
	claim.RevisionID = ""
	return claim
}

func decodeGroundingSource(inv k12storage.GroundingRetrievalInvocation) (gradingGroundingSourceRecord, error) {
	var state gradingGroundingSourceRecord
	resultDigest := sha256.Sum256([]byte(inv.ResultJSON))
	if inv.Status != k12storage.GroundingRetrievalInvocationStatusSucceeded ||
		json.Unmarshal([]byte(inv.ResultJSON), &state) != nil || state.Schema != gradingGroundingSourceSchema ||
		hex.EncodeToString(resultDigest[:]) != inv.QueryReceiptDigest ||
		validateGradingGroundingSnapshot(state.Snapshot) != nil ||
		groundingSnapshotDigest(state.Snapshot) != inv.GroundingSnapshotDigest ||
		state.Snapshot.OwnerID != inv.OwnerID || state.Snapshot.AgentName != inv.AgentName ||
		state.Snapshot.DocumentID != inv.DocumentID || state.Snapshot.DocumentGeneration != inv.DocumentGeneration {
		return state, fmt.Errorf("%w: frozen grounding source is invalid", ErrModelInvocationRequiresReconciliation)
	}
	return state, nil
}

func loadGradingGroundingSource(ctx context.Context, deps Deps, owner, agent, job string) (GroundingSnapshot, bool, []k12storage.GroundingRetrievalInvocation, error) {
	var zero GroundingSnapshot
	if deps.Records == nil || owner == "" {
		return zero, false, nil, nil
	}
	rows, err := deps.Records.ListGroundingRetrievalInvocations(ctx, owner, agent, job)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return zero, false, nil, nil
		}
		return zero, false, nil, err
	}
	var source *k12storage.GroundingRetrievalInvocation
	for i := range rows {
		switch rows[i].Operation {
		case k12storage.GroundingSourceRecoveryOperation:
			source = &rows[i]
		case k12storage.GroundingSourceSnapshotOperation:
			if source == nil {
				source = &rows[i]
			}
		}
	}
	if source == nil {
		return zero, false, rows, nil
	}
	state, err := decodeGroundingSource(*source)
	return state.Snapshot, err == nil, rows, err
}

// legacyGroundingSnapshot 只在旧摘要可由原范围精确复现时恢复旧快照。
func legacyGroundingSnapshot(requested GroundingSnapshot, rows []k12storage.GroundingRetrievalInvocation) (GroundingSnapshot, bool, error) {
	var candidate GroundingSnapshot
	found := false
	for _, row := range rows {
		if row.Operation != "k12_grounding_retrieval" {
			continue
		}
		current := cloneGradingGroundingSnapshot(requested)
		current.OwnerID, current.SourceMode, current.VectorRevisionID = "", "", row.RevisionID
		if groundingSnapshotDigest(current) != row.GroundingSnapshotDigest ||
			row.DocumentID != current.DocumentID || row.DocumentGeneration != current.DocumentGeneration ||
			validateGradingGroundingSnapshot(current) != nil || found && !reflect.DeepEqual(current, candidate) {
			return GroundingSnapshot{}, false, fmt.Errorf("%w: original grounding snapshot cannot be reconstructed", ErrModelInvocationRequiresReconciliation)
		}
		candidate, found = current, true
	}
	return candidate, found, nil
}

func freezeGradingGroundingSource(ctx context.Context, deps Deps, owner, agent, job string, snapshot GroundingSnapshot) (GroundingSnapshot, error) {
	state := gradingGroundingSourceRecord{Schema: gradingGroundingSourceSchema, Snapshot: snapshot}
	raw, err := json.Marshal(state)
	if err != nil {
		return GroundingSnapshot{}, err
	}
	claim := groundingSourceClaim(owner, agent, job, k12storage.GroundingSourceSnapshotOperation,
		strings.TrimPrefix(modelInvocationDigest([]byte("freeze-textbook-source")), "sha256:"), snapshot)
	stored, _, err := deps.Records.FreezeGroundingSource(ctx, claim, string(raw))
	if err != nil {
		return GroundingSnapshot{}, err
	}
	decoded, err := decodeGroundingSource(stored)
	return decoded.Snapshot, err
}

type GroundingSourceRecoveryInput struct {
	Agent          string `json:"agent"`
	Version        int    `json:"version"`
	JobVersion     int    `json:"job_version"`
	IdempotencyKey string `json:"idempotency_key"`
}

type GroundingSourceRecoveryResult struct {
	JobID      string `json:"job_id"`
	RecoveryID string `json:"recovery_id"`
	SourceMode string `json:"source_mode"`
	Replayed   bool   `json:"replayed"`
}

// AuthorizeGroundingSourceRecovery 只切换原任务的教材来源，成功模型步骤与未知请求均保留。
func (o *GradingOrchestrator) AuthorizeGroundingSourceRecovery(ctx context.Context, dispatchID string, in GroundingSourceRecoveryInput) (GroundingSourceRecoveryResult, error) {
	var zero GroundingSourceRecoveryResult
	in.Agent, in.IdempotencyKey = strings.TrimSpace(in.Agent), strings.TrimSpace(in.IdempotencyKey)
	if o == nil || o.deps.Records == nil || in.Agent == "" || in.IdempotencyKey == "" || in.Version < 1 || in.JobVersion < 1 {
		return zero, fmt.Errorf("%w: grounding recovery identity and versions required", ErrInvalidInput)
	}
	dispatch, err := o.deps.Records.GetImageTaskDispatch(ctx, in.Agent, dispatchID)
	if err != nil {
		return zero, err
	}
	homework, err := o.deps.Records.GetHomeworkSubmission(ctx, in.Agent, dispatch.TargetObjectID)
	if err != nil {
		return zero, err
	}
	lock := o.jobLock(homework.GradingJobID)
	lock.Lock()
	defer lock.Unlock()
	job, err := o.deps.GetGradingJob(ctx, in.Agent, homework.GradingJobID)
	if err != nil {
		return zero, err
	}
	owner, err := resolveGradingGroundingTextbookOwner(ctx, o.deps, job)
	if err != nil {
		return zero, err
	}
	rows, err := o.deps.Records.ListGroundingRetrievalInvocations(ctx, owner, in.Agent, job.Record.RecordID)
	if err != nil {
		return zero, err
	}
	requestRaw, _ := json.Marshal(in)
	requestDigest := strings.TrimPrefix(modelInvocationDigest([]byte(dispatchID), requestRaw), "sha256:")
	for _, row := range rows {
		if row.Operation != k12storage.GroundingSourceRecoveryOperation {
			continue
		}
		state, e := decodeGroundingSource(row)
		if e != nil {
			return zero, e
		}
		if state.RequestDigest != requestDigest {
			return zero, fmt.Errorf("%w: grounding recovery already exists", ErrInvalidInput)
		}
		reconciled, err := o.deps.Records.ReconcileGroundingSourceNotStarted(ctx, owner, in.Agent, job.Record.RecordID, row.InvocationID, job.Record.Version)
		if err != nil {
			return zero, err
		}
		if reconciled {
			gradingGroundingSessions.Delete(gradingGroundingSessionKey{records: o.deps.Records, agentName: in.Agent, jobID: job.Record.RecordID})
		}
		return GroundingSourceRecoveryResult{job.Record.RecordID, row.InvocationID, state.Snapshot.SourceMode, true}, nil
	}
	if job.Record.Version != in.JobVersion || dispatch.Version != in.Version ||
		job.Record.Status != k12.GradingStageFailedRetryable ||
		(job.Fields.FailedStage != k12.GradingStageAssessing &&
			!(job.Fields.FailedStage == k12.GradingStageQueued && job.Fields.FailureKind == "interactive_deadline_exceeded")) ||
		job.Fields.ConfirmationState != k12.GradingConfirmationConfirmed || k12.GradingResumeStage(job.Fields.StageCheckpoints) != k12.GradingStageAssessing {
		return zero, records.ErrVersionConflict
	}
	inspection, err := inspectGradingGroundingInvocations(ctx, o.deps, in.Agent, job.Record.RecordID)
	if err != nil {
		return zero, err
	}
	if inspection.found || inspection.directSucceeded != 0 {
		return zero, fmt.Errorf("%w: existing provider result has a different grounding source", ErrModelInvocationRequiresReconciliation)
	}
	items, err := o.deps.Records.ListGradingItemInvocations(ctx, in.Agent, job.Record.RecordID)
	if err != nil {
		return zero, err
	}
	for _, item := range items {
		if item.Status == k12.ModelInvocationSent || item.Status == k12.ModelInvocationOutcomeUnknown {
			return zero, fmt.Errorf("%w: unresolved model invocation cannot be resent", ErrModelInvocationRequiresReconciliation)
		}
	}
	scope, found, err := o.deps.Records.GetActiveTextbookGroundingScope(ctx, k12storage.TextbookScope{OwnerID: owner, AgentName: in.Agent, Subject: "math"})
	if err != nil {
		return zero, err
	}
	if !found {
		return zero, fmt.Errorf("%w: original verified textbook unavailable", ErrGradingGroundingUnavailable)
	}
	requested := groundingSnapshotFromScope(owner, in.Agent, scope)
	old, found, err := legacyGroundingSnapshot(requested, rows)
	if err != nil {
		return zero, err
	}
	if !found {
		return zero, fmt.Errorf("%w: no original semantic retrieval receipts", ErrInvalidInput)
	}
	reader, ok := o.deps.Grounding.(SnapshotGrounding)
	if !ok {
		return zero, ErrGradingGroundingUnavailable
	}
	requested.SourceMode = GroundingSourceModeVerifiedText
	frozen, err := reader.FreezeGroundingSnapshot(ctx, requested)
	if err != nil {
		return zero, err
	}
	if err := validateFrozenGradingGrounding(requested, frozen); err != nil {
		return zero, err
	}
	if _, err = o.deps.Records.ReadVerifiedTextbookPages(ctx, k12.VerifiedTextbookReadRequest{OwnerID: owner, AgentName: in.Agent, Subject: "math", Scope: scope}); err != nil {
		return zero, err
	}
	state := gradingGroundingSourceRecord{Schema: gradingGroundingSourceSchema, Snapshot: frozen, PreviousSnapshot: &old, RequestDigest: requestDigest}
	for _, row := range rows {
		state.SourceInvocationIDs = append(state.SourceInvocationIDs, row.InvocationID)
	}
	priorRaw, _ := json.Marshal(rows)
	state.SourceReceiptsDigest = modelInvocationDigest(priorRaw)
	payload, _ := json.Marshal(state)
	next := job.Fields
	next.FailureKind, next.FailedStage, next.Retryable = "", "", false
	now := o.deps.now()
	next.ParentAutomaticDeadlineAt = now + int64(dispatch.AutomaticBudgetSeconds)
	next.ParentAutomaticRemainingSeconds = int64(dispatch.AutomaticBudgetSeconds)
	if err := o.deps.setGradingDeadline(ctx, in.Agent, &next, k12.GradingStageAssessing); err != nil {
		return zero, err
	}
	if next.Deadline <= now {
		return zero, fmt.Errorf("%w: grounding recovery has no remaining budget", ErrInvalidInput)
	}
	next.ParentAutomaticDeadlineAt, next.ParentAutomaticRemainingSeconds = next.Deadline, next.Deadline-now
	claim := groundingSourceClaim(owner, in.Agent, job.Record.RecordID, k12storage.GroundingSourceRecoveryOperation, requestDigest, frozen)
	stored, created, err := o.deps.Records.AuthorizeGroundingSourceRecovery(ctx, claim, string(payload), rows, dispatchID, in.Version, in.JobVersion, next)
	if err != nil {
		return zero, err
	}
	gradingGroundingSessions.Delete(gradingGroundingSessionKey{records: o.deps.Records, agentName: in.Agent, jobID: job.Record.RecordID})
	return GroundingSourceRecoveryResult{job.Record.RecordID, stored.InvocationID, frozen.SourceMode, !created}, nil
}

func groundingSnapshotFromScope(owner, agent string, scope k12.TextbookGroundingScope) GroundingSnapshot {
	return cloneGradingGroundingSnapshot(GroundingSnapshot{OwnerID: owner, AgentName: agent, LearnerID: agent, Subject: "数学",
		TextbookBindingID: scope.TextbookBindingID, TextbookManifestID: scope.TextbookManifestID,
		DocumentID: scope.DocumentID, DocumentGeneration: scope.DocumentGeneration, SourceDigest: scope.SourceDigest,
		Edition: scope.Edition, Volume: scope.Volume, SegmentRefs: scope.SegmentRefs, PageRefs: scope.PageRefs})
}
