package engineadapter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

var _ usecase.ArchiveMigrationRestorer = (*ArchiveRestoreAdapter)(nil)

func (a *ArchiveRestoreAdapter) RestoreArchiveAs(
	ctx context.Context,
	plan usecase.RestoreAsPlan,
) (usecase.RestoreAsResult, error) {
	if err := a.validateRestoreAsPlan(plan); err != nil {
		return usecase.RestoreAsResult{}, err
	}
	var result usecase.RestoreAsResult
	err := a.dispatcher.UpdateAgentPersisted(plan.TargetAgent,
		func(current routerAgentConfig) (routerAgentConfig, error) { return current, nil },
		func(updated *routerAgentConfig) error {
			tx, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin restore-as transaction: %w", err)
			}
			defer tx.Rollback()
			// DD-025 requires the migration copy's owner rewrite and checksum seal to be
			// part of the same transaction as all target writes and evidence rows.
			migrated, err := usecase.MigrateHexbakOwner(plan.OriginalArchive, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("rewrite archive owner in restore-as transaction: %w", err)
			}
			if migrated.Checksum != plan.MigratedArchive.Checksum {
				return fmt.Errorf("%w: migrated checksum changed after preflight", records.ErrVersionConflict)
			}

			existing, found, err := loadRestoreAsByIdempotency(ctx, tx, plan.TargetAgent, plan.IdempotencyKey)
			if err != nil {
				return err
			}
			if found && (existing.SourceAgent != plan.SourceAgent || existing.OriginalArchiveDigest != plan.OriginalArchiveDigest) {
				return fmt.Errorf("%w: restore-as idempotency key 已绑定其他归档", records.ErrVersionConflict)
			}
			ids, err := a.restoreRecordIDs(ctx, tx, plan.OriginalArchive, plan.TargetAgent, existing.MigrationID)
			if err != nil {
				return err
			}
			if len(ids) > 0 {
				migrated, err = usecase.MigrateHexbakOwnerWithRecordIDs(plan.OriginalArchive, plan.TargetAgent, ids)
				if err != nil {
					return err
				}
			}
			if found {
				comparison := *migrated
				// 已成功迁移的回执按创建时的格式核对，升级不产生新的迁移命令。
				if existing.Snapshot != nil && existing.Snapshot.Version != comparison.Version {
					comparison.Version, comparison.ArchiveID = existing.Snapshot.Version, ""
					if err = usecase.SealHexbak(&comparison); err != nil {
						return err
					}
				}
				comparisonArchive := &comparison
				if len(comparison.WeeklyHistory) > 0 && comparison.Checksum != existing.MigratedChecksum {
					comparisonArchive, err = restoreLegacyWeeklyAttemptOrder(&comparison, existing.MigratedChecksum)
					if err != nil {
						return err
					}
				}
				if existing.MigratedChecksum != comparisonArchive.Checksum {
					return fmt.Errorf("%w: restore-as idempotency key 已绑定其他归档", records.ErrVersionConflict)
				}
				existing.Idempotent = true
				result = existing
				return nil
			}

			preRecords, err := a.records.ExportAgentTx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("snapshot target records: %w", err)
			}
			preProblemAttempts, err := a.records.ExportProblemAttemptSnapshotsTx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("snapshot target Problem/Attempt ledger: %w", err)
			}
			preProblemSource, err := a.records.ExportProblemSourceArchiveV6Tx(
				ctx, tx, plan.TargetAgent,
			)
			if err != nil {
				return fmt.Errorf("snapshot target problem-source closure: %w", err)
			}
			profile := k12.ProfileFromMeta(updated.Metadata)
			var snapshotProfile *k12.ChildProfile
			if profile != (k12.ChildProfile{}) {
				snapshotProfile = &profile
			}
			snapshot := &usecase.Hexbak{
				Version: usecase.HexbakVersion, AgentName: plan.TargetAgent,
				ExportedAt: plan.RequestedAt, Records: preRecords, Profile: snapshotProfile,
				ProblemAttempts: preProblemAttempts,
			}
			if !preProblemSource.IsEmpty() {
				snapshot.ProblemSource = &preProblemSource
			}
			snapshot.CurrentCreativeWorks, err = a.records.ExportCreativeWorksArchiveV7Tx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("snapshot current creative works: %w", err)
			}
			snapshot.ReviewSchedules, err = a.records.ExportReviewSchedulesTx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return err
			}
			snapshot.WeeklyHistory, err = a.records.ExportWeeklyHistoryTx(ctx, tx, plan.TargetAgent, plan.RequestedAt)
			if err != nil {
				return err
			}
			snapshot.CurrentAccumulations, err = a.records.ExportAccumulationsArchiveV10Tx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("snapshot current accumulations: %w", err)
			}
			snapshot.TeachingDiagrams, err = a.records.ExportTeachingDiagramAssetsTx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("snapshot teaching diagrams: %w", err)
			}
			snapshot.ReviewSources, err = a.records.ExportReviewSourceArchiveTx(ctx, tx, plan.TargetAgent)
			if err != nil {
				return fmt.Errorf("snapshot target reviewed sources: %w", err)
			}
			recordAssets, err := usecase.PackHexbakAssets(plan.TargetAgent, preRecords)
			if err != nil {
				return fmt.Errorf("pack pre-restore snapshot assets: %w", err)
			}
			problemAssets, err := usecase.PackHexbakProblemAttemptAssets(plan.TargetAgent, preProblemAttempts)
			if err != nil {
				return fmt.Errorf("pack pre-restore snapshot Problem page assets: %w", err)
			}
			problemSourceAssets, err := usecase.PackHexbakProblemSourceAssets(
				plan.TargetAgent, snapshot.ProblemSource,
			)
			if err != nil {
				return fmt.Errorf("pack pre-restore snapshot source PageAssets: %w", err)
			}
			currentAssets, err := usecase.PackHexbakCurrentCreativeAssets(plan.TargetAgent, snapshot.CurrentCreativeWorks)
			if err != nil {
				return err
			}
			reviewAssets, err := usecase.PackHexbakReviewSourceAssets(plan.TargetAgent, snapshot.ReviewSources)
			if err != nil {
				return fmt.Errorf("pack pre-restore reviewed source assets: %w", err)
			}
			snapshot.Assets, err = usecase.MergeHexbakAssets(
				recordAssets, problemAssets, problemSourceAssets, currentAssets, reviewAssets,
			)
			if err != nil {
				return fmt.Errorf("merge pre-restore snapshot assets: %w", err)
			}
			snapshot.CreativeWorkOCR, err = usecase.PackHexbakCreativeWorkOCRWithResolver(
				ctx, plan.TargetAgent, preRecords,
				func(ctx context.Context, agentName, jobID string, version int) (k12.CreativeWorkOCRArchiveEvidence, error) {
					return a.records.GetCreativeWorkOCRArchiveEvidenceTx(ctx, tx, agentName, jobID, version)
				},
			)
			if err != nil {
				return fmt.Errorf("pack pre-restore snapshot OCR evidence: %w", err)
			}
			if err := usecase.SealHexbak(snapshot); err != nil {
				return fmt.Errorf("seal pre-restore snapshot: %w", err)
			}
			snapshotDigest, err := usecase.HexbakDigest(snapshot)
			if err != nil {
				return err
			}
			archiveJSON, err := json.Marshal(plan.OriginalArchive)
			if err != nil {
				return fmt.Errorf("marshal original archive: %w", err)
			}
			snapshotJSON, err := json.Marshal(snapshot)
			if err != nil {
				return fmt.Errorf("marshal pre-restore snapshot: %w", err)
			}
			migrationID := restoreMigrationID(plan)
			at := plan.RequestedAt
			if at <= 0 {
				at = time.Now().Unix()
			}

			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO k12_restore_archives
				(archive_digest,archive_version,source_agent,checksum,archive_json,created_at)
				VALUES(?,?,?,?,?,?)`, plan.OriginalArchiveDigest, plan.OriginalArchive.Version,
				plan.SourceAgent, plan.OriginalArchive.Checksum, string(archiveJSON), at); err != nil {
				return fmt.Errorf("preserve original archive: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO k12_restore_snapshots
				(snapshot_digest,target_agent,checksum,snapshot_json,created_at)
				VALUES(?,?,?,?,?)`, snapshotDigest, plan.TargetAgent, snapshot.Checksum, string(snapshotJSON), at); err != nil {
				return fmt.Errorf("preserve pre-restore snapshot: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO k12_restore_migrations
				(migration_id,source_agent,target_agent,idempotency_key,original_archive_digest,
				 migrated_checksum,snapshot_digest,status,restored_count,created_at,completed_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?)`, migrationID, plan.SourceAgent, plan.TargetAgent,
				plan.IdempotencyKey, plan.OriginalArchiveDigest, migrated.Checksum,
				snapshotDigest, usecase.RestoreMigrationCompleted, len(migrated.Records), at, at); err != nil {
				return fmt.Errorf("create restore-as receipt: %w", err)
			}
			ocrMigrations, err := inspectRestoreOCRMigrations(ctx, tx, a.records, migrated)
			if err != nil {
				return err
			}
			if err := a.records.ImportAgentRecordsTx(ctx, tx, plan.TargetAgent, migrated.Records); err != nil {
				return fmt.Errorf("merge owner-rewritten records: %w", err)
			}
			if plan.OriginalArchive.Version >= 11 {
				if err = a.records.RestoreReviewSchedulesTx(ctx, tx, plan.TargetAgent, migrated.Records, migrated.ReviewSchedules); err != nil {
					return err
				}
			}
			if err := a.records.ImportWeeklyHistoryTx(ctx, tx, plan.TargetAgent, migrated.WeeklyHistory, plan.RequestedAt); err != nil {
				return err
			}
			if plan.OriginalArchive.Version >= 10 {
				if err := a.records.ImportAccumulationsArchiveV10Tx(ctx, tx, plan.TargetAgent, migrated.CurrentAccumulations); err != nil {
					return fmt.Errorf("restore migrated current accumulations: %w", err)
				}
			}
			if err := a.records.ImportCreativeWorksArchiveV7Tx(ctx, tx, plan.TargetAgent, migrated.CurrentCreativeWorks); err != nil {
				return fmt.Errorf("restore migrated current creative works: %w", err)
			}
			if err := a.records.ImportTeachingDiagramAssetsTx(ctx, tx, plan.TargetAgent, migrated.TeachingDiagrams); err != nil {
				return fmt.Errorf("restore migrated teaching diagrams: %w", err)
			}
			if err := a.records.ImportProblemAttemptSnapshotsTx(
				ctx, tx, plan.TargetAgent, migrated.ProblemAttempts,
			); err != nil {
				return fmt.Errorf("merge owner-rewritten Problem/Attempt ledger: %w", err)
			}
			if migrated.ProblemSource != nil {
				if err := a.records.ImportProblemSourceArchiveV6Tx(
					ctx, tx, plan.TargetAgent, *migrated.ProblemSource,
				); err != nil {
					return fmt.Errorf("merge owner-rewritten problem-source closure: %w", err)
				}
			}
			if err := a.records.ImportReviewSourceArchiveTx(ctx, tx, plan.TargetAgent, migrated.ArchiveID, migrated.ReviewSources); err != nil {
				return fmt.Errorf("restore migrated reviewed sources: %w", err)
			}
			updated.Metadata = k12.ReplaceProfileInMeta(updated.Metadata, migrated.Profile)
			if err := a.agents.SaveAgentTx(ctx, tx, updated); err != nil {
				return fmt.Errorf("replace migrated target profile: %w", err)
			}
			if err := a.records.ImportCreativeWorkOCREvidenceTx(
				ctx, tx, plan.TargetAgent, migrated.CreativeWorkOCR,
			); err != nil {
				return fmt.Errorf("merge owner-rewritten OCR evidence: %w", err)
			}
			assetMigrations, err := installRestoreAsAssets(ctx, tx, migrationID, plan.OriginalArchive, migrated)
			if err != nil {
				return err
			}
			cleanupOnFailure := func(cause error) error {
				if cleanupErr := removeCreatedRestoreAssets(assetMigrations); cleanupErr != nil {
					return errors.Join(cause, cleanupErr)
				}
				return cause
			}
			journalEntries, err := appendRestoreAsJournal(
				ctx, tx, migrationID, at, plan, migrated, snapshot, assetMigrations, ocrMigrations,
			)
			if err != nil {
				return cleanupOnFailure(err)
			}
			if err := tx.Commit(); err != nil {
				return cleanupOnFailure(fmt.Errorf("commit restore-as transaction: %w", err))
			}
			result = usecase.RestoreAsResult{
				MigrationID: migrationID, SourceAgent: plan.SourceAgent, TargetAgent: plan.TargetAgent,
				Status: usecase.RestoreMigrationCompleted, Restored: len(migrated.Records),
				OriginalArchiveDigest: plan.OriginalArchiveDigest, MigratedChecksum: migrated.Checksum,
				SnapshotDigest: snapshotDigest, JournalEntries: journalEntries,
				OriginalArchivePreserved: true, Snapshot: snapshot,
			}
			return nil
		},
	)
	if err != nil {
		return usecase.RestoreAsResult{}, err
	}
	return result, nil
}

func (a *ArchiveRestoreAdapter) RollbackRestoreAs(
	ctx context.Context,
	req usecase.RestoreAsRollbackRequest,
) (usecase.RestoreAsResult, error) {
	if a == nil || a.db == nil || a.records == nil || a.dispatcher == nil || a.agents == nil {
		return usecase.RestoreAsResult{}, fmt.Errorf("k12 archive restore: atomic adapter is not fully configured")
	}
	var result usecase.RestoreAsResult
	err := a.dispatcher.UpdateAgentPersisted(req.TargetAgent,
		func(current routerAgentConfig) (routerAgentConfig, error) { return current, nil },
		func(updated *routerAgentConfig) error {
			tx, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("begin restore-as rollback: %w", err)
			}
			defer tx.Rollback()

			current, found, err := loadRestoreAsByMigrationID(ctx, tx, req.MigrationID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("%w: restore migration %q", records.ErrNotFound, req.MigrationID)
			}
			if current.TargetAgent != req.TargetAgent {
				return fmt.Errorf("%w: migration target=%q request=%q", usecase.ErrArchiveScopeMismatch, current.TargetAgent, req.TargetAgent)
			}
			if current.Status == usecase.RestoreMigrationRolledBack {
				current.Idempotent = true
				result = current
				return nil
			}
			if current.Status != usecase.RestoreMigrationCompleted || current.Snapshot == nil {
				return fmt.Errorf("%w: restore migration 状态 %q 不可回退", records.ErrVersionConflict, current.Status)
			}
			if err := usecase.VerifyHexbak(current.Snapshot); err != nil {
				return fmt.Errorf("verify immutable pre-restore snapshot: %w", err)
			}
			if current.Snapshot.AgentName != req.TargetAgent {
				return fmt.Errorf("%w: snapshot target=%q", usecase.ErrArchiveScopeMismatch, current.Snapshot.AgentName)
			}
			assetMigrations, err := loadRestoreAssetMigrations(ctx, tx, req.MigrationID)
			if err != nil {
				return err
			}
			ocrMigrations, err := loadRestoreOCRMigrations(ctx, tx, req.MigrationID)
			if err != nil {
				return err
			}
			original, err := loadOriginalRestoreArchive(ctx, tx, req.MigrationID)
			if err != nil {
				return err
			}
			ids, err := a.restoreRecordIDs(ctx, tx, original, req.TargetAgent, req.MigrationID)
			if err != nil {
				return err
			}
			migrated, err := usecase.MigrateHexbakOwnerWithRecordIDs(original, req.TargetAgent, ids)
			if err != nil {
				return fmt.Errorf("rebuild immutable migrated assets for rollback: %w", err)
			}
			if len(migrated.WeeklyHistory) != 0 && migrated.Checksum != current.MigratedChecksum {
				migrated, err = restoreLegacyWeeklyAttemptOrder(migrated, current.MigratedChecksum)
				if err != nil {
					return err
				}
			}
			if k12.ProfileFromMeta(updated.Metadata) != k12.ProfileFromMeta(k12.ReplaceProfileInMeta(nil, migrated.Profile)) {
				return fmt.Errorf("%w: learner profile changed after restore", records.ErrVersionConflict)
			}
			reinstalledSnapshot, err := ensureArchiveAssets(current.Snapshot)
			if err != nil {
				return fmt.Errorf("restore snapshot assets: %w", err)
			}
			rollbackFailure := func(cause error, removed []usecase.HexbakAsset) error {
				var compensation []error
				for _, item := range removed {
					if id, _, ensureErr := assetstore.Ensure(item.OwnerAgent, item.Data); ensureErr != nil || id != item.AssetID {
						compensation = append(compensation, fmt.Errorf("restore removed asset %q: %v", item.AssetID, ensureErr))
					}
				}
				if cleanupErr := removeCreatedRestoreAssets(reinstalledSnapshot); cleanupErr != nil {
					compensation = append(compensation, cleanupErr)
				}
				return errors.Join(append([]error{cause}, compensation...)...)
			}
			before, err := a.records.ExportAgentTx(ctx, tx, req.TargetAgent)
			if err != nil {
				return rollbackFailure(fmt.Errorf("snapshot current restore state: %w", err), nil)
			}
			var schedules map[string]k12storage.ArchivedReviewSchedule
			if current.Snapshot.Version >= 11 {
				schedules, err = a.records.ExportReviewSchedulesTx(ctx, tx, req.TargetAgent)
				if err != nil {
					return rollbackFailure(err, nil)
				}
			}
			if err = verifyRestoreRecordVersions(before, schedules, current.Snapshot, migrated); err != nil {
				return rollbackFailure(err, nil)
			}
			if err := a.records.RollbackWeeklyHistoryTx(ctx, tx, req.TargetAgent, current.Snapshot.WeeklyHistory, migrated.WeeklyHistory); err != nil {
				return rollbackFailure(err, nil)
			}
			beforeProblemAttempts, err := a.records.ExportProblemAttemptSnapshotsTx(ctx, tx, req.TargetAgent)
			if err != nil {
				return rollbackFailure(fmt.Errorf("snapshot current Problem/Attempt state: %w", err), nil)
			}
			beforeProblemSource, err := a.records.ExportProblemSourceArchiveV6Tx(
				ctx, tx, req.TargetAgent,
			)
			if err != nil {
				return rollbackFailure(fmt.Errorf("snapshot current problem-source state: %w", err), nil)
			}
			beforeCreative, err := a.records.ExportCreativeWorksArchiveV7Tx(ctx, tx, req.TargetAgent)
			if err != nil {
				return rollbackFailure(err, nil)
			}
			beforeAccumulations, err := a.records.ExportAccumulationsArchiveV10Tx(ctx, tx, req.TargetAgent)
			if err != nil {
				return rollbackFailure(err, nil)
			}
			if current.Snapshot.Version >= 10 {
				if err := verifyRestoreAccumulationsUnchanged(beforeAccumulations, current.Snapshot, migrated, original.Version); err != nil {
					return rollbackFailure(err, nil)
				}
			}
			beforeReviewed, err := a.records.ExportReviewSourceArchiveTx(ctx, tx, req.TargetAgent)
			if err != nil {
				return rollbackFailure(err, nil)
			}
			expectedGroups := map[string]bool{}
			for _, archive := range []*k12storage.ReviewSourceArchive{current.Snapshot.ReviewSources, migrated.ReviewSources} {
				if archive != nil {
					for _, group := range archive.Groups {
						expectedGroups[group.GroupID] = true
					}
				}
			}
			if beforeReviewed != nil {
				for _, group := range beforeReviewed.Groups {
					if !expectedGroups[group.GroupID] {
						return rollbackFailure(fmt.Errorf("%w: reviewed sources changed after restore", records.ErrVersionConflict), nil)
					}
					delete(expectedGroups, group.GroupID)
				}
			}
			if len(expectedGroups) != 0 {
				return rollbackFailure(fmt.Errorf("%w: reviewed sources changed after restore", records.ErrVersionConflict), nil)
			}
			if migrated.ReviewSources != nil {
				// 回退校验实际迁移时封存的来源 ID，不能用当前归档版本重新派生。
				var migratedID string
				if err = tx.QueryRowContext(ctx, `SELECT entity_id FROM k12_restore_journal
					WHERE migration_id=? AND operation='seal_migrated' AND entity_kind='hexbak' AND after_json=?`,
					req.MigrationID, current.MigratedChecksum).Scan(&migratedID); err != nil {
					return rollbackFailure(fmt.Errorf("read sealed migrated archive identity: %w", err), nil)
				}
				for _, source := range migrated.ReviewSources.Snapshots {
					var origin string
					if err = tx.QueryRowContext(ctx, `SELECT archive_id FROM k12_review_source_archive WHERE agent_name=? AND record_id=?`, req.TargetAgent, source.Member.RecordID).Scan(&origin); err != nil || origin != migratedID {
						return rollbackFailure(fmt.Errorf("%w: reviewed source selection changed after restore", records.ErrVersionConflict), nil)
					}
				}
			}
			invocations := map[string]k12.ImageTaskInvocation{}
			for _, works := range [][]k12storage.CreativeWorkArchiveV7{current.Snapshot.CurrentCreativeWorks, migrated.CurrentCreativeWorks} {
				for _, work := range works {
					for _, invocation := range work.Invocations {
						invocations[invocation.InvocationID] = invocation
					}
				}
			}
			for _, work := range beforeCreative {
				for _, invocation := range work.Invocations {
					prior, found := invocations[invocation.InvocationID]
					// 回退不能删除迁移之后已经发出的新调用或更晚的结果事实。
					if invocation.Status != k12.ImageTaskInvocationPrepared && (!found || prior.Status != invocation.Status || prior.ResultDigest != invocation.ResultDigest) {
						return rollbackFailure(fmt.Errorf("%w: creative invocation advanced after restore", records.ErrVersionConflict), nil)
					}
				}
			}
			if err := a.records.DeleteProblemSourceArchiveV6Tx(
				ctx, tx, req.TargetAgent,
			); err != nil {
				return rollbackFailure(fmt.Errorf("remove migrated problem-source closure: %w", err), nil)
			}
			if err := a.records.RollbackImportedRecordsTx(ctx, tx, req.TargetAgent, current.Snapshot.Records, migrated.Records); err != nil {
				return rollbackFailure(fmt.Errorf("restore pre-migration records: %w", err), nil)
			}
			if current.Snapshot.Version >= 11 {
				if err = a.records.RestoreReviewSchedulesTx(ctx, tx, req.TargetAgent, current.Snapshot.Records, current.Snapshot.ReviewSchedules); err != nil {
					return rollbackFailure(err, nil)
				}
			}
			if err := a.records.ImportAccumulationsArchiveV10Tx(ctx, tx, req.TargetAgent, current.Snapshot.CurrentAccumulations); err != nil {
				return rollbackFailure(fmt.Errorf("restore current accumulation snapshot: %w", err), nil)
			}
			if err := a.records.ImportCreativeWorksArchiveV7Tx(ctx, tx, req.TargetAgent, current.Snapshot.CurrentCreativeWorks); err != nil {
				return rollbackFailure(fmt.Errorf("restore current creative snapshot: %w", err), nil)
			}
			if err := a.records.ImportTeachingDiagramAssetsTx(ctx, tx, req.TargetAgent, current.Snapshot.TeachingDiagrams); err != nil {
				return rollbackFailure(fmt.Errorf("restore teaching diagram snapshot: %w", err), nil)
			}
			if err := a.records.ReplaceProblemAttemptSnapshotsTx(
				ctx, tx, req.TargetAgent, current.Snapshot.ProblemAttempts,
			); err != nil {
				return rollbackFailure(fmt.Errorf("restore pre-migration Problem/Attempt ledger: %w", err), nil)
			}
			if current.Snapshot.ProblemSource != nil {
				if err := a.records.ImportProblemSourceArchiveV6Tx(
					ctx, tx, req.TargetAgent, *current.Snapshot.ProblemSource,
				); err != nil {
					return rollbackFailure(fmt.Errorf("restore pre-migration problem-source closure: %w", err), nil)
				}
			}
			if err := a.records.ReplaceReviewSourceArchiveTx(ctx, tx, req.TargetAgent, current.Snapshot.ArchiveID, current.Snapshot.ReviewSources); err != nil {
				return rollbackFailure(fmt.Errorf("restore pre-migration reviewed sources: %w", err), nil)
			}
			createdPageAssetIDs := make([]string, 0, len(assetMigrations))
			for _, item := range assetMigrations {
				if item.CreatedNew {
					createdPageAssetIDs = append(createdPageAssetIDs, item.TargetAssetID)
				}
			}
			if err := a.records.DeleteUnreferencedProblemSourcePageAssetsTx(
				ctx, tx, req.TargetAgent, createdPageAssetIDs,
			); err != nil {
				return rollbackFailure(fmt.Errorf("remove migration-created PageAsset metadata: %w", err), nil)
			}
			if err := a.records.ImportCreativeWorkOCREvidenceTx(
				ctx, tx, req.TargetAgent, current.Snapshot.CreativeWorkOCR,
			); err != nil {
				return rollbackFailure(fmt.Errorf("restore pre-migration OCR evidence: %w", err), nil)
			}
			createdOCRJobIDs := make([]string, 0, len(ocrMigrations))
			for _, item := range ocrMigrations {
				if item.CreatedNew {
					createdOCRJobIDs = append(createdOCRJobIDs, item.TargetJobID)
				}
			}
			if err := a.records.DeleteCreativeWorkOCRJobsTx(ctx, tx, req.TargetAgent, createdOCRJobIDs); err != nil {
				return rollbackFailure(fmt.Errorf("remove migration-created OCR evidence: %w", err), nil)
			}
			updated.Metadata = k12.ReplaceProfileInMeta(updated.Metadata, current.Snapshot.Profile)
			if err := a.agents.SaveAgentTx(ctx, tx, updated); err != nil {
				return rollbackFailure(fmt.Errorf("restore pre-migration profile: %w", err), nil)
			}
			at := time.Now().Unix()
			beforeJSON, _ := json.Marshal(struct {
				Records              []*records.AgentRecord              `json:"records"`
				ProblemAttempts      []k12.ProblemAttemptSnapshot        `json:"problem_attempts"`
				ProblemSource        k12storage.ProblemSourceArchiveV6   `json:"problem_source"`
				CurrentAccumulations []k12storage.AccumulationArchiveV10 `json:"current_accumulations,omitempty"`
			}{before, beforeProblemAttempts, beforeProblemSource, beforeAccumulations})
			afterJSON, _ := json.Marshal(struct {
				Records              []*records.AgentRecord              `json:"records"`
				ProblemAttempts      []k12.ProblemAttemptSnapshot        `json:"problem_attempts"`
				ProblemSource        *k12storage.ProblemSourceArchiveV6  `json:"problem_source,omitempty"`
				CurrentAccumulations []k12storage.AccumulationArchiveV10 `json:"current_accumulations,omitempty"`
			}{current.Snapshot.Records, current.Snapshot.ProblemAttempts, current.Snapshot.ProblemSource, current.Snapshot.CurrentAccumulations})
			var ordinal int
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM k12_restore_journal WHERE migration_id=?`, req.MigrationID).Scan(&ordinal); err != nil {
				return rollbackFailure(fmt.Errorf("next rollback journal ordinal: %w", err), nil)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO k12_restore_journal
				(migration_id,ordinal,operation,entity_kind,entity_id,before_json,after_json,created_at)
				VALUES(?,?,?,?,?,?,?,?)`, req.MigrationID, ordinal, "rollback_snapshot", "target_archive",
				req.TargetAgent, string(beforeJSON), string(afterJSON), at); err != nil {
				return rollbackFailure(fmt.Errorf("append rollback journal: %w", err), nil)
			}
			migratedByID := make(map[string]usecase.HexbakAsset, len(migrated.Assets))
			for _, item := range migrated.Assets {
				migratedByID[item.AssetID] = item
			}
			removed := make([]usecase.HexbakAsset, 0, len(assetMigrations))
			for _, item := range assetMigrations {
				if !item.CreatedNew {
					continue
				}
				payload, ok := migratedByID[item.TargetAssetID]
				if !ok {
					return rollbackFailure(fmt.Errorf("rollback asset %q missing from immutable archive", item.TargetAssetID), removed)
				}
				wasRemoved, removeErr := assetstore.Remove(req.TargetAgent, item.TargetAssetID)
				if removeErr != nil {
					return rollbackFailure(removeErr, removed)
				}
				if wasRemoved {
					removed = append(removed, payload)
				}
				ordinal++
				if _, err := tx.ExecContext(ctx, `INSERT INTO k12_restore_journal
					(migration_id,ordinal,operation,entity_kind,entity_id,before_json,after_json,created_at)
					VALUES(?,?,?,?,?,?,?,?)`, req.MigrationID, ordinal, "rollback_asset_cleanup", "asset",
					item.TargetAssetID, item.TargetAssetID, "", at); err != nil {
					return rollbackFailure(fmt.Errorf("append rollback asset journal: %w", err), removed)
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE k12_restore_migrations
				SET status=?, rolled_back_at=? WHERE migration_id=? AND status=?`,
				usecase.RestoreMigrationRolledBack, at, req.MigrationID, usecase.RestoreMigrationCompleted); err != nil {
				return rollbackFailure(fmt.Errorf("advance rollback receipt: %w", err), removed)
			}
			if err := tx.Commit(); err != nil {
				return rollbackFailure(fmt.Errorf("commit restore-as rollback: %w", err), removed)
			}
			current.Status = usecase.RestoreMigrationRolledBack
			current.JournalEntries = ordinal
			current.Restored = len(current.Snapshot.Records)
			result = current
			return nil
		},
	)
	if err != nil {
		return usecase.RestoreAsResult{}, err
	}
	return result, nil
}

// local alias keeps callback signatures readable without weakening the concrete router contract.
type routerAgentConfig = router.AgentConfig

type restoreAssetMigration struct {
	SourceAssetID string
	TargetAssetID string
	SHA256        string
	MIME          string
	CreatedNew    bool
}

type restoreOCRMigration struct {
	TargetJobID string `json:"target_job_id"`
	CreatedNew  bool   `json:"created_new"`
}

func inspectRestoreOCRMigrations(
	ctx context.Context,
	tx *sql.Tx,
	store *k12storage.Store,
	migrated *usecase.Hexbak,
) ([]restoreOCRMigration, error) {
	seen := map[string]struct{}{}
	jobIDs := make([]string, 0, len(migrated.CreativeWorkOCR))
	for _, item := range migrated.CreativeWorkOCR {
		if _, ok := seen[item.JobID]; ok {
			continue
		}
		seen[item.JobID] = struct{}{}
		jobIDs = append(jobIDs, item.JobID)
	}
	sort.Strings(jobIDs)
	out := make([]restoreOCRMigration, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		exists, err := store.CreativeWorkOCRJobExistsTx(ctx, tx, jobID)
		if err != nil {
			return nil, fmt.Errorf("inspect target OCR job %q: %w", jobID, err)
		}
		out = append(out, restoreOCRMigration{TargetJobID: jobID, CreatedNew: !exists})
	}
	return out, nil
}

func installRestoreAsAssets(
	ctx context.Context,
	tx *sql.Tx,
	migrationID string,
	original, migrated *usecase.Hexbak,
) ([]restoreAssetMigration, error) {
	if err := usecase.ValidateHexbakAssets(original); err != nil {
		return nil, err
	}
	if err := usecase.ValidateHexbakAssets(migrated); err != nil {
		return nil, err
	}
	sourceByDigest := make(map[string]usecase.HexbakAsset, len(original.Assets))
	for _, item := range original.Assets {
		sourceByDigest[item.SHA256] = item
	}
	installed := make([]restoreAssetMigration, 0, len(migrated.Assets))
	for i, item := range migrated.Assets {
		source, ok := sourceByDigest[item.SHA256]
		if !ok {
			return nil, errors.Join(
				fmt.Errorf("restore-as target asset %q has no immutable source", item.AssetID),
				removeCreatedRestoreAssets(installed),
			)
		}
		id, created, err := assetstore.Ensure(item.OwnerAgent, item.Data)
		if err != nil || id != item.AssetID {
			if err == nil {
				err = fmt.Errorf("assetstore returned %q want %q", id, item.AssetID)
			}
			return nil, errors.Join(
				fmt.Errorf("install restore-as asset: %w", err),
				removeCreatedRestoreAssets(installed),
			)
		}
		entry := restoreAssetMigration{
			SourceAssetID: source.AssetID, TargetAssetID: item.AssetID,
			SHA256: item.SHA256, MIME: item.MIME, CreatedNew: created,
		}
		installed = append(installed, entry)
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_restore_asset_migrations
			(migration_id,ordinal,source_asset_id,target_asset_id,sha256,mime,created_new)
			VALUES(?,?,?,?,?,?,?)`, migrationID, i+1, entry.SourceAssetID, entry.TargetAssetID,
			entry.SHA256, entry.MIME, boolInt(entry.CreatedNew)); err != nil {
			return nil, errors.Join(
				fmt.Errorf("persist restore-as asset mapping[%d]: %w", i+1, err),
				removeCreatedRestoreAssets(installed),
			)
		}
	}
	return installed, nil
}

// ensureArchiveAssets repairs/installs every verified asset in an archive. The returned
// entries record only this call's newly created files so a later DB failure can compensate.
func ensureArchiveAssets(bak *usecase.Hexbak) ([]restoreAssetMigration, error) {
	if err := usecase.ValidateHexbakAssets(bak); err != nil {
		return nil, err
	}
	installed := make([]restoreAssetMigration, 0, len(bak.Assets))
	for _, item := range bak.Assets {
		id, created, err := assetstore.Ensure(item.OwnerAgent, item.Data)
		if err != nil || id != item.AssetID {
			if err == nil {
				err = fmt.Errorf("assetstore returned %q want %q", id, item.AssetID)
			}
			return nil, errors.Join(err, removeCreatedRestoreAssets(installed))
		}
		installed = append(installed, restoreAssetMigration{
			SourceAssetID: item.AssetID, TargetAssetID: item.AssetID,
			SHA256: item.SHA256, MIME: item.MIME, CreatedNew: created,
		})
	}
	return installed, nil
}

func removeCreatedRestoreAssets(items []restoreAssetMigration) error {
	var errs []error
	for i := len(items) - 1; i >= 0; i-- {
		if !items[i].CreatedNew {
			continue
		}
		if _, err := assetstore.Remove(assetOwner(items[i].TargetAssetID), items[i].TargetAssetID); err != nil {
			errs = append(errs, fmt.Errorf("compensate restore asset %q: %w", items[i].TargetAssetID, err))
		}
	}
	return errors.Join(errs...)
}

func assetOwner(id string) string {
	owner, _, _ := assetstore.Parse(id)
	return owner
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func loadRestoreAssetMigrations(ctx context.Context, tx *sql.Tx, migrationID string) ([]restoreAssetMigration, error) {
	rows, err := tx.QueryContext(ctx, `SELECT source_asset_id,target_asset_id,sha256,mime,created_new
		FROM k12_restore_asset_migrations WHERE migration_id=? ORDER BY ordinal`, migrationID)
	if err != nil {
		return nil, fmt.Errorf("load restore-as asset mappings: %w", err)
	}
	defer rows.Close()
	var out []restoreAssetMigration
	for rows.Next() {
		var item restoreAssetMigration
		var created int
		if err := rows.Scan(&item.SourceAssetID, &item.TargetAssetID, &item.SHA256, &item.MIME, &created); err != nil {
			return nil, fmt.Errorf("scan restore-as asset mapping: %w", err)
		}
		item.CreatedNew = created != 0
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate restore-as asset mappings: %w", err)
	}
	return out, nil
}

func loadRestoreOCRMigrations(ctx context.Context, tx *sql.Tx, migrationID string) ([]restoreOCRMigration, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT after_json FROM k12_restore_journal
		WHERE migration_id=? AND operation='rewrite_owner' AND entity_kind='creative_work_ocr_ledger'
		ORDER BY ordinal LIMIT 1`, migrationID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load restore-as OCR mappings: %w", err)
	}
	var payload struct {
		Jobs []restoreOCRMigration `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		// v4 development snapshots before created_new was journaled stored only
		// the evidence array. Fail safe by deleting none during rollback.
		var legacy []k12.CreativeWorkOCRArchiveEvidence
		if legacyErr := json.Unmarshal([]byte(raw), &legacy); legacyErr == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("decode restore-as OCR mappings: %w", err)
	}
	return payload.Jobs, nil
}

func loadOriginalRestoreArchive(ctx context.Context, tx *sql.Tx, migrationID string) (*usecase.Hexbak, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT a.archive_json
		FROM k12_restore_migrations m
		JOIN k12_restore_archives a ON a.archive_digest=m.original_archive_digest
		WHERE m.migration_id=?`, migrationID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("load immutable restore-as archive: %w", err)
	}
	var bak usecase.Hexbak
	if err := json.Unmarshal([]byte(raw), &bak); err != nil {
		return nil, fmt.Errorf("decode immutable restore-as archive: %w", err)
	}
	if err := usecase.VerifyHexbak(&bak); err != nil {
		return nil, fmt.Errorf("verify immutable restore-as archive: %w", err)
	}
	return &bak, nil
}

func (a *ArchiveRestoreAdapter) validateRestoreAsPlan(plan usecase.RestoreAsPlan) error {
	if a == nil || a.db == nil || a.records == nil || a.dispatcher == nil || a.agents == nil {
		return fmt.Errorf("k12 archive restore: atomic adapter is not fully configured")
	}
	if plan.OriginalArchive == nil || plan.MigratedArchive == nil || plan.SourceAgent == "" || plan.TargetAgent == "" || plan.IdempotencyKey == "" {
		return fmt.Errorf("%w: incomplete restore-as plan", usecase.ErrInvalidInput)
	}
	if err := usecase.VerifyHexbak(plan.OriginalArchive); err != nil {
		return fmt.Errorf("verify original restore-as archive: %w", err)
	}
	digest, err := usecase.HexbakDigest(plan.OriginalArchive)
	if err != nil {
		return err
	}
	if digest != plan.OriginalArchiveDigest || plan.OriginalArchive.AgentName != plan.SourceAgent {
		return usecase.ErrArchiveScopeMismatch
	}
	if err := usecase.VerifyHexbak(plan.MigratedArchive); err != nil {
		return fmt.Errorf("verify migrated restore-as archive: %w", err)
	}
	if plan.MigratedArchive.AgentName != plan.TargetAgent {
		return usecase.ErrArchiveScopeMismatch
	}
	return a.records.ValidateAgentRecords(plan.TargetAgent, plan.MigratedArchive.Records)
}

func restoreMigrationID(plan usecase.RestoreAsPlan) string {
	sum := sha256.Sum256([]byte(plan.TargetAgent + "\x00" + plan.IdempotencyKey + "\x00" + plan.OriginalArchiveDigest))
	return "restore-" + hex.EncodeToString(sum[:16])
}

// 旧迁移没有冻结排序键；仅在重建结果严格匹配原回执时使用旧视图回退。
func restoreLegacyWeeklyAttemptOrder(migrated *usecase.Hexbak, checksum string) (*usecase.Hexbak, error) {
	legacy := *migrated
	legacy.ArchiveID = ""
	legacy.WeeklyHistory = slices.Clone(migrated.WeeklyHistory)
	for i := range legacy.WeeklyHistory {
		week := &legacy.WeeklyHistory[i]
		week.Attempts = slices.Clone(week.Attempts)
		for j := range week.Attempts {
			week.Attempts[j].OrderID = ""
		}
	}
	if err := usecase.SealHexbak(&legacy); err != nil {
		return nil, err
	}
	if legacy.Checksum == checksum {
		return &legacy, nil
	}
	return migrated, nil
}

func appendRestoreAsJournal(
	ctx context.Context,
	tx *sql.Tx,
	migrationID string,
	at int64,
	plan usecase.RestoreAsPlan,
	migrated *usecase.Hexbak,
	snapshot *usecase.Hexbak,
	assets []restoreAssetMigration,
	ocrMigrations []restoreOCRMigration,
) (int, error) {
	type entry struct{ operation, kind, id, before, after string }
	entries := []entry{
		{"preserve_archive", "hexbak", plan.OriginalArchive.ArchiveID, "", plan.OriginalArchiveDigest},
		{"snapshot_target", "target_archive", plan.TargetAgent, "", snapshot.Checksum},
	}
	for i, rec := range migrated.Records {
		before, _ := json.Marshal(plan.OriginalArchive.Records[i])
		after, _ := json.Marshal(rec)
		entries = append(entries, entry{"rewrite_owner", "record", rec.RecordID, string(before), string(after)})
	}
	for _, item := range assets {
		after, _ := json.Marshal(map[string]any{
			"asset_id": item.TargetAssetID, "sha256": item.SHA256,
			"mime": item.MIME, "created_new": item.CreatedNew,
		})
		entries = append(entries, entry{"migrate_asset", "asset", item.TargetAssetID, item.SourceAssetID, string(after)})
	}
	if len(migrated.CreativeWorkOCR) > 0 {
		before, _ := json.Marshal(plan.OriginalArchive.CreativeWorkOCR)
		after, _ := json.Marshal(struct {
			Evidence []k12.CreativeWorkOCRArchiveEvidence `json:"evidence"`
			Jobs     []restoreOCRMigration                `json:"jobs"`
		}{migrated.CreativeWorkOCR, ocrMigrations})
		entries = append(entries, entry{
			"rewrite_owner", "creative_work_ocr_ledger", plan.TargetAgent, string(before), string(after),
		})
	}
	if len(migrated.ProblemAttempts) > 0 || len(plan.OriginalArchive.ProblemAttempts) > 0 {
		before, _ := json.Marshal(plan.OriginalArchive.ProblemAttempts)
		after, _ := json.Marshal(migrated.ProblemAttempts)
		entries = append(entries, entry{
			"rewrite_owner", "problem_attempt_ledger", plan.TargetAgent, string(before), string(after),
		})
	}
	if migrated.ProblemSource != nil || plan.OriginalArchive.ProblemSource != nil {
		before, _ := json.Marshal(plan.OriginalArchive.ProblemSource)
		after, _ := json.Marshal(migrated.ProblemSource)
		entries = append(entries, entry{
			"rewrite_owner", "problem_source_ledger", plan.TargetAgent,
			string(before), string(after),
		})
	}
	beforeProfile, _ := json.Marshal(snapshot.Profile)
	afterProfile, _ := json.Marshal(migrated.Profile)
	entries = append(entries,
		entry{"replace_profile", "profile", plan.TargetAgent, string(beforeProfile), string(afterProfile)},
		entry{"seal_migrated", "hexbak", migrated.ArchiveID, plan.OriginalArchive.Checksum, migrated.Checksum},
	)
	for i, item := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_restore_journal
			(migration_id,ordinal,operation,entity_kind,entity_id,before_json,after_json,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, migrationID, i+1, item.operation, item.kind, item.id,
			item.before, item.after, at); err != nil {
			return 0, fmt.Errorf("append restore-as journal[%d]: %w", i+1, err)
		}
	}
	return len(entries), nil
}

// 整批回退前在同一事务校验所有将被替换的根；历史包也携带根版本。
func verifyRestoreRecordVersions(before []*records.AgentRecord, live map[string]k12storage.ArchivedReviewSchedule, snapshot, migrated *usecase.Hexbak) error {
	type recordKey struct{ collection, id string }
	expected := make(map[recordKey]*records.AgentRecord)
	schedules := make(map[string]k12storage.ArchivedReviewSchedule)
	for _, archive := range []*usecase.Hexbak{snapshot, migrated} {
		for _, record := range archive.Records {
			expected[recordKey{record.Collection, record.RecordID}] = record
			if record.Collection == k12.CollectionMistakes {
				delete(schedules, record.RecordID)
			}
		}
		for id, schedule := range archive.ReviewSchedules {
			schedules[id] = schedule
		}
	}
	for _, record := range before {
		key := recordKey{record.Collection, record.RecordID}
		previous := expected[key]
		if previous == nil || record.Version != previous.Version || record.Status != previous.Status {
			return fmt.Errorf("%w: %s record changed after restore", records.ErrVersionConflict, record.Collection)
		}
		schedule, known := schedules[record.RecordID]
		if record.Collection == k12.CollectionMistakes && known && live != nil && schedule != live[record.RecordID] {
			return fmt.Errorf("%w: mistake review changed after restore", records.ErrVersionConflict)
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: a restored record is missing", records.ErrVersionConflict)
	}
	return nil
}

// verifyRestoreAccumulationsUnchanged 防止回退覆盖迁移之后修改的来源事实。
func verifyRestoreAccumulationsUnchanged(before []k12storage.AccumulationArchiveV10, snapshot, migrated *usecase.Hexbak, sourceVersion int) error {
	expected := make(map[string]k12storage.AccumulationArchiveV10, len(snapshot.CurrentAccumulations)+len(migrated.CurrentAccumulations))
	for _, item := range snapshot.CurrentAccumulations {
		expected[item.AccumulationID] = item
	}
	if sourceVersion >= 10 {
		for _, item := range migrated.CurrentAccumulations {
			expected[item.AccumulationID] = item
		}
	}
	// 未携带当前证据的迁移只给新增根保留空列，已有根沿用迁移前快照。
	for _, rec := range migrated.Records {
		if rec.Collection == k12.CollectionAccumulation {
			if _, exists := expected[rec.RecordID]; !exists {
				expected[rec.RecordID] = k12storage.AccumulationArchiveV10{AccumulationID: rec.RecordID, AgentName: migrated.AgentName, RowVersion: 1}
			}
		}
	}
	if len(before) != len(expected) {
		return fmt.Errorf("%w: current accumulations changed after restore", records.ErrVersionConflict)
	}
	for _, item := range before {
		prior, found := expected[item.AccumulationID]
		actualJSON, err := json.Marshal(item)
		if err != nil {
			return err
		}
		expectedJSON, err := json.Marshal(prior)
		if err != nil {
			return err
		}
		if !found || string(actualJSON) != string(expectedJSON) {
			return fmt.Errorf("%w: current accumulation source changed after restore", records.ErrVersionConflict)
		}
	}
	return nil
}

func loadRestoreAsByIdempotency(ctx context.Context, tx *sql.Tx, target, key string) (usecase.RestoreAsResult, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT migration_id FROM k12_restore_migrations WHERE target_agent=? AND idempotency_key=?`, target, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return usecase.RestoreAsResult{}, false, nil
	}
	if err != nil {
		return usecase.RestoreAsResult{}, false, fmt.Errorf("query restore-as idempotency: %w", err)
	}
	result, found, err := loadRestoreAsByMigrationID(ctx, tx, id)
	return result, found, err
}

func loadRestoreAsByMigrationID(ctx context.Context, tx *sql.Tx, id string) (usecase.RestoreAsResult, bool, error) {
	var result usecase.RestoreAsResult
	err := tx.QueryRowContext(ctx, `SELECT migration_id,source_agent,target_agent,status,restored_count,
		original_archive_digest,migrated_checksum,snapshot_digest
		FROM k12_restore_migrations WHERE migration_id=?`, id).Scan(
		&result.MigrationID, &result.SourceAgent, &result.TargetAgent, &result.Status, &result.Restored,
		&result.OriginalArchiveDigest, &result.MigratedChecksum, &result.SnapshotDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return usecase.RestoreAsResult{}, false, nil
	}
	if err != nil {
		return usecase.RestoreAsResult{}, false, fmt.Errorf("query restore-as migration: %w", err)
	}
	var snapshotJSON string
	if err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM k12_restore_snapshots WHERE snapshot_digest=?`, result.SnapshotDigest).Scan(&snapshotJSON); err != nil {
		return usecase.RestoreAsResult{}, false, fmt.Errorf("load restore-as snapshot: %w", err)
	}
	var snapshot usecase.Hexbak
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return usecase.RestoreAsResult{}, false, fmt.Errorf("decode restore-as snapshot: %w", err)
	}
	result.Snapshot = &snapshot
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_restore_journal WHERE migration_id=?`, id).Scan(&result.JournalEntries); err != nil {
		return usecase.RestoreAsResult{}, false, fmt.Errorf("count restore-as journal: %w", err)
	}
	result.OriginalArchivePreserved = true
	return result, true, nil
}
