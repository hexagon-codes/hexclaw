package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// K12CurriculumEstimateV124 在同一进度表区分建议与人工确认，旧数据逐列原样迁移。
var K12CurriculumEstimateV124 = Migration{Version: 124, Description: "K12 curriculum recommendation provenance", AtomicFunc: migrateK12CurriculumEstimateV124}

func migrateK12CurriculumEstimateV124(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	start := strings.Index(K12WeeklyPracticeV52DDL, "CREATE TABLE IF NOT EXISTS k12_curriculum_progress (")
	end := strings.Index(K12WeeklyPracticeV52DDL[start:], "\n);") + start + 3
	ddl := strings.Replace(K12WeeklyPracticeV52DDL[start:end], "k12_curriculum_progress (", "k12_curriculum_progress_next (", 1)
	ddl = strings.Replace(ddl, "CHECK(evidence_source='parent_confirmed')", "CHECK(evidence_source IN ('parent_confirmed','ai_estimated'))", 1)
	ddl = strings.Replace(ddl, "confirmed_at INTEGER NOT NULL CHECK(confirmed_at > 0)", "confirmed_at INTEGER NOT NULL CHECK((evidence_source='parent_confirmed' AND confirmed_at>0) OR (evidence_source='ai_estimated' AND confirmed_at=0))", 1)
	ddl = strings.Replace(ddl, "    UNIQUE(agent_name,subject),", "    textbook_manifest_id TEXT NOT NULL DEFAULT '',\n    grade_term TEXT NOT NULL DEFAULT '',\n    estimate_basis_json TEXT CHECK(estimate_basis_json IS NULL OR json_valid(estimate_basis_json)),\n    UNIQUE(agent_name,subject),", 1)
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create curriculum provenance table: %w", err)
	}
	columns := `progress_id,agent_name,subject,revision,textbook_binding_id,textbook_edition,textbook_version,title,volume,unit_id,unit_title,lesson_id,lesson_title,requested_page_from,requested_page_to,verified_page_from,verified_page_to,page_verification_status,segment_refs_json,evidence_source,confirmed_at,created_at,updated_at,textbook_manifest_id`
	if _, err := tx.ExecContext(ctx, "INSERT INTO k12_curriculum_progress_next("+columns+") SELECT "+columns+" FROM k12_curriculum_progress"); err != nil {
		return fmt.Errorf("preserve curriculum provenance rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE k12_curriculum_progress; ALTER TABLE k12_curriculum_progress_next RENAME TO k12_curriculum_progress; CREATE INDEX idx_k12_curriculum_progress_manifest ON k12_curriculum_progress(textbook_manifest_id);`); err != nil {
		return err
	}
	if err := recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
