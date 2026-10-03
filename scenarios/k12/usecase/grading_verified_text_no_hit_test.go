package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 来源封套经过真实存储后仍可读取，避免跨层摘要协议不一致阻断原任务。
func TestVerifiedTextSourceSnapshotReadsStoredDigest(t *testing.T) {
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
	ctx := context.Background()
	snapshot := verifiedTextNoHitSnapshot()
	frozen, err := freezeGradingGroundingSource(ctx, o.deps, snapshot.OwnerID, snapshot.AgentName, "source-digest-job", snapshot)
	if err != nil {
		t.Fatalf("read committed source snapshot: %v", err)
	}
	if frozen.DocumentID != snapshot.DocumentID || frozen.SourceMode != GroundingSourceModeVerifiedText {
		t.Fatalf("source identity changed: %+v", frozen)
	}
	rows, err := o.deps.Records.ListGroundingRetrievalInvocations(ctx, snapshot.OwnerID, snapshot.AgentName, "source-digest-job")
	if err != nil || len(rows) != 1 {
		t.Fatalf("source records: %d err=%v", len(rows), err)
	}
	rows[0].ResultJSON = strings.Replace(rows[0].ResultJSON, `"document"`, `"changed-document"`, 1)
	if _, err := decodeGroundingSource(rows[0]); err == nil {
		t.Fatal("changed persisted source was accepted")
	}
}

// 本地正文读取错误保留明确未发送回执，重启后不能变成未知或重新读取。
func TestVerifiedTextReadFailureRemainsNotSentAfterSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
	snapshot := verifiedTextNoHitSnapshot()
	q := RecognizedQuestion{ProblemID: "read-failure-problem", Question: "57+38=", Subject: "数学"}
	req := GradeRequest{Subject: "数学", Grade: "五年级上"}
	claim := groundingRetrievalClaim(snapshot.OwnerID, snapshot.AgentName, "read-failure-job", q, "", snapshot, gradingItemGroundingQuery(q, req))
	invocation, err := o.deps.Records.ClaimGroundingRetrievalInvocation(ctx, claim)
	if err != nil || !invocation.Fresh || invocation.Operation != "k12_grounding_verified_text" {
		t.Fatalf("claim local query: %+v err=%v", invocation, err)
	}
	session := &gradingGroundingSession{retrieval: o.deps.Records}
	err = session.recordQueryFailure(ctx, invocation, errors.New("verified textbook read failed"))
	if !errors.Is(err, ErrGradingGroundingUnavailable) || errors.Is(err, ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("local read failure became an unknown external outcome: %v", err)
	}
	assertFailed := func(store *k12storage.Store) {
		t.Helper()
		row, err := store.ClaimGroundingRetrievalInvocation(ctx, claim)
		if err != nil || row.Fresh || row.InvocationID != invocation.InvocationID || row.Status != k12storage.GroundingRetrievalInvocationStatusFailed {
			t.Fatalf("failed local query was reclaimed for execution: %+v err=%v", row, err)
		}
		var failure k12storage.GroundingRetrievalFailure
		if err := json.Unmarshal([]byte(row.ResultJSON), &failure); err != nil || failure.Kind != "not_sent" || failure.StatusCode != 0 {
			t.Fatalf("failure receipt lost not-sent classification: %+v err=%v", failure, err)
		}
		rows, err := store.ListGroundingRetrievalInvocations(ctx, snapshot.OwnerID, snapshot.AgentName, claim.JobID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("local failure replay added a query: count=%d err=%v", len(rows), err)
		}
	}
	assertFailed(o.deps.Records)
	var databasePath string
	if err := o.deps.Records.DB().QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil || databasePath == "" {
		t.Fatalf("locate persistent SQLite fixture: path=%q err=%v", databasePath, err)
	}
	if err := o.deps.Records.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store := k12storage.NewStore(db, nil)
	assertFailed(store)
	grounding := &gradingItemPinnedGrounding{noHit: true}
	restarted := &gradingGroundingSession{
		required: true, snapshot: snapshot, evidenceSource: grounding, retrieval: store,
		ownerID: snapshot.OwnerID, agentName: snapshot.AgentName, jobID: claim.JobID,
		items: make(map[string]*gradingGroundingItemState),
	}
	if _, err := restarted.resolveItem(ctx, q, req); !errors.Is(err, ErrGradingGroundingUnavailable) || errors.Is(err, ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("restarted local failure changed error contract: %v", err)
	}
	_, legacyCalls, queries := grounding.snapshot()
	if legacyCalls != 0 || len(queries) != 0 {
		t.Fatalf("failed local query executed again: legacy=%d reads=%d", legacyCalls, len(queries))
	}
	assertFailed(store)
}

func verifiedTextNoHitSnapshot() GroundingSnapshot {
	return GroundingSnapshot{
		OwnerID: "desktop-user", AgentName: "mingming", LearnerID: "learner",
		Subject: "数学", SourceMode: GroundingSourceModeVerifiedText,
		TextbookBindingID: "binding", TextbookManifestID: "manifest", DocumentID: "document",
		DocumentGeneration: 1, SourceDigest: strings.Repeat("a", 64), Edition: "人教版", Volume: "上册",
		SegmentRefs: []string{"segment-1"},
		PageRefs: []k12.TextbookGroundingPageRef{{
			LogicalPage: 1, PDFPage: 3, SegmentRefs: []string{"segment-1"},
		}},
	}
}

// 本地正文读取成功但无相关命中时，保存真实无依据事实，并复用独立求解和批改的回执。
func TestVerifiedTextNoHitGroundingReusesSuccessfulReceiptsAfterSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	grounding := &gradingItemPinnedGrounding{noHit: true}
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{questions: []RecognizedQuestion{{
		Question: "57+38=", Subject: "数学", StudentAnswer: "95",
		AnswerState: AnswerStatePresent, KnowledgePoints: []string{"两位数加法"},
	}}}, nil, WithGradingRunDir(t.TempDir()))
	jobID := runItemResumeJobToAssessing(t, o, "verified-text-no-hit")
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	q := run.questions[0]
	if len(run.anchored) != 0 {
		q = run.anchored[0]
	}
	req := GradeRequest{Subject: "数学", Grade: "五年级上", KnowledgePoints: []string{"两位数加法"}}
	snapshot := verifiedTextNoHitSnapshot()
	newSession := func(store *k12storage.Store) *gradingGroundingSession {
		return &gradingGroundingSession{
			required: true, snapshot: snapshot, evidenceSource: grounding, retrieval: store,
			ownerID: snapshot.OwnerID, agentName: "mingming", jobID: jobID,
			items: make(map[string]*gradingGroundingItemState),
		}
	}
	first := newSession(o.deps.Records)
	evidence, err := first.resolveItem(ctx, q, req)
	if err != nil {
		t.Fatalf("resolve verified text no-hit: %v", err)
	}
	if !evidence.noHit || evidence.text != "" || len(evidence.receipts) != 0 || len(evidence.sources) != 0 {
		t.Fatalf("no-hit manufactured textbook evidence: %+v", evidence)
	}
	if _, err := first.resolveItem(ctx, q, req); err != nil {
		t.Fatalf("reuse no-hit within session: %v", err)
	}
	assertQuery := func(store *k12storage.Store) {
		t.Helper()
		var count int
		if err := store.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM k12_grounding_retrieval_invocations WHERE job_id=?`, jobID,
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("retrieval rows=%d want 1: %v", count, err)
		}
		var status, operation, resultJSON string
		if err := store.DB().QueryRowContext(ctx,
			`SELECT status,operation,result_json FROM k12_grounding_retrieval_invocations WHERE job_id=?`, jobID,
		).Scan(&status, &operation, &resultJSON); err != nil {
			t.Fatal(err)
		}
		if status != "succeeded" || operation != "k12_grounding_verified_text" {
			t.Fatalf("no-hit query status=%s operation=%s", status, operation)
		}
		var result GroundingSnapshotResult
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			t.Fatal(err)
		}
		if result.Found || result.Text != "" || len(result.Receipts) != 0 || len(result.Sources) != 0 {
			t.Fatalf("persisted no-hit contains evidence: %+v", result)
		}
		_, legacyCalls, queries := grounding.snapshot()
		if legacyCalls != 0 || len(queries) != 1 {
			t.Fatalf("retrieval was repeated or bypassed: legacy=%d reads=%d", legacyCalls, len(queries))
		}
	}
	assertQuery(o.deps.Records)

	providerCalls := 0
	operations := []k12.GradingItemOperation{k12.GradingItemOperationSolveVerify, k12.GradingItemOperationGrade}
	execute := func(current *GradingOrchestrator, currentEvidence gradingProviderGrounding) map[k12.GradingItemOperation]GradingPhysicalCallResult {
		t.Helper()
		callCtx := withGradingProviderGrounding(ctx, currentEvidence)
		callCtx = WithGradingPhysicalCallExecutor(callCtx, newDurableGradingPhysicalCallExecutor(current, job, q))
		results := make(map[k12.GradingItemOperation]GradingPhysicalCallResult)
		for _, operation := range operations {
			result, err := ExecuteGradingPhysicalCall(callCtx, GradingPhysicalCallSpec{
				Operation: operation, RequestDigest: modelInvocationDigest([]byte(operation), []byte(q.Question)),
			}, func(providerCtx context.Context) (string, error) {
				providerCalls++
				if text, ok := GradingGroundingForProvider(providerCtx); ok || text != "" {
					t.Fatalf("no-hit injected textbook into Provider: ok=%v text=%q", ok, text)
				}
				return `{"answer":"95"}`, nil
			})
			if err != nil {
				t.Fatalf("execute %s with no-hit provenance: %v", operation, err)
			}
			if result.Payload != `{"answer":"95"}` || result.InvocationID == "" {
				t.Fatalf("physical result lost payload or receipt: %+v", result)
			}
			results[operation] = result
		}
		return results
	}
	firstResults := execute(o, evidence)
	if providerCalls != 2 {
		t.Fatalf("first independent solve/grade calls=%d want 2", providerCalls)
	}

	var databasePath string
	if err := o.deps.Records.DB().QueryRowContext(ctx,
		`SELECT file FROM pragma_database_list WHERE name='main'`,
	).Scan(&databasePath); err != nil || databasePath == "" {
		t.Fatalf("locate persistent SQLite fixture: path=%q err=%v", databasePath, err)
	}
	if err := o.deps.Records.DB().Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := sql.Open("sqlite", databasePath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	reopenedDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = reopenedDB.Close() })
	restartedDeps := o.deps
	restartedDeps.Records = k12storage.NewStore(reopenedDB, nil)
	restarted := &GradingOrchestrator{deps: restartedDeps}
	replayedEvidence, err := newSession(restartedDeps.Records).resolveItem(ctx, q, req)
	if err != nil {
		t.Fatalf("reuse persisted no-hit after SQLite restart: %v", err)
	}
	if !replayedEvidence.noHit || replayedEvidence.identityDigest != evidence.identityDigest {
		t.Fatalf("no-hit provenance changed after restart: %+v", replayedEvidence)
	}
	replayedResults := execute(restarted, replayedEvidence)
	if providerCalls != 2 {
		t.Fatalf("restart resent successful physical calls: calls=%d want 2", providerCalls)
	}
	for _, operation := range operations {
		if replayedResults[operation] != firstResults[operation] {
			t.Fatalf("%s replay changed receipt: first=%+v replay=%+v", operation, firstResults[operation], replayedResults[operation])
		}
	}
	assertQuery(restartedDeps.Records)
	invocations, err := restartedDeps.Records.ListGradingItemInvocations(ctx, "mingming", jobID)
	if err != nil || len(invocations) != 2 {
		t.Fatalf("persisted physical calls=%d want 2: %v", len(invocations), err)
	}
	for _, invocation := range invocations {
		var envelope gradingGroundedPhysicalEnvelope
		if err := json.Unmarshal([]byte(invocation.ResultJSON), &envelope); err != nil {
			t.Fatal(err)
		}
		if invocation.Status != k12.ModelInvocationSucceeded || envelope.Schema != gradingGroundedPhysicalSchema ||
			!envelope.Grounding.NoHit || envelope.Grounding.Snapshot.SourceMode != GroundingSourceModeVerifiedText ||
			envelope.Grounding.Text != "" || len(envelope.Grounding.Receipts) != 0 || len(envelope.Grounding.Sources) != 0 {
			t.Fatalf("physical envelope lost honest no-hit provenance: %+v", envelope)
		}
	}
}

// 无命中只允许编排内部的本地正文路径；语义路径与公开教材命中入口不放宽。
func TestVerifiedTextNoHitKeepsSemanticAndPublicEvidenceContracts(t *testing.T) {
	for _, mode := range []string{"", GroundingSourceModeSemantic} {
		t.Run("semantic_mode_"+mode, func(t *testing.T) {
			snapshot := verifiedTextNoHitSnapshot()
			snapshot.SourceMode = mode
			snapshot.VectorRevisionID = "revision-a"
			if _, err := newGradingProviderGrounding(snapshot, GroundingSnapshotResult{}); !errors.Is(err, ErrGradingGroundingUnavailable) {
				t.Fatalf("semantic no-hit accepted: %v", err)
			}
		})
	}
	if _, err := WithVerifiedGradingGrounding(context.Background(), verifiedTextNoHitSnapshot(), GroundingSnapshotResult{}); !errors.Is(err, ErrGradingGroundingUnavailable) {
		t.Fatalf("public verified-evidence entry accepted no-hit: %v", err)
	}
}
