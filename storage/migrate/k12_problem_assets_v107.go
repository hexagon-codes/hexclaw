package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// K12ProblemAssetsV107 保存题目资产与采用来源，保留历史批改回执及其外键。
var K12ProblemAssetsV107 = Migration{
	Version:     107,
	Description: "K12 题目资产不可变版本、发布与采用回执",
	AtomicFunc:  migrateK12ProblemAssetsV107,
}

const k12ProblemAssetsV107DDL = `
CREATE TABLE k12_problem_assets (
    asset_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL CHECK(length(trim(owner_id))>0),
    normalization_version TEXT NOT NULL,
    facts_digest TEXT NOT NULL,
    current_version INTEGER NOT NULL CHECK(current_version>=1),
    revision INTEGER NOT NULL CHECK(revision>=1),
    status TEXT NOT NULL CHECK(status IN ('active','archived')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(owner_id,normalization_version,facts_digest),
    UNIQUE(owner_id,asset_id)
);
CREATE TABLE k12_problem_asset_versions (
    owner_id TEXT NOT NULL,
    asset_id TEXT NOT NULL,
    asset_version INTEGER NOT NULL CHECK(asset_version>=1),
    published_revision INTEGER NOT NULL CHECK(published_revision>=1),
    facts_json TEXT NOT NULL CHECK(json_valid(facts_json)),
    facts_digest TEXT NOT NULL,
    answer TEXT NOT NULL CHECK(length(trim(answer))>0),
    answer_result_json TEXT NOT NULL CHECK(json_valid(answer_result_json)),
    created_at INTEGER NOT NULL,
    PRIMARY KEY(owner_id,asset_id,asset_version),
    FOREIGN KEY(owner_id,asset_id) REFERENCES k12_problem_assets(owner_id,asset_id)
);
CREATE TABLE k12_problem_asset_publications (
    owner_id TEXT NOT NULL,
    publication_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    asset_id TEXT NOT NULL,
    asset_version INTEGER NOT NULL,
    verification_json TEXT NOT NULL CHECK(json_valid(verification_json)),
    invocation_json TEXT NOT NULL CHECK(json_valid(invocation_json)),
    created_at INTEGER NOT NULL,
    PRIMARY KEY(owner_id,publication_id),
    FOREIGN KEY(owner_id,asset_id,asset_version)
        REFERENCES k12_problem_asset_versions(owner_id,asset_id,asset_version)
);
CREATE TABLE k12_problem_asset_adoptions (
    adoption_id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    input_revision INTEGER NOT NULL CHECK(input_revision>=1),
    input_digest TEXT NOT NULL,
    asset_id TEXT NOT NULL,
    asset_version INTEGER NOT NULL,
    asset_revision INTEGER NOT NULL CHECK(asset_revision>=1),
    facts_digest TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE(owner_id,job_id,problem_id,input_revision),
    FOREIGN KEY(owner_id,asset_id,asset_version)
        REFERENCES k12_problem_asset_versions(owner_id,asset_id,asset_version)
);
CREATE TRIGGER k12_problem_asset_versions_immutable
BEFORE UPDATE ON k12_problem_asset_versions BEGIN SELECT RAISE(ABORT,'problem asset versions are immutable'); END;
CREATE TRIGGER k12_problem_asset_publications_immutable
BEFORE UPDATE ON k12_problem_asset_publications BEGIN SELECT RAISE(ABORT,'problem asset publications are immutable'); END;
CREATE TRIGGER k12_problem_asset_adoptions_immutable
BEFORE UPDATE ON k12_problem_asset_adoptions BEGIN SELECT RAISE(ABORT,'problem asset adoptions are immutable'); END;
`

const k12AssetAssessmentV107DDL = `
CREATE TABLE k12_grading_assessment_items_v107 (
    agent_name TEXT NOT NULL,
    job_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    confirmed_version INTEGER NOT NULL CHECK(confirmed_version >= 1),
    input_revision INTEGER NOT NULL DEFAULT 1 CHECK(input_revision >= 1),
    published_revision INTEGER NOT NULL DEFAULT 1 CHECK(published_revision >= 1),
    current_disposition TEXT NOT NULL DEFAULT 'current'
        CHECK(current_disposition IN ('current','superseded')),
    structure_version INTEGER NOT NULL DEFAULT 1 CHECK(structure_version >= 1),
    input_digest TEXT NOT NULL CHECK(input_digest != ''),
    status TEXT NOT NULL CHECK(status IN
        ('correct','correct_with_process_issue','wrong','unanswered','answer_unclear',
         'blank_solved','out_of_scope','untrusted')),
    result_json TEXT NOT NULL CHECK(result_json != ''),
    result_digest TEXT NOT NULL CHECK(result_digest != ''),
    solve_invocation_id TEXT,
    answer_source_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(answer_source_json)),
    grade_invocation_id TEXT,
    parent_guide_invocation_id TEXT,
    projection_record_id TEXT NOT NULL DEFAULT '',
    projection_created INTEGER NOT NULL DEFAULT 0 CHECK(projection_created IN (0,1)),
    projection_status TEXT NOT NULL CHECK(projection_status = 'committed'),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(job_id, problem_id, input_revision),
    UNIQUE(job_id, problem_id, published_revision),
    FOREIGN KEY(agent_name, job_id)
        REFERENCES k12_grading_jobs(agent_name, record_id) ON DELETE CASCADE,
    FOREIGN KEY(agent_name, problem_id)
        REFERENCES k12_problems(agent_name, problem_id) ON DELETE CASCADE,
    FOREIGN KEY(agent_name, attempt_id, problem_id)
        REFERENCES k12_attempts(agent_name, attempt_id, problem_id) ON DELETE CASCADE,
    FOREIGN KEY(solve_invocation_id)
        REFERENCES k12_grading_item_invocations(item_invocation_id),
    FOREIGN KEY(grade_invocation_id)
        REFERENCES k12_grading_item_invocations(item_invocation_id),
    FOREIGN KEY(parent_guide_invocation_id)
        REFERENCES k12_grading_item_invocations(item_invocation_id),
    CHECK(COALESCE(json_extract(answer_source_json,'$.kind'),'')!='asset' OR solve_invocation_id IS NULL),
    CHECK(projection_created = 0 OR projection_record_id != ''),
    CHECK(parent_guide_invocation_id IS NULL OR
        status IN ('wrong','blank_solved','correct_with_process_issue')),
    CHECK(
        (status IN ('correct','wrong','untrusted') AND
            (solve_invocation_id IS NOT NULL OR COALESCE(json_extract(answer_source_json,'$.kind'),'')='asset') AND grade_invocation_id IS NOT NULL)
        OR (status = 'correct_with_process_issue' AND
            (solve_invocation_id IS NOT NULL OR COALESCE(json_extract(answer_source_json,'$.kind'),'')='asset') AND grade_invocation_id IS NOT NULL AND
            parent_guide_invocation_id IS NOT NULL)
        OR (status = 'blank_solved' AND
            (solve_invocation_id IS NOT NULL OR COALESCE(json_extract(answer_source_json,'$.kind'),'')='asset') AND grade_invocation_id IS NULL)
        OR (status IN ('unanswered','answer_unclear') AND
            solve_invocation_id IS NULL AND grade_invocation_id IS NULL AND answer_source_json='{}')
        OR (status = 'out_of_scope' AND grade_invocation_id IS NULL)
    )
);`

func migrateK12ProblemAssetsV107(ctx context.Context, db *sql.DB, recordVersion func(context.Context, *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, k12ProblemAssetsV107DDL); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, k12AssetAssessmentV107DDL); err != nil {
		return err
	}
	// 原始结果和调用身份逐列保留，新来源仅供后续提交使用。
	columns := `agent_name,job_id,problem_id,attempt_id,confirmed_version,input_revision,published_revision,current_disposition,structure_version,input_digest,status,result_json,result_digest,solve_invocation_id,grade_invocation_id,parent_guide_invocation_id,projection_record_id,projection_created,projection_status,created_at,updated_at`
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_grading_assessment_items_v107 (`+columns+`) SELECT `+columns+` FROM k12_grading_assessment_items`); err != nil {
		return err
	}
	for _, statement := range []string{
		`DROP TABLE k12_grading_assessment_items`,
		`ALTER TABLE k12_grading_assessment_items_v107 RENAME TO k12_grading_assessment_items`,
		`CREATE INDEX idx_k12_grading_assessment_items_job ON k12_grading_assessment_items(agent_name,job_id,problem_id,published_revision)`,
		`CREATE UNIQUE INDEX idx_k12_grading_assessment_items_current ON k12_grading_assessment_items(agent_name,job_id,problem_id) WHERE current_disposition='current'`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var violations int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("asset migration found %d foreign key conflicts", violations)
	}
	if err = recordVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
