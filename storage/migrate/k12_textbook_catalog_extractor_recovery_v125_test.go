package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestK12TextbookCatalogRecoveryV125PreservesFactsAndAdmitsV4(t *testing.T) {
	db := newV124CatalogRecoveryFixture(t, "checkpoint-toc-footer-v4")
	before := catalogRecoveryBusinessSnapshot(t, db)
	if err := Run(context.Background(), db, All); err != nil {
		t.Fatal(err)
	}
	if after := catalogRecoveryBusinessSnapshot(t, db); after != before {
		t.Fatal("V125 changed existing V124 business facts or table schema")
	}
	if _, err := db.Exec(catalogRecoveryV125Update, "checkpoint-toc-footer-v5", strings.Repeat("d", 64)); err != nil {
		t.Fatalf("same-source terminal evidence failure must recover to v5: %v", err)
	}
	var state, contract, ingest, plan, request string
	var attempt, nextAttempt, leaseExpires int64
	if err := db.QueryRow(`SELECT state,extractor_contract,ingest_job_id,source_plan_digest,
		request_digest,attempt,next_attempt_at,lease_expires_at
		FROM k12_textbook_catalog_jobs WHERE job_id='migration-catalog'`).Scan(
		&state, &contract, &ingest, &plan, &request, &attempt, &nextAttempt, &leaseExpires); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || contract != "checkpoint-toc-footer-v5" || ingest != "migration-ingest" ||
		plan != strings.Repeat("b", 64) || request != strings.Repeat("d", 64) ||
		attempt != 0 || nextAttempt != 0 || leaseExpires != 0 {
		t.Fatalf("recovery changed source or retained retry state: %s %s %s %s %s %d %d %d",
			state, contract, ingest, plan, request, attempt, nextAttempt, leaseExpires)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 125 {
		t.Fatalf("registered migration version=%d err=%v", version, err)
	}
}

func TestK12TextbookCatalogRecoveryV125KeepsFrozenSourceProtections(t *testing.T) {
	db := newV124CatalogRecoveryFixture(t, "checkpoint-toc-footer-v4")
	if err := Run(context.Background(), db, All); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, oldMutation, newFields, request string
	}{
		{name: "different ingest", newFields: ",ingest_job_id='other-ingest'"},
		{name: "different source plan", newFields: ",source_plan_digest='" + strings.Repeat("e", 64) + "'"},
		{name: "existing result", oldMutation: "result_digest='" + strings.Repeat("e", 64) + "'"},
		{name: "other failure", oldMutation: "failure_code='source_evidence_incomplete'"},
		{name: "active job", oldMutation: "state='running'"},
		{name: "attempt not reset", newFields: ",attempt=1"},
		{name: "lease not reset", newFields: ",lease_owner='stale-worker'"},
		{name: "unchanged request digest", request: strings.Repeat("c", 64)},
		{name: "incomplete request digest", request: "short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`SAVEPOINT recovery_case`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.Exec(`ROLLBACK TO recovery_case`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`RELEASE recovery_case`); err != nil {
					t.Fatal(err)
				}
			}()
			if tc.oldMutation != "" {
				if _, err := db.Exec(`UPDATE k12_textbook_catalog_jobs SET ` + tc.oldMutation + ` WHERE job_id='migration-catalog'`); err != nil {
					t.Fatal(err)
				}
			}
			before := catalogRecoveryBusinessSnapshot(t, db)
			request := tc.request
			if request == "" {
				request = strings.Repeat("d", 64)
			}
			query := strings.Replace(catalogRecoveryV125Update, " WHERE job_id=", tc.newFields+" WHERE job_id=", 1)
			if _, err := db.Exec(query, "checkpoint-toc-footer-v5", request); err == nil ||
				!strings.Contains(err.Error(), "textbook catalog source snapshot is immutable") {
				t.Fatalf("unproved recovery must be rejected by the source guard: %v", err)
			}
			if after := catalogRecoveryBusinessSnapshot(t, db); after != before {
				t.Fatal("rejected recovery changed persisted business facts")
			}
		})
	}
}

func TestK12TextbookCatalogRecoveryV125RetainsPreviousRecoveryBranches(t *testing.T) {
	for _, route := range [][2]string{
		{"checkpoint-toc-footer-v1", "checkpoint-toc-footer-v2"},
		{"checkpoint-toc-footer-v2", "checkpoint-toc-footer-v3"},
		{"checkpoint-toc-footer-v1", "checkpoint-toc-footer-v4"},
		{"checkpoint-toc-footer-v3", "checkpoint-toc-footer-v4"},
	} {
		t.Run(route[0]+" to "+route[1], func(t *testing.T) {
			db := newV124CatalogRecoveryFixture(t, route[0])
			if err := Run(context.Background(), db, All); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(catalogRecoveryV125Update, route[1], strings.Repeat("d", 64)); err != nil {
				t.Fatalf("previous same-source recovery branch was lost: %v", err)
			}
		})
	}
}

const catalogRecoveryV125Update = `UPDATE k12_textbook_catalog_jobs
	SET extractor_contract=?,request_digest=?,state='queued',failure_code='',last_error='',
	    result_digest='',attempt=0,next_attempt_at=0,lease_owner='',lease_expires_at=0 WHERE job_id='migration-catalog'`

func newV124CatalogRecoveryFixture(t *testing.T, contract string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "catalog-v124.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	legacy := make([]Migration, 0)
	for _, migration := range All {
		if migration.Version <= 124 {
			legacy = append(legacy, migration)
		}
	}
	if err := Run(context.Background(), db, legacy); err != nil {
		t.Fatal(err)
	}
	const content = "original checkpoint\n155"
	sourceDigest := strings.Repeat("a", 64)
	for index, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO agents(name) VALUES('confirmed-fixture'),('estimated-fixture')`, nil},
		{`INSERT INTO kb_semantic_corpora(corpus_uid,owner_id,corpus_alias,kind,content_version,created_at,updated_at)
			VALUES('migration-corpus','desktop-user','default','general',1,1,1)`, nil},
		{`INSERT INTO kb_documents(id,title,content,source,deleted,corpus_uid,created_at,updated_at)
			VALUES('migration-doc','fixture.pdf',?,'upload:fixture.pdf',0,'migration-corpus',1,1)`, []any{content}},
		{`INSERT INTO kb_semantic_document_generations(owner_id,corpus_uid,document_id,content_generation,created_at)
			VALUES('desktop-user','migration-corpus','migration-doc',1,1)`, nil},
		{`INSERT INTO kb_chunks(id,doc_id,content,chunk_index,created_at,page_start,page_end,source_digest,source_offset_start,source_offset_end)
			VALUES('migration-chunk','migration-doc',?,0,1,1,1,?,0,?)`, []any{content, sourceDigest, len(content)}},
		{`INSERT INTO kb_knowledge_jobs(job_id,parent_job_id,kind,owner_id,corpus_uid,document_id,
			 document_generation,target_revision_id,idempotency_key,state,stage,attempt,cancel_requested,
			 lease_owner,lease_epoch,last_error,created_at,updated_at,finished_at,pages_total,pages_done)
			VALUES('migration-ingest',NULL,'ingest','desktop-user','migration-corpus','migration-doc',
			 1,NULL,'migration-ingest','succeeded','publishing',1,0,'',1,'',1,1,1,1,1)`, nil},
		{`INSERT INTO kb_ingest_page_checkpoints(job_id,page_number,pages_total,source_digest,extraction_mode,
			 content,content_digest,source_offset_start,source_offset_end,lease_epoch,created_at,updated_at)
			VALUES('migration-ingest',1,1,?,'ocr_vlm',?,?,0,?,1,1,1)`,
			[]any{sourceDigest, content, fmt.Sprintf("%x", sha256.Sum256([]byte(content))), len(content)}},
		{`INSERT INTO k12_curriculum_progress(progress_id,agent_name,subject,revision,textbook_binding_id,
			 textbook_edition,textbook_version,title,volume,unit_id,unit_title,page_verification_status,
			 evidence_source,confirmed_at,created_at,updated_at)
			VALUES('confirmed-progress','confirmed-fixture','math',1,'legacy-binding','人教版','2025',
			 'fixture','上册','u1','unit','not_requested','parent_confirmed',1,1,2),
			 ('estimated-progress','estimated-fixture','math',1,'legacy-binding','人教版','2025',
			 'fixture','上册','u1','unit','not_requested','ai_estimated',0,1,2)`, nil},
		{`INSERT INTO k12_curriculum_progress_revisions(agent_name,subject,revision,updated_at)
			VALUES('confirmed-fixture','math',1,2),('estimated-fixture','math',1,2)`, nil},
		{`INSERT INTO k12_textbook_manifests(manifest_id,owner_id,document_id,document_generation,document_title,
			 subject,source_digest,state,retryable,failure_message,text_index_state,vector_index_state,
			 catalog_json,catalog_digest,created_at,updated_at)
			VALUES('migration-manifest','desktop-user','migration-doc',1,'fixture.pdf','math',?,
			 'failed_terminal',0,'catalog evidence incomplete','ready','ready',NULL,NULL,1,1)`, []any{sourceDigest}},
		{`INSERT INTO k12_textbook_catalog_jobs(job_id,manifest_id,owner_id,document_id,document_generation,
			 source_digest,state,attempt,lease_owner,lease_epoch,lease_expires_at,request_digest,result_digest,
			 last_error,created_at,updated_at,ingest_job_id,source_plan_digest,extractor_contract,
			 next_attempt_at,heartbeat_at,failure_code)
			VALUES('migration-catalog','migration-manifest','desktop-user','migration-doc',1,?,
			 'failed_terminal',3,'stale-worker',1,19,?,'','conflicting footer',1,2,
			 'migration-ingest',?,?,9,1,'catalog_evidence_incomplete')`,
			[]any{sourceDigest, strings.Repeat("c", 64), strings.Repeat("b", 64), contract}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed V124 recovery fixture statement %d: %v", index, err)
		}
	}
	return db
}

func catalogRecoveryBusinessSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	snapshot := make(map[string][]string)
	for _, table := range []string{"agents", "kb_semantic_corpora", "kb_documents", "kb_semantic_document_generations", "kb_chunks",
		"kb_knowledge_jobs", "kb_ingest_page_checkpoints", "k12_curriculum_progress",
		"k12_curriculum_progress_revisions", "k12_textbook_manifests", "k12_textbook_catalog_jobs", "table_schema"} {
		query := `SELECT * FROM ` + table
		if table == "table_schema" {
			query = `SELECT name,sql FROM sqlite_master WHERE type='table' ORDER BY name`
		}
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columnNames, _ := json.Marshal(columns)
		snapshot[table] = []string{string(columnNames)}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot[table] = append(snapshot[table], string(encoded))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(snapshot[table][1:])
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
