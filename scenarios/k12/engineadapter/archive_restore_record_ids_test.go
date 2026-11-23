package engineadapter

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestRestoreAsSameDatabaseKeepsTombstonesAndWorkReceipts(t *testing.T) {
	f := newArchiveRestoreFixture(t)
	registerRestoreTarget(t, f)
	ctx := t.Context()
	const source, target = "mingming", "target-child"
	accID := seedAccumulationArchiveV10(t, f, source, "原文", "来源")
	if _, err := f.records.TombstoneCurrentObject(ctx, source, "accumulation", accID, 1, "remove-old"); err != nil {
		t.Fatal(err)
	}
	work, err := k12.NewCreativeWorkRecord(source, "", k12.CreativeWorkFields{WorkType: k12.WorkTypeWriting, DisplayName: "当前作品"})
	if err != nil {
		t.Fatal(err)
	}
	generation, _, err := f.records.CreateCreativeWorkWithInitialGeneration(ctx, work, "create-essay", "essay-source", k12.CreativeWorkSourceSnapshot{WorkType: k12.WorkTypeWriting, ContentMarkdown: "保留原稿"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.records.PrepareImageTaskInvocation(ctx, k12.ImageTaskInvocation{
		InvocationID: "writing-invocation", AgentName: source, WorkRecordID: work.RecordID,
		Operation: k12.ImageTaskOperationWorkFeedback, OperationKey: "work:" + work.RecordID + ":version:" + generation.GenerationID + ":feedback",
		RequestDigest: "frozen-request", Attempt: 1,
		RouteSnapshot: k12.ImageTaskRouteSnapshot{Provider: "fixture", Model: "model", Route: "fixture/model",
			Capability: "text", SelectionSource: "explicit", PolicyVersion: "image-task-routing-v1", PromptVersion: "work-feedback-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := usecase.Deps{Records: f.records, ArchiveMigrator: f.restore}
	sourceArchive, err := d.Backup(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.RestoreAs(ctx, usecase.RestoreAsRequest{Archive: sourceArchive, SourceAgent: source, TargetAgent: target, GuardianConfirmed: true, IdempotencyKey: "copy-current-objects"})
	if err != nil {
		t.Fatal(err)
	}
	targetArchive, err := d.Backup(ctx, target)
	if err != nil || len(targetArchive.CurrentCreativeWorks) != 1 || len(targetArchive.CurrentAccumulations) != 1 {
		t.Fatalf("target current objects: %+v %v", targetArchive, err)
	}
	moved := targetArchive.CurrentCreativeWorks[0]
	accumulation := targetArchive.CurrentAccumulations[0]
	if moved.WorkID == work.RecordID || len(moved.Invocations) != 1 || moved.Invocations[0].WorkRecordID != moved.WorkID || moved.Invocations[0].OperationKey != "work:"+moved.WorkID+":version:"+moved.InitialID+":feedback" {
		t.Fatalf("work invocation no longer belongs to the migrated work: %+v", moved)
	}
	var receipt k12.CurrentDeleteReceipt
	if accumulation.AccumulationID == accID || accumulation.DeletedAt == nil || json.Unmarshal([]byte(accumulation.DeleteReceiptJSON), &receipt) != nil || receipt.ObjectID != accumulation.AccumulationID || !receipt.Deleted {
		t.Fatalf("migrated tombstone is not owned by the target: %+v", accumulation)
	}
}

func TestRestoreAsSameDatabasePreservesPracticeSourceAndLaterEdits(t *testing.T) {
	f := newArchiveRestoreFixture(t)
	registerRestoreTarget(t, f)
	ctx := t.Context()
	source, target := "mingming", "target-child"
	d := usecase.Deps{Records: f.records, ArchiveMigrator: f.restore}
	sourceRecords, err := f.records.ExportAgent(ctx, source)
	if err != nil || len(sourceRecords) == 0 {
		t.Fatalf("source records: %v %v", sourceRecords, err)
	}
	var mistakeID string
	for _, record := range sourceRecords {
		if record.Collection == k12.CollectionMistakes {
			mistakeID = record.RecordID
			break
		}
	}
	if mistakeID == "" {
		t.Fatal("missing source mistake fixture")
	}
	setID, _, err := d.AddToBasket(ctx, source, "archive-session", k12.PracticeItem{
		SourceProblemID: mistakeID, ItemID: "worksheet-item", Subject: "数学",
		QuestionMarkdown: "题干保留原文 " + mistakeID, ExpectedAnswerMarkdown: "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := d.Backup(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	cmd := usecase.RestoreAsRequest{Archive: archive, SourceAgent: source, TargetAgent: target, GuardianConfirmed: true, IdempotencyKey: "copy-path"}
	result, err := d.RestoreAs(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := f.records.ExportAgent(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	var targetMistake, targetSet *records.AgentRecord
	for _, rec := range moved {
		switch rec.Collection {
		case k12.CollectionMistakes:
			targetMistake = rec
		case k12.CollectionPracticeSet:
			targetSet = rec
		}
	}
	if targetSet == nil || targetMistake == nil || targetSet.RecordID == setID || targetMistake.RecordID == mistakeID {
		t.Fatalf("target identities not independent: %v", moved)
	}
	set, err := d.GetPracticeSet(ctx, target, targetSet.RecordID)
	if err != nil || len(set.Fields.Items) != 1 || set.Fields.Items[0].SourceProblemID != targetMistake.RecordID || set.Fields.Items[0].QuestionMarkdown != "题干保留原文 "+mistakeID {
		t.Fatalf("target links or printed text changed: %+v, %v", set.Fields.Items, err)
	}
	if _, err = d.ArchiveMistake(ctx, target, targetMistake.RecordID, targetMistake.Version, "edit-after-restore"); err != nil {
		t.Fatal(err)
	}
	replayed, err := d.RestoreAs(ctx, cmd)
	if err != nil || replayed.MigrationID != result.MigrationID || !replayed.Idempotent {
		t.Fatalf("replay %+v, %v", replayed, err)
	}
	_, err = d.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{MigrationID: result.MigrationID, TargetAgent: target, GuardianConfirmed: true})
	if !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("rollback after target edit: %v", err)
	}
	original, err := f.records.Get(ctx, mistakeID)
	if err != nil || original.AgentName != source || original.Status == k12.StatusArchived {
		t.Fatalf("source changed: %+v %v", original, err)
	}
}

func TestRestoreAsSameDatabaseRebindsFrozenGradingJob(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	f := newArchiveRestoreFixture(t)
	registerRestoreTarget(t, f)
	ctx := t.Context()
	seedRestorableProblemSourceActionV6(t, f)
	d := usecase.Deps{Records: f.records, ArchiveMigrator: f.restore}
	archive, err := d.Backup(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	cmd := usecase.RestoreAsRequest{Archive: archive, SourceAgent: "mingming", TargetAgent: "target-child", GuardianConfirmed: true, IdempotencyKey: "photo-copy"}
	result, err := d.RestoreAs(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := f.records.ExportProblemSourceArchiveV6(ctx, cmd.TargetAgent)
	if err != nil || len(closure.HomeworkSubmissions) != 1 {
		t.Fatalf("target source closure: %v %v", closure, err)
	}
	jobID := closure.HomeworkSubmissions[0].GradingJobID
	if jobID == "" || jobID == archive.ProblemSource.HomeworkSubmissions[0].GradingJobID {
		t.Fatal("target homework refers to the source grading job")
	}
	job, err := f.records.Get(ctx, jobID)
	if err != nil || job.AgentName != cmd.TargetAgent || job.Collection != k12.CollectionGradingJob {
		t.Fatalf("target grading job %+v: %v", job, err)
	}
	if _, err = d.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{MigrationID: result.MigrationID, TargetAgent: cmd.TargetAgent, GuardianConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	original, err := f.records.ExportProblemSourceArchiveV6(ctx, cmd.SourceAgent)
	if err != nil || len(original.HomeworkSubmissions) != 1 || original.HomeworkSubmissions[0].GradingJobID != archive.ProblemSource.HomeworkSubmissions[0].GradingJobID {
		t.Fatal("rollback changed source homework")
	}
}
