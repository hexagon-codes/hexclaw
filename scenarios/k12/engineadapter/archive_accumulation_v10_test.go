package engineadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func seedAccumulationArchiveV10(t *testing.T, fixture *archiveRestoreFixture, agent, content, source string) string {
	t.Helper()
	record, err := k12.NewAccumRecord(agent, "", k12.AccumFields{
		GradeTerm: "五年级上", Subject: "语文", EntryType: "古诗积累", Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	confidence := 0.99
	provenance := k12.DerivationProvenance{
		Method: "model", Policy: "controlled-archive", Version: "1", Confidence: &confidence,
		EvidenceRef: "controlled-source",
	}
	metadata := k12.AccumulationDerivedMetadata{
		Subject: "语文", EntryType: "古诗积累", Source: source,
		SubjectProvenance: provenance, EntryTypeProvenance: provenance, SourceProvenance: &provenance,
	}
	created, err := fixture.records.CreateAccumulationWithDerivedMetadata(
		context.Background(), record, metadata, "archive-"+content, "request-"+content,
	)
	if err != nil || !created {
		t.Fatalf("create accumulation: created=%v err=%v", created, err)
	}
	return record.RecordID
}

func currentAccumulationArchiveV10(t *testing.T, fixture *archiveRestoreFixture, agent, id string) k12storage.AccumulationArchiveV10 {
	t.Helper()
	items, err := fixture.records.ExportAccumulationsArchiveV10(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.AccumulationID == id {
			return item
		}
	}
	t.Fatalf("current accumulation %q is missing", id)
	return k12storage.AccumulationArchiveV10{}
}

func TestAccumulationArchiveV10OrdinaryRestorePreservesSourceAndProvenance(t *testing.T) {
	ctx := context.Background()
	source, destination := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
	// 独立目标只保留已注册实例，移除夹具自动附带的同内容错题。
	if _, err := destination.db.ExecContext(ctx, `DELETE FROM k12_mistakes WHERE record_id=?`, destination.oldRecordID); err != nil {
		t.Fatal(err)
	}
	id := seedAccumulationArchiveV10(t, source, "mingming", "床前明月光，疑是地上霜。", "《静夜思》")
	legacy, err := k12.NewAccumRecord("mingming", "", k12.AccumFields{
		Subject: "语文", EntryType: "古诗积累", Content: "旧资料", Source: "旧课本",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.records.Put(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	before := currentAccumulationArchiveV10(t, source, "mingming", id)
	archive, err := (usecase.Deps{Records: source.records}).Backup(ctx, "mingming")
	if err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: destination.records, ArchiveRestorer: destination.restore}
	if _, err := deps.Restore(ctx, archive); err != nil {
		t.Fatal(err)
	}
	after := currentAccumulationArchiveV10(t, destination, "mingming", id)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("current source/provenance changed: before=%+v after=%+v", before, after)
	}
	item, err := deps.GetAccumulation(ctx, "mingming", id)
	if err != nil || item.Fields.Source != "《静夜思》" {
		t.Fatalf("public source=%q err=%v", item.Fields.Source, err)
	}
	legacyEvidence := currentAccumulationArchiveV10(t, destination, "mingming", legacy.RecordID)
	if legacyEvidence.DerivedSourceRef != nil || legacyEvidence.SubjectProvenance != nil || legacyEvidence.EntryTypeProvenance != nil || legacyEvidence.SourceProvenance != nil {
		t.Fatal("restore invented current evidence for a legacy accumulation")
	}
	legacyItem, err := deps.GetAccumulation(ctx, "mingming", legacy.RecordID)
	if err != nil || legacyItem.Fields.Source != "旧课本" {
		t.Fatalf("legacy compatibility source=%q err=%v", legacyItem.Fields.Source, err)
	}
}

func TestAccumulationArchiveMissingHistoricalEvidenceRestoreAsRollsBackWithoutInventingSource(t *testing.T) {
	for _, version := range []int{9, 10} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			ctx := context.Background()
			source, target := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
			registerRestoreTarget(t, target)
			incomingID := seedAccumulationArchiveV10(t, source, "mingming", "床前明月光", "《静夜思》")
			existingID := seedAccumulationArchiveV10(t, target, "target-child", "春眠不觉晓", "《春晓》")
			before := currentAccumulationArchiveV10(t, target, "target-child", existingID)
			archive, err := (usecase.Deps{Records: source.records}).Backup(ctx, "mingming")
			if err != nil {
				t.Fatal(err)
			}
			// nil保留旧包及其升级副本未提供当前证据的语义。
			archive.Version, archive.CurrentAccumulations = version, nil
			if err := usecase.SealHexbak(archive); err != nil {
				t.Fatal(err)
			}
			deps := usecase.Deps{Records: target.records, ArchiveMigrator: target.restore}
			result, err := deps.RestoreAs(ctx, usecase.RestoreAsRequest{
				Archive: archive, SourceAgent: "mingming", TargetAgent: "target-child",
				GuardianConfirmed: true, IdempotencyKey: "legacy-current-source",
			})
			if err != nil {
				t.Fatal(err)
			}
			incoming := currentAccumulationArchiveV10(t, target, "target-child", incomingID)
			if incoming.DerivedSourceRef != nil || incoming.SubjectProvenance != nil || incoming.EntryTypeProvenance != nil || incoming.SourceProvenance != nil {
				t.Fatal("migration invented unavailable source evidence")
			}
			if _, err := deps.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{
				MigrationID: result.MigrationID, TargetAgent: "target-child", GuardianConfirmed: true,
			}); err != nil {
				t.Fatal(err)
			}
			if after := currentAccumulationArchiveV10(t, target, "target-child", existingID); !reflect.DeepEqual(before, after) {
				t.Fatal("legacy rollback changed preexisting current evidence")
			}
			if _, err := deps.GetAccumulation(ctx, "target-child", incomingID); !errors.Is(err, records.ErrNotFound) {
				t.Fatalf("legacy rollback retained the new root: %v", err)
			}
		})
	}
}

func TestAccumulationArchiveV10RestoreAsAndRollbackShareAtomicMetadata(t *testing.T) {
	for _, outcome := range []string{"rollback", "empty_target", "later_source", "journal_failure"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			source, target := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
			registerRestoreTarget(t, target)
			incomingID := seedAccumulationArchiveV10(t, source, "mingming", "床前明月光", "《静夜思》")
			var existingID string
			var before k12storage.AccumulationArchiveV10
			if outcome != "empty_target" {
				existingID = seedAccumulationArchiveV10(t, target, "target-child", "春眠不觉晓", "《春晓》")
				before = currentAccumulationArchiveV10(t, target, "target-child", existingID)
			}
			archive, err := (usecase.Deps{Records: source.records}).Backup(ctx, "mingming")
			if err != nil {
				t.Fatal(err)
			}
			original, _ := json.Marshal(archive)
			if outcome == "journal_failure" {
				if _, err := target.db.ExecContext(ctx, `CREATE TRIGGER fail_accumulation_archive_journal BEFORE INSERT ON k12_restore_journal BEGIN SELECT RAISE(ABORT,'controlled archive journal failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			deps := usecase.Deps{Records: target.records, ArchiveRestorer: target.restore, ArchiveMigrator: target.restore, Now: func() int64 { return 500 }}
			result, err := deps.RestoreAs(ctx, usecase.RestoreAsRequest{
				Archive: archive, SourceAgent: "mingming", TargetAgent: "target-child",
				GuardianConfirmed: true, IdempotencyKey: "archive-source-once",
			})
			if outcome == "journal_failure" {
				if err == nil {
					t.Fatal("journal failure committed metadata")
				}
				if _, err := deps.GetAccumulation(ctx, "target-child", incomingID); !errors.Is(err, records.ErrNotFound) {
					t.Fatalf("failed transaction left incoming accumulation: %v", err)
				}
				if after := currentAccumulationArchiveV10(t, target, "target-child", existingID); !reflect.DeepEqual(before, after) {
					t.Fatal("failed transaction changed target metadata")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			incoming, err := deps.GetAccumulation(ctx, "target-child", incomingID)
			if err != nil || incoming.Fields.Source != "《静夜思》" {
				t.Fatalf("migrated source=%q err=%v", incoming.Fields.Source, err)
			}
			afterOriginal, _ := json.Marshal(archive)
			if string(original) != string(afterOriginal) {
				t.Fatal("owner migration mutated the immutable source archive")
			}
			if outcome == "later_source" {
				later, err := deps.Backup(ctx, "target-child")
				if err != nil {
					t.Fatal(err)
				}
				for index := range later.CurrentAccumulations {
					if later.CurrentAccumulations[index].AccumulationID == incomingID {
						updated := "后来核验的来源"
						later.CurrentAccumulations[index].DerivedSourceRef = &updated
					}
				}
				if err := usecase.SealHexbak(later); err != nil {
					t.Fatal(err)
				}
				if _, err := deps.Restore(ctx, later); err != nil {
					t.Fatal(err)
				}
			}
			_, err = deps.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{
				MigrationID: result.MigrationID, TargetAgent: "target-child", GuardianConfirmed: true,
			})
			if outcome == "later_source" {
				if !errors.Is(err, records.ErrVersionConflict) {
					t.Fatalf("rollback overwrote later source: %v", err)
				}
				item, err := deps.GetAccumulation(ctx, "target-child", incomingID)
				if err != nil || item.Fields.Source != "后来核验的来源" {
					t.Fatalf("later source was lost: source=%q err=%v", item.Fields.Source, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if existingID != "" {
				if after := currentAccumulationArchiveV10(t, target, "target-child", existingID); !reflect.DeepEqual(before, after) {
					t.Fatal("rollback lost the target's original source/provenance")
				}
			} else if items, err := target.records.ExportAccumulationsArchiveV10(ctx, "target-child"); err != nil || len(items) != 0 {
				t.Fatalf("empty target snapshot retained migrated metadata: items=%+v err=%v", items, err)
			}
			if _, err := deps.GetAccumulation(ctx, "target-child", incomingID); !errors.Is(err, records.ErrNotFound) {
				t.Fatalf("rollback retained the migrated accumulation: %v", err)
			}
		})
	}
}

func TestAccumulationArchiveLegacyV1ThroughV9DoesNotInventOrOverwriteCurrentEvidence(t *testing.T) {
	ctx := context.Background()
	fixture := newArchiveRestoreFixture(t)
	id := seedAccumulationArchiveV10(t, fixture, "mingming", "床前明月光", "《静夜思》")
	before := currentAccumulationArchiveV10(t, fixture, "mingming", id)
	deps := usecase.Deps{Records: fixture.records, ArchiveRestorer: fixture.restore}
	for version := 1; version <= 9; version++ {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			archive, err := deps.Backup(ctx, "mingming")
			if err != nil {
				t.Fatal(err)
			}
			archive.Version, archive.CurrentAccumulations = version, nil
			if err := usecase.SealHexbak(archive); err != nil {
				t.Fatal(err)
			}
			if version == 1 {
				raw, err := json.Marshal(archive.Records)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(raw)
				archive.Checksum = hex.EncodeToString(digest[:])
			}
			if _, err := deps.Restore(ctx, archive); err != nil {
				t.Fatal(err)
			}
			if after := currentAccumulationArchiveV10(t, fixture, "mingming", id); !reflect.DeepEqual(before, after) {
				t.Fatal("legacy archive overwrote current evidence")
			}
		})
	}
}

func TestAccumulationArchiveV10RejectsBrokenEvidenceBeforeRestore(t *testing.T) {
	ctx := context.Background()
	fixture := newArchiveRestoreFixture(t)
	id := seedAccumulationArchiveV10(t, fixture, "mingming", "床前明月光", "《静夜思》")
	deps := usecase.Deps{Records: fixture.records, ArchiveRestorer: fixture.restore}
	before := currentAccumulationArchiveV10(t, fixture, "mingming", id)
	for _, mutation := range []string{"wrong_owner", "missing_root", "duplicate", "invalid_provenance", "unsigned_old_version"} {
		t.Run(mutation, func(t *testing.T) {
			archive, err := deps.Backup(ctx, "mingming")
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "wrong_owner":
				archive.CurrentAccumulations[0].AgentName = "other-child"
			case "missing_root":
				archive.CurrentAccumulations[0].AccumulationID = "missing-root"
			case "duplicate":
				archive.CurrentAccumulations = append(archive.CurrentAccumulations, archive.CurrentAccumulations[0])
			case "invalid_provenance":
				archive.CurrentAccumulations[0].SourceProvenance.Method = "invented"
			case "unsigned_old_version":
				archive.Version = 9
			}
			if err := usecase.SealHexbak(archive); err != nil {
				t.Fatal(err)
			}
			if _, err := deps.Restore(ctx, archive); err == nil {
				t.Fatal("invalid current evidence was accepted")
			}
			if after := currentAccumulationArchiveV10(t, fixture, "mingming", id); !reflect.DeepEqual(before, after) {
				t.Fatal("rejected archive changed persisted evidence")
			}
		})
	}
}
