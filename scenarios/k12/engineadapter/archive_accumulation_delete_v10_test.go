package engineadapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func assertRestoredAccumulationDeletion(t *testing.T, fixture *archiveRestoreFixture, agent, id, commandKey string, receipt k12.CurrentDeleteReceipt) {
	t.Helper()
	ctx := context.Background()
	if _, err := (usecase.Deps{Records: fixture.records}).GetAccumulation(ctx, agent, id); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("restored deletion is visible: %v", err)
	}
	listed, err := fixture.records.ListByScope(ctx, agent, k12.CollectionAccumulation, "")
	if err != nil {
		t.Fatal(err)
	}
	due, err := fixture.records.ListDue(ctx, agent, k12.CollectionAccumulation, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range [][]*records.AgentRecord{listed, due} {
		for _, record := range group {
			if record.RecordID == id {
				t.Fatal("restored deletion entered the visible list or due queue")
			}
		}
	}
	replayed, err := fixture.records.TombstoneCurrentObject(ctx, agent, "accumulation", id, 1, commandKey)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("same delete command changed its receipt: got=%+v want=%+v err=%v", replayed, receipt, err)
	}
	if _, err := fixture.records.TombstoneCurrentObject(ctx, agent, "accumulation", id, receipt.RowVersion, "different-delete-command"); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("deleted object accepted a different delete command: %v", err)
	}
}

func TestAccumulationArchiveV10OrdinaryRestoreKeepsDeletionCASAndReceipt(t *testing.T) {
	ctx := context.Background()
	source, target := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
	if _, err := target.db.ExecContext(ctx, `DELETE FROM k12_mistakes WHERE record_id=?`, target.oldRecordID); err != nil {
		t.Fatal(err)
	}
	deletedID := seedAccumulationArchiveV10(t, source, "mingming", "床前明月光", "《静夜思》")
	activeID := seedAccumulationArchiveV10(t, source, "mingming", "春眠不觉晓", "《春晓》")
	if _, err := source.db.ExecContext(ctx, `UPDATE k12_accumulations SET due_at=1 WHERE record_id IN (?,?)`, deletedID, activeID); err != nil {
		t.Fatal(err)
	}
	receipt, err := source.records.TombstoneCurrentObject(ctx, "mingming", "accumulation", deletedID, 1, "source-delete")
	if err != nil {
		t.Fatal(err)
	}
	before := currentAccumulationArchiveV10(t, source, "mingming", deletedID)
	archive, err := (usecase.Deps{Records: source.records}).Backup(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: target.records, ArchiveRestorer: target.restore}
	if _, err := deps.Restore(ctx, archive); err != nil {
		t.Fatal(err)
	}
	assertRestoredAccumulationDeletion(t, target, "mingming", deletedID, "source-delete", receipt)
	if after := currentAccumulationArchiveV10(t, target, "mingming", deletedID); !reflect.DeepEqual(before, after) {
		t.Fatal("ordinary restore changed the tombstone or its current provenance")
	}
	active, err := deps.GetAccumulation(ctx, "mingming", activeID)
	if err != nil || active.Fields.Source != "《春晓》" || active.RowVersion != 1 {
		t.Fatalf("active accumulation changed: item=%+v err=%v", active, err)
	}
	if _, err := target.records.TombstoneCurrentObject(ctx, "mingming", "accumulation", activeID, 2, "active-delete"); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("stale CAS version was accepted: %v", err)
	}
	if _, err := target.records.TombstoneCurrentObject(ctx, "mingming", "accumulation", activeID, 1, "active-delete"); err != nil {
		t.Fatalf("restored current CAS version cannot delete: %v", err)
	}
	// 旧包缺少删除事实，不得覆盖目标已持有的真实删除状态及回执。
	archive.Version, archive.CurrentAccumulations = 9, nil
	if err := usecase.SealHexbak(archive); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Restore(ctx, archive); err != nil {
		t.Fatal(err)
	}
	assertRestoredAccumulationDeletion(t, target, "mingming", deletedID, "source-delete", receipt)
	if after := currentAccumulationArchiveV10(t, target, "mingming", deletedID); !reflect.DeepEqual(before, after) {
		t.Fatal("legacy restore invented or overwrote current deletion facts")
	}
}

func TestAccumulationArchiveV10RestoreAsAndRollbackKeepDeletionFacts(t *testing.T) {
	ctx := context.Background()
	source, target := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
	registerRestoreTarget(t, target)
	deletedID := seedAccumulationArchiveV10(t, source, "mingming", "床前明月光", "《静夜思》")
	activeID := seedAccumulationArchiveV10(t, source, "mingming", "春眠不觉晓", "《春晓》")
	priorID := seedAccumulationArchiveV10(t, target, "target-child", "白日依山尽", "《登鹳雀楼》")
	sourceReceipt, err := source.records.TombstoneCurrentObject(ctx, "mingming", "accumulation", deletedID, 1, "source-delete")
	if err != nil {
		t.Fatal(err)
	}
	priorReceipt, err := target.records.TombstoneCurrentObject(ctx, "target-child", "accumulation", priorID, 1, "prior-delete")
	if err != nil {
		t.Fatal(err)
	}
	before := currentAccumulationArchiveV10(t, target, "target-child", priorID)
	archive, err := (usecase.Deps{Records: source.records}).Backup(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: target.records, ArchiveMigrator: target.restore}
	result, err := deps.RestoreAs(ctx, usecase.RestoreAsRequest{
		Archive: archive, SourceAgent: "mingming", TargetAgent: "target-child",
		GuardianConfirmed: true, IdempotencyKey: "restore-deleted-accumulations",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRestoredAccumulationDeletion(t, target, "target-child", deletedID, "source-delete", sourceReceipt)
	assertRestoredAccumulationDeletion(t, target, "target-child", priorID, "prior-delete", priorReceipt)
	if migrated := currentAccumulationArchiveV10(t, target, "target-child", deletedID); migrated.DeletedBy != "target-child" {
		t.Fatalf("deleted owner was not rewritten: %q", migrated.DeletedBy)
	}
	if _, err := deps.GetAccumulation(ctx, "target-child", activeID); err != nil {
		t.Fatalf("normal accumulation was hidden: %v", err)
	}
	if _, err := deps.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{
		MigrationID: result.MigrationID, TargetAgent: "target-child", GuardianConfirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	assertRestoredAccumulationDeletion(t, target, "target-child", priorID, "prior-delete", priorReceipt)
	if after := currentAccumulationArchiveV10(t, target, "target-child", priorID); !reflect.DeepEqual(before, after) {
		t.Fatal("rollback lost the original deleted accumulation facts")
	}
	all, err := target.records.ExportAgent(ctx, "target-child")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range all {
		if record.RecordID == deletedID || record.RecordID == activeID {
			t.Fatal("rollback retained a migrated accumulation root")
		}
	}
}

func TestAccumulationArchiveV10RollbackDoesNotEraseLaterDeletion(t *testing.T) {
	ctx := context.Background()
	source, target := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
	registerRestoreTarget(t, target)
	id := seedAccumulationArchiveV10(t, source, "mingming", "床前明月光", "《静夜思》")
	archive, err := (usecase.Deps{Records: source.records}).Backup(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: target.records, ArchiveMigrator: target.restore}
	result, err := deps.RestoreAs(ctx, usecase.RestoreAsRequest{
		Archive: archive, SourceAgent: "mingming", TargetAgent: "target-child",
		GuardianConfirmed: true, IdempotencyKey: "later-deletion",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := target.records.TombstoneCurrentObject(ctx, "target-child", "accumulation", id, 1, "later-delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{
		MigrationID: result.MigrationID, TargetAgent: "target-child", GuardianConfirmed: true,
	}); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("rollback erased a later deletion: %v", err)
	}
	assertRestoredAccumulationDeletion(t, target, "target-child", id, "later-delete", receipt)
}
