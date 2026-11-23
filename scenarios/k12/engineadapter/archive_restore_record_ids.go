package engineadapter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// restoreRecordIDs 从既有回执重建原映射；只有新命令才根据当前物理归属分配目标根 ID。
func (a *ArchiveRestoreAdapter) restoreRecordIDs(ctx context.Context, tx *sql.Tx, archive *usecase.Hexbak, target, migrationID string) (usecase.RestoreRecordIDs, error) {
	ids := usecase.RestoreRecordIDs{}
	rows, err := tx.QueryContext(ctx, `SELECT json_extract(j.before_json,'$.collection'),
		json_extract(j.before_json,'$.record_id'),j.entity_id FROM k12_restore_journal j
		JOIN k12_restore_migrations m ON m.migration_id=j.migration_id
		WHERE m.source_agent=? AND m.target_agent=? AND (?='' OR m.migration_id=?)
		AND (m.status='completed' OR m.migration_id=?)
		AND j.operation='rewrite_owner' AND j.entity_kind='record'
		AND json_extract(j.before_json,'$.record_id')<>j.entity_id`, archive.AgentName, target, migrationID, migrationID, migrationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var collection, old, replacement string
		if err = rows.Scan(&collection, &old, &replacement); err != nil {
			break
		}
		if ids[collection] == nil {
			ids[collection] = map[string]string{}
		}
		if prior := ids[collection][old]; prior != "" && prior != replacement {
			err = fmt.Errorf("%w: conflicting restore record mapping", records.ErrVersionConflict)
			break
		}
		ids[collection][old] = replacement
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if migrationID != "" {
		return ids, nil
	}
	for _, rec := range archive.Records {
		if ids[rec.Collection][rec.RecordID] != "" {
			continue
		}
		conflict, err := a.records.ArchiveRecordIDOccupiedTx(ctx, tx, rec.Collection, target, rec.RecordID)
		if err != nil {
			return nil, err
		}
		if !conflict {
			continue
		}
		replacement := restoreTargetRecordID(archive.AgentName, target, rec.Collection, rec.RecordID)
		occupied, err := a.records.ArchiveRecordIDOccupiedTx(ctx, tx, rec.Collection, "", replacement)
		if err != nil {
			return nil, err
		}
		if occupied {
			return nil, fmt.Errorf("%w: restore record ID is already occupied", records.ErrVersionConflict)
		}
		if ids[rec.Collection] == nil {
			ids[rec.Collection] = map[string]string{}
		}
		ids[rec.Collection][rec.RecordID] = replacement
	}
	return ids, nil
}

func restoreTargetRecordID(source, target, collection, id string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + target + "\x00" + collection + "\x00" + id))
	return "restored-" + hex.EncodeToString(sum[:16])
}
