package migrate

import (
	"context"
	"strings"
	"testing"
)

func TestK12TextbookCatalogRecoveryV146PreservesFactsAndAdmitsKnownContracts(t *testing.T) {
	for _, oldContract := range []string{
		"checkpoint-toc-footer-v1", "checkpoint-toc-footer-v2", "checkpoint-toc-footer-v3",
		"checkpoint-toc-footer-v4", "checkpoint-toc-footer-v5",
	} {
		t.Run(oldContract, func(t *testing.T) {
			db := newV124CatalogRecoveryFixture(t, oldContract)
			before := catalogRecoveryBusinessSnapshot(t, db)
			if err := Run(context.Background(), db, []Migration{K12TextbookCatalogExtractorRecoveryV125, K12TextbookCatalogExtractorRecoveryV146}); err != nil {
				t.Fatal(err)
			}
			if after := catalogRecoveryBusinessSnapshot(t, db); after != before {
				t.Fatal("V146 changed existing business facts")
			}
			if _, err := db.Exec(catalogRecoveryV125Update, "checkpoint-toc-footer-v6", strings.Repeat("d", 64)); err != nil {
				t.Fatalf("same-source terminal evidence failure must recover to v6: %v", err)
			}
			var state, contract, ingest, plan string
			if err := db.QueryRow(`SELECT state,extractor_contract,ingest_job_id,source_plan_digest
				FROM k12_textbook_catalog_jobs WHERE job_id='migration-catalog'`).Scan(&state, &contract, &ingest, &plan); err != nil {
				t.Fatal(err)
			}
			if state != "queued" || contract != "checkpoint-toc-footer-v6" || ingest != "migration-ingest" || plan != strings.Repeat("b", 64) {
				t.Fatalf("recovery changed source facts: %s %s %s %s", state, contract, ingest, plan)
			}
		})
	}
}

func TestK12TextbookCatalogRecoveryV146KeepsFrozenSourceProtections(t *testing.T) {
	db := newV124CatalogRecoveryFixture(t, "checkpoint-toc-footer-v5")
	if err := Run(context.Background(), db, []Migration{K12TextbookCatalogExtractorRecoveryV125, K12TextbookCatalogExtractorRecoveryV146}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, oldMutation, newFields string }{
		{name: "ready", oldMutation: "state='succeeded',result_digest='" + strings.Repeat("e", 64) + "'"},
		{name: "different source", newFields: ",source_plan_digest='" + strings.Repeat("e", 64) + "'"},
		{name: "existing result", oldMutation: "result_digest='" + strings.Repeat("e", 64) + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec("SAVEPOINT recovery_case"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.Exec("ROLLBACK TO recovery_case"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("RELEASE recovery_case"); err != nil {
					t.Fatal(err)
				}
			}()
			if tc.oldMutation != "" {
				if _, err := db.Exec("UPDATE k12_textbook_catalog_jobs SET " + tc.oldMutation + " WHERE job_id='migration-catalog'"); err != nil {
					t.Fatal(err)
				}
			}
			before := catalogRecoveryBusinessSnapshot(t, db)
			query := strings.Replace(catalogRecoveryV125Update, " WHERE job_id=", tc.newFields+" WHERE job_id=", 1)
			if _, err := db.Exec(query, "checkpoint-toc-footer-v6", strings.Repeat("d", 64)); err == nil || !strings.Contains(err.Error(), "textbook catalog source snapshot is immutable") {
				t.Fatalf("invalid recovery must retain frozen source protection: %v", err)
			}
			if after := catalogRecoveryBusinessSnapshot(t, db); after != before {
				t.Fatal("rejected recovery changed persisted business facts")
			}
		})
	}
}
