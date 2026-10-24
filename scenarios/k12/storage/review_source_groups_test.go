package k12storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

// 来源通过实际存储提交路径建立；模型结果是本地合同夹具，不执行模型或投递。
func seedReviewedSource(t *testing.T, store *k12storage.Store, key, question, pageHex string) k12.ReviewSourceGroupMember {
	t.Helper()
	ctx := context.Background()
	page, _, err := store.PreparePageAsset(ctx, pageAssetMetadata("guardian", "mingming", pageHex))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkPageAssetReady(ctx, "guardian", "mingming", page.PageAssetID); err != nil {
		t.Fatal(err)
	}
	problem := k12.Problem{ProblemID: "problem-" + key, AgentName: "mingming", SubmissionID: "submission-" + key,
		PageAssetID: page.PageAssetID, ProblemKind: k12.ProblemKindStandalone, Subject: "数学", StemRaw: question,
		StemMarkdown: question, CanonicalVersion: 1, SourceNumberPath: []string{"1"}, DisplayLabel: "1", CreatedAt: 100, UpdatedAt: 100}
	attempt := k12.Attempt{AttemptID: "attempt-" + key, AgentName: "mingming", SubmissionID: problem.SubmissionID,
		ProblemID: problem.ProblemID, AnswerState: "present", AnswerRaw: "1", AnswerMarkdown: "1", ConfirmedVersion: 1,
		InputDigest: k12.ReviewSourceTextDigest(key), CreatedAt: 100, UpdatedAt: 100}
	if err := store.PutProblemAttemptSnapshot(ctx, k12.ProblemAttemptSnapshot{Problems: []k12.Problem{problem}, Attempts: []k12.Attempt{attempt}}); err != nil {
		t.Fatal(err)
	}
	job, err := k12.NewGradingJobRecord("mingming", "session-"+key, k12.GradingJobFields{
		SubmissionID: problem.SubmissionID, SourceKind: "test", IdempotencyKey: k12.BuildGradingIdempotencyKey("test", key, 0),
		ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending,
		ModelSnapshot: k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	return commitReviewedSource(t, store, job.RecordID, attempt, problem, key, page.ContentDigest)
}

func commitReviewedSource(t *testing.T, store *k12storage.Store, jobID string, attempt k12.Attempt, problem k12.Problem, sourceSession, pageDigest string) k12.ReviewSourceGroupMember {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, 2)
	for _, operation := range []k12.GradingItemOperation{k12.GradingItemOperationSolve, k12.GradingItemOperationGrade} {
		inv := itemInvocation(jobID, attempt, operation, attempt.ConfirmedVersion)
		inv.InvocationID = fmt.Sprintf("review-source-%s-%s-%d", jobID, operation, attempt.ConfirmedVersion)
		prepared, _, err := store.PrepareGradingItemInvocation(ctx, inv)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.MarkGradingItemInvocationSent(ctx, "mingming", prepared.InvocationID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.MarkGradingItemInvocationSucceeded(ctx, "mingming", prepared.InvocationID, "fixture-result", `{"ok":true}`); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, prepared.InvocationID)
	}
	data := map[string]any{"Status": "wrong", "Recognized": map[string]any{
		"problem_id": problem.ProblemID, "attempt_id": attempt.AttemptID, "page_asset_id": problem.PageAssetID,
		"input_digest": attempt.InputDigest, "confirmed_version": attempt.ConfirmedVersion, "canonical_version": problem.CanonicalVersion,
		"source_number_path": problem.SourceNumberPath, "raw_transcription": problem.StemRaw, "canonical_markdown": problem.StemMarkdown,
	}}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	h.Write([]byte{0})
	h.Write(raw)
	receipt := assessmentReceipt(jobID, attempt, ids[0], ids[1])
	receipt.ResultJSON, receipt.ResultDigest = string(raw), "sha256:"+hex.EncodeToString(h.Sum(nil))
	due := int64(500)
	stored, _, err := store.CommitGradingAssessmentItem(ctx, receipt, k12storage.GradingAssessmentEffects{Mistake: &k12storage.GradingMistakeEffect{
		SourceSession: sourceSession, DueAt: &due, Fields: k12.MistakeFields{Subject: "数学", Question: problem.StemMarkdown,
			EntrySource: k12.MistakeEntryPhoto, CanonicalAnswer: "fixture answer", KnowledgePoint: "数量关系"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return k12.ReviewSourceGroupMember{RecordID: stored.ProjectionRecordID, QuestionDigest: k12.ReviewSourceTextDigest(problem.StemMarkdown),
		JobID: stored.JobID, ProblemID: stored.ProblemID, AttemptID: stored.AttemptID, ConfirmedVersion: stored.ConfirmedVersion,
		InputRevision: stored.InputRevision, InputDigest: stored.InputDigest, ResultDigest: stored.ResultDigest,
		PageDigest: pageDigest, SourceNumberPath: problem.SourceNumberPath}
}

func reviewedGroup(members ...k12.ReviewSourceGroupMember) k12.ReviewSourceGroup {
	return k12.ReviewSourceGroup{AgentName: "mingming", CanonicalRecordID: members[0].RecordID, CanonicalStatement: "已用4.8 TB，占总容量2/5。",
		Evidence: k12.ReviewSourceGroupEvidence{Kind: k12.ReviewSourceEvidenceReviewed, Note: "已核对原图、当前问项及完整条件。"}, Members: members, CreatedAt: 300}
}

func TestReviewSourceGroupPersistsIdempotentlyWithoutChangingFactsOrPDF(t *testing.T) {
	store, db := setup(t)
	ctx := context.Background()
	first := seedReviewedSource(t, store, "first", "已用4.8 TB，占总容量2/5。", "a")
	second := seedReviewedSource(t, store, "second", "已用4.8 TB，占总容量2/5。", "b")
	before := map[string]*records.AgentRecord{}
	for _, member := range []k12.ReviewSourceGroupMember{first, second} {
		record, err := store.Get(ctx, member.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		before[member.RecordID] = record
	}
	payload := []byte("%PDF-1.7\nfrozen historical fixture\n%%EOF")
	artifact := k12.PrintArtifact{ArtifactID: "old-review-sheet", AgentName: "mingming", SourceKind: k12.PrintSourcePracticeQuestion,
		SourceRef: "old-sheet", Title: "旧卷", CanonicalMarkdown: "旧卷题面", SourceDigest: strings.Repeat("c", 64), CreatedAt: 200}
	render := k12.PrintArtifactRender{ArtifactID: artifact.ArtifactID, Format: "pdf", RenderContractVersion: "old", ContentType: "application/pdf",
		ByteDigest: k12.ReviewSourceTextDigest(string(payload)), ByteSize: int64(len(payload)), Payload: payload, CreatedAt: 200}
	if _, _, _, err := store.FreezePrintArtifact(ctx, artifact, render); err != nil {
		t.Fatal(err)
	}
	stored, created, err := store.PutReviewSourceGroup(ctx, reviewedGroup(first, second))
	if err != nil || !created {
		t.Fatalf("put reviewed group created=%v err=%v", created, err)
	}
	restarted := k12storage.NewStore(db, nil)
	retry := reviewedGroup(second, first)
	retry.CanonicalRecordID, retry.CreatedAt, retry.Evidence.Note = first.RecordID, 400, "另一条审核说明。"
	retry.CanonicalStatement = "已用 $4.8$ TB，占总容量 $\\frac{2}{5}$。"
	replayed, created, err := restarted.PutReviewSourceGroup(ctx, retry)
	if err != nil || created || !reflect.DeepEqual(stored, replayed) {
		t.Fatalf("derived statement changes rewrote the first frozen group: created=%v err=%v", created, err)
	}
	groups, err := restarted.ListReviewSourceGroups(ctx, "mingming", []string{second.RecordID})
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0], stored) {
		t.Fatalf("restart did not return complete source members: %+v err=%v", groups, err)
	}
	if groups, err := restarted.ListReviewSourceGroups(ctx, "lele", []string{second.RecordID}); err != nil || len(groups) != 0 {
		t.Fatalf("group crossed agent scope: %+v err=%v", groups, err)
	}
	for id, original := range before {
		after, err := store.Get(ctx, id)
		if err != nil || !reflect.DeepEqual(original, after) {
			t.Fatalf("source group changed original record: id=%s err=%v", id, err)
		}
	}
	gotArtifact, err := store.GetPrintArtifact(ctx, "mingming", artifact.ArtifactID)
	if err != nil || !reflect.DeepEqual(artifact, gotArtifact) {
		t.Fatalf("source group changed historical artifact: %v", err)
	}
	gotRender, err := store.GetPrintArtifactRender(ctx, "mingming", artifact.ArtifactID)
	if err != nil || !reflect.DeepEqual(render, gotRender) {
		t.Fatalf("source group changed historical PDF: %v", err)
	}
	if _, err := db.Exec(migrate.K12ReviewSourceGroupsV136.SQL); err != nil {
		t.Fatal(err)
	}
	if groups, err := restarted.ListReviewSourceGroups(ctx, "mingming", []string{first.RecordID}); err != nil || len(groups) != 1 {
		t.Fatalf("idempotent migration changed reviewed source group: %v", err)
	}
}

func TestReviewSourceGroupRejectsActiveOverlapButAllowsRevisedSources(t *testing.T) {
	store, db := setup(t)
	ctx := context.Background()
	question := "已用4.8 TB，占总容量2/5。"
	first := seedReviewedSource(t, store, "first", question, "a")
	second := seedReviewedSource(t, store, "second", question, "b")
	third := seedReviewedSource(t, store, "third", question, "c")
	stored, _, err := store.PutReviewSourceGroup(ctx, reviewedGroup(first, second))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []k12.ReviewSourceGroup{reviewedGroup(second, first), reviewedGroup(second, third)} {
		if _, created, err := store.PutReviewSourceGroup(ctx, candidate); created || !errors.Is(err, k12storage.ErrReviewSourceGroupConflict) {
			t.Fatalf("active source was taken over by another reviewed group: created=%v err=%v", created, err)
		}
	}
	if replay, created, err := store.PutReviewSourceGroup(ctx, stored); err != nil || created || replay.GroupID != stored.GroupID {
		t.Fatalf("exact replay was blocked by its own membership: created=%v err=%v", created, err)
	}
	// 原代表题面改变后旧组已整体失效，另两个未改动的来源可以重新接受显式审核。
	if _, err := db.Exec(`UPDATE k12_mistakes SET question=? WHERE record_id=?`, "已用8.4 TB，占总容量2/5。", first.RecordID); err != nil {
		t.Fatal(err)
	}
	current, created, err := store.PutReviewSourceGroup(ctx, reviewedGroup(second, third))
	if err != nil || !created {
		t.Fatalf("stale historical overlap blocked a new review: created=%v err=%v", created, err)
	}
	groups, err := store.ListReviewSourceGroups(ctx, "mingming", []string{second.RecordID})
	if err != nil || len(groups) != 1 || groups[0].GroupID != current.GroupID {
		t.Fatalf("applicable group selection lost the new review: groups=%+v err=%v", groups, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM k12_review_source_groups`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("new review rewrote old group history: count=%d err=%v", count, err)
	}
}

func TestReviewSourceGroupRejectsChangedConditionsUnitsAndBindings(t *testing.T) {
	store, db := setup(t)
	ctx := context.Background()
	first := seedReviewedSource(t, store, "first", "已用4.8 TB，占总容量2/5。", "a")
	second := seedReviewedSource(t, store, "second", "已用4.8 TB，占总容量2/5。", "b")
	for _, tc := range []struct {
		name string
		edit func(*k12.ReviewSourceGroupMember)
	}{
		{"unit", func(m *k12.ReviewSourceGroupMember) {
			m.QuestionDigest = k12.ReviewSourceTextDigest("已用4.8 GB，占总容量2/5。")
		}},
		{"condition", func(m *k12.ReviewSourceGroupMember) {
			m.QuestionDigest = k12.ReviewSourceTextDigest("已用8.4 TB，占总容量2/5。")
		}},
		{"input", func(m *k12.ReviewSourceGroupMember) { m.InputDigest = "different-input" }},
		{"subproblem", func(m *k12.ReviewSourceGroupMember) { m.SourceNumberPath = []string{"2"} }},
		{"page", func(m *k12.ReviewSourceGroupMember) { m.PageDigest = strings.Repeat("f", 64) }},
		{"attempt", func(m *k12.ReviewSourceGroupMember) { m.AttemptID = first.AttemptID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := second
			tc.edit(&changed)
			if _, _, err := store.PutReviewSourceGroup(ctx, reviewedGroup(first, changed)); !errors.Is(err, k12storage.ErrReviewSourceGroupStale) {
				t.Fatalf("changed source was accepted: %v", err)
			}
		})
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM k12_review_source_groups`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected members left partial groups: count=%d err=%v", count, err)
	}
	group, _, err := store.PutReviewSourceGroup(ctx, reviewedGroup(first, second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE k12_mistakes SET status='explained',due_at=9999,review_stage=2 WHERE record_id=?`, second.RecordID); err != nil {
		t.Fatal(err)
	}
	if groups, err := store.ListReviewSourceGroups(ctx, "mingming", []string{first.RecordID}); err != nil || len(groups) != 1 {
		t.Fatalf("learning progress incorrectly invalidated source identity: %v", err)
	}
	if _, err := db.Exec(`UPDATE k12_mistakes SET question=? WHERE record_id=?`, "已用4.8 GB，占总容量2/5。", second.RecordID); err != nil {
		t.Fatal(err)
	}
	if groups, err := store.ListReviewSourceGroups(ctx, "mingming", []string{first.RecordID}); err != nil || len(groups) != 0 {
		t.Fatalf("changed member did not invalidate the complete group: %v", err)
	}
	if _, _, err := store.PutReviewSourceGroup(ctx, group); !errors.Is(err, k12storage.ErrReviewSourceGroupStale) {
		t.Fatalf("stale group replay was accepted: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM k12_review_source_groups`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("read invalidation deleted the frozen group: count=%d err=%v", count, err)
	}
}

func TestReviewSourceGroupTracksPublishedInputRevisionBeyondOne(t *testing.T) {
	store, db := setup(t)
	ctx := context.Background()
	first := seedReviewedSource(t, store, "first", "已用4.8 TB，占总容量2/5。", "a")
	second := seedReviewedSource(t, store, "second", "已用4.8 TB，占总容量2/5。", "b")
	if _, _, err := store.PutReviewSourceGroup(ctx, reviewedGroup(first, second)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetProblemAttemptSnapshot(ctx, "mingming", "submission-first")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Problems[0].CanonicalVersion, snapshot.Problems[0].UpdatedAt = 2, 300
	snapshot.Attempts[0].ConfirmedVersion, snapshot.Attempts[0].UpdatedAt = 2, 300
	snapshot.Attempts[0].InputDigest = "input-first-v2"
	if err := store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	current := commitReviewedSource(t, store, first.JobID, snapshot.Attempts[0], snapshot.Problems[0], "first", first.PageDigest)
	if current.RecordID != first.RecordID || current.InputRevision != 2 {
		t.Fatalf("fixture did not preserve the original mistake at revision two: %+v", current)
	}
	if groups, err := store.ListReviewSourceGroups(ctx, "mingming", []string{second.RecordID}); err != nil || len(groups) != 0 {
		t.Fatalf("superseded canonical input remained applicable: %v", err)
	}
	currentGroup, created, err := store.PutReviewSourceGroup(ctx, reviewedGroup(current, second))
	if err != nil || !created {
		t.Fatalf("published revision two was not supported: created=%v err=%v", created, err)
	}
	if groups, err := store.ListReviewSourceGroups(ctx, "mingming", []string{second.RecordID}); err != nil || len(groups) != 1 || groups[0].GroupID != currentGroup.GroupID {
		t.Fatalf("current group was not returned independently of stale history: %v", err)
	}
	// 输入版本与发布版本不一致时，已有提交记录也不能继续用于归并。
	if _, err := db.Exec(`UPDATE k12_grading_assessment_items SET published_revision=3 WHERE agent_name=? AND job_id=? AND problem_id=? AND input_revision=2`, "mingming", current.JobID, current.ProblemID); err != nil {
		t.Fatal(err)
	}
	if groups, err := store.ListReviewSourceGroups(ctx, "mingming", []string{second.RecordID}); err != nil || len(groups) != 0 {
		t.Fatalf("unpublished input remained applicable: %v", err)
	}
}
