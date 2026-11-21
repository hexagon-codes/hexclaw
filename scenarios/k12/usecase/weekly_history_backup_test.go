package usecase_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestWeeklyHistoryBackupRetainsPDFAndSettledAttempt(t *testing.T) {
	const agent = "xiaoming"
	ctx := t.Context()
	source := newDataDeps(t, agent)
	clock := &mutableWeeklyClock{now: 1785081600}
	source.Now = func() int64 { return clock.now }
	configureWeeklyBundle(t, &source, false)
	seedInvariantDueMistake(t, source, clock.now-1)
	plan, _, err := source.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: agent, IdempotencyKey: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	source.Renderer = invariantRenderer{}
	prepared, err := source.PrepareWeeklyPracticeOutput(ctx, agent, plan.PlanID, plan.Revision, "output")
	if err != nil {
		t.Fatal(err)
	}
	itemID := prepared.Snapshot.Tracks[0].Items[0].ItemID
	digest := k12.WeeklyAssessmentRequestDigest(prepared.Snapshot.SnapshotID, itemID, "7")
	attempt := k12.WeeklyPracticeAttempt{AttemptID: "completed-attempt", SnapshotID: prepared.Snapshot.SnapshotID, ItemID: itemID,
		AssessmentID: "assessed", Result: k12.WeeklyAttemptCorrect, VerificationEvidence: "independent calculation", CreatedAt: clock.now}
	if _, _, err := source.Records.PutWeeklyPracticeAttempt(ctx, agent, itemID, "answer", digest, attempt); err != nil {
		t.Fatal(err)
	}
	clock.now += 8 * 86400
	firstBackup, err := source.Backup(ctx, agent)
	if err != nil || len(firstBackup.WeeklyHistory) != 1 {
		t.Fatalf("prior paper missing before first history read: %v", err)
	}
	live, err := source.Records.GetWeeklyPracticePlan(ctx, agent, plan.PlanID)
	if err != nil || live.Status != k12.WeeklyPlanFrozen {
		t.Fatalf("backup changed source plan: %+v %v", live, err)
	}
	if _, err := source.GetCurrentWeeklyPracticePlan(ctx, agent); err != nil {
		t.Fatal(err)
	}
	later, _, err := source.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: agent, IdempotencyKey: "later-week"})
	if err != nil {
		t.Fatal(err)
	}
	// 旧式独立冻结可以先有快照，周界流转到 archived 时还没有实际发布的 PDF。
	if _, _, err = source.Records.FreezeWeeklyPracticeSnapshot(ctx, k12.WeeklyPracticeSnapshot{
		AgentName: agent, SnapshotID: "unpublished", PlanID: later.PlanID, PlanRevision: later.Revision,
		Tracks: later.Tracks, AnswerKeys: later.AnswerKeys, SnapshotDigest: strings.Repeat("b", 64), CreatedAt: clock.now,
	}); err != nil {
		t.Fatal(err)
	}
	clock.now += 8 * 86400
	if _, err := source.GetCurrentWeeklyPracticePlan(ctx, agent); err != nil {
		t.Fatal(err)
	}
	backup, err := source.Backup(ctx, agent)
	if err != nil || len(backup.WeeklyHistory) != 1 {
		t.Fatalf("weekly backup: %v", err)
	}
	dst := newDataDeps(t, agent)
	tx, err := dst.Records.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := dst.Records.ImportWeeklyHistoryTx(ctx, tx, agent, backup.WeeklyHistory, clock.now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	history, _, err := dst.Records.ListWeeklyPracticeHistory(ctx, agent, "", 20)
	if err != nil || len(history) != 1 || history[0].CorrectCount != 1 {
		t.Fatalf("weekly history: %+v %v", history, err)
	}
	pdf, err := dst.Records.GetPrintArtifactRender(ctx, agent, prepared.Snapshot.ArtifactID)
	if err != nil || !bytes.Equal(pdf.Payload, []byte("%PDF-1.7\nweekly-invariant")) {
		t.Fatalf("original PDF: %v", err)
	}
	command := k12.WeeklyPracticeAssessmentCommand{CommandID: "replay", AgentName: agent, SnapshotID: attempt.SnapshotID, ItemID: itemID,
		IdempotencyKey: "answer", RequestDigest: digest, Status: k12.WeeklyAssessmentPrepared, CreatedAt: clock.now, UpdatedAt: clock.now}
	got, created, err := dst.Records.PrepareWeeklyPracticeAssessmentCommand(ctx, command)
	if err != nil || created || got.Status != k12.WeeklyAssessmentCommitted || got.AttemptID != attempt.AttemptID {
		t.Fatalf("completed attempt sent again: %+v %v", got, err)
	}
	command.RequestDigest = strings.Repeat("b", 64)
	if _, _, err = dst.Records.PrepareWeeklyPracticeAssessmentCommand(ctx, command); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("different answer under original command key: %v", err)
	}
	migrated, err := usecase.MigrateHexbakOwner(backup, "other")
	if err != nil {
		t.Fatal(err)
	}
	moved := newDataDeps(t, "other")
	tx, err = moved.Records.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = moved.Records.ImportWeeklyHistoryTx(ctx, tx, "other", migrated.WeeklyHistory, clock.now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	command.AgentName = "other"
	command.SnapshotID = migrated.WeeklyHistory[0].Snapshot.SnapshotID
	command.RequestDigest = k12.WeeklyAssessmentRequestDigest(command.SnapshotID, itemID, "7")
	got, claimed, err := moved.Records.PrepareWeeklyPracticeAnswer(ctx, command, "7")
	if err != nil || claimed || got.Status != k12.WeeklyAssessmentCommitted || got.AttemptID == attempt.AttemptID {
		t.Fatalf("renamed attempt lost its original input binding: %+v %v", got, err)
	}
	command.RequestDigest = k12.WeeklyAssessmentRequestDigest(command.SnapshotID, itemID, "9")
	if _, _, err = moved.Records.PrepareWeeklyPracticeAnswer(ctx, command, "9"); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("renamed attempt accepted a different answer: %v", err)
	}
}
