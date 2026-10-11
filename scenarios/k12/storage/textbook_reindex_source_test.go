package k12storage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/records"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestSourceReindexCatalogUsesRealOriginalParseWithoutRewritingHistory(t *testing.T) {
	store, _, _ := seedTextbookCatalogMaterialization(t)
	db := store.DB()
	ctx := context.Background()
	rows, err := db.Query(`SELECT page_number,content FROM kb_ingest_page_checkpoints WHERE job_id='catalog-ingest' ORDER BY page_number`)
	if err != nil {
		t.Fatal(err)
	}
	type page struct {
		number int
		text   string
	}
	pages := []page{}
	for rows.Next() {
		var p page
		if err := rows.Scan(&p.number, &p.text); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p)
	}
	rows.Close()
	var canonical strings.Builder
	for _, p := range pages {
		if canonical.Len() > 0 {
			canonical.WriteString("\n\n")
		}
		canonical.WriteString(knowledge.CanonicalPDFPagePrefix(p.number, p.number, p.number))
		start := canonical.Len()
		canonical.WriteString(p.text)
		if _, err := db.Exec(`UPDATE kb_ingest_page_checkpoints SET source_offset_start=?,source_offset_end=? WHERE job_id='catalog-ingest' AND page_number=?`, start, canonical.Len(), p.number); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE kb_documents SET content=?,status='indexed',source='upload:数学五年级下册.pdf' WHERE id='catalog-doc'`, canonical.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM k12_textbook_catalog_jobs WHERE job_id='catalog-job'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kb_embedding_policies(corpus_uid,selection_kind,version,updated_at) VALUES('catalog-corpus','disabled',1,1)`); err != nil {
		t.Fatal(err)
	}
	kb := knowledge.NewSQLiteStore(db, knowledge.WithSQLiteSemanticMutations("desktop-user", "default"))
	doc, err := kb.Get(ctx, "catalog-doc")
	if err != nil {
		t.Fatal(err)
	}
	chunks := []*knowledge.Chunk{{ID: "current-segment-3", DocID: doc.ID, Content: pages[2].text, Index: 0, CreatedAt: time.Now()}}
	doc.ChunkCount = 1
	if err := kb.Replace(ctx, doc, chunks); err != nil {
		t.Fatal(err)
	}
	options, err := store.ListTextbookBindingOptions(ctx, k12storage.TextbookScope{OwnerID: "desktop-user", AgentName: "mingming", Subject: "math"})
	if err != nil {
		t.Fatal(err)
	}
	currentManifest := ""
	for _, option := range options {
		if option.DocumentGeneration == 2 {
			currentManifest = option.ManifestID
		}
	}
	if currentManifest == "" {
		t.Fatalf("current source candidate missing: %+v", options)
	}
	now := time.Now().UTC()
	claim, found, err := store.ClaimTextbookCatalogJob(ctx, "current-source-worker", now, 30*time.Second)
	if err != nil || !found || claim.ManifestID != currentManifest || claim.IngestJobID != "catalog-ingest" {
		t.Fatalf("current catalog not pinned to actual original parse: %+v found=%v err=%v", claim, found, err)
	}
	source, err := store.LoadTextbookCatalogSource(ctx, claim, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if source.IngestJobID != "catalog-ingest" || len(source.Pages) != 3 || len(source.Pages[2].SegmentRefs) != 1 || source.Pages[2].SegmentRefs[0] != "current-segment-3" {
		t.Fatalf("current proof lost real pages/chunks: %+v", source)
	}
	publication, err := (usecase.TextbookCatalogCheckpointExtractor{}).Extract(ctx, source)
	if err != nil {
		t.Fatalf("actual pure extractor failed: %v", err)
	}
	if _, err := db.Exec(`UPDATE kb_documents SET content=content||' changed' WHERE id='catalog-doc'`); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishTextbookCatalog(ctx, claim, publication, now.Add(2*time.Second)); err == nil {
		t.Fatal("publication borrowed original parse after current content drift")
	}
	var mappingCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM k12_textbook_page_mappings WHERE manifest_id=?`, claim.ManifestID).Scan(&mappingCount); err != nil || mappingCount != 0 {
		t.Fatal("rejected drift partially published mappings")
	}
	if _, err := db.Exec(`UPDATE kb_documents SET content=? WHERE id='catalog-doc'`, canonical.String()); err != nil {
		t.Fatal(err)
	}
	knownFailure := fmt.Errorf("%w: textbook page checkpoint is missing", records.ErrIllegalTransition).Error()
	if err := store.FailTextbookCatalogJob(ctx, claim, k12storage.TextbookCatalogFailure{Code: "catalog_evidence_incomplete", Message: knownFailure, Terminal: true}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	failedClaim := claim
	failedClaim.LeaseOwner = ""
	failedClaim.LeaseExpiresAtMilli = 0
	for _, bad := range []k12storage.TextbookCatalogJobClaim{func() k12storage.TextbookCatalogJobClaim { c := failedClaim; c.LeaseEpoch++; return c }(), func() k12storage.TextbookCatalogJobClaim {
		c := failedClaim
		c.SourcePlanDigest = strings.Repeat("0", 64)
		return c
	}(), func() k12storage.TextbookCatalogJobClaim { c := failedClaim; c.OwnerID = "other-owner"; return c }()} {
		if err := store.RecoverSourceReindexCatalogPublication(ctx, bad, now.Add(4*time.Second), false); !errors.Is(err, k12storage.ErrTextbookCatalogJobFenced) {
			t.Fatalf("invalid recovery CAS accepted: %v", err)
		}
	}
	if err := store.RecoverSourceReindexCatalogPublication(ctx, failedClaim, now.Add(4*time.Second), true); err != nil {
		t.Fatal(err)
	}
	var dryState string
	if err := db.QueryRow(`SELECT state FROM k12_textbook_catalog_jobs WHERE job_id=?`, claim.JobID).Scan(&dryState); err != nil || dryState != "failed_terminal" {
		t.Fatal("catalog recovery dry-run wrote state")
	}
	if err := store.RecoverSourceReindexCatalogPublication(ctx, failedClaim, now.Add(4*time.Second), false); err != nil {
		t.Fatal(err)
	}
	var retainedError, retainedCode string
	var retainedAttempt int
	if err := db.QueryRow(`SELECT last_error,failure_code,attempt FROM k12_textbook_catalog_jobs WHERE job_id=?`, claim.JobID).Scan(&retainedError, &retainedCode, &retainedAttempt); err != nil || retainedError != knownFailure || retainedCode != "catalog_evidence_incomplete" || retainedAttempt != claim.Attempt {
		t.Fatal("known failure evidence was cleared by recovery")
	}
	if err := store.RecoverSourceReindexCatalogPublication(ctx, failedClaim, now.Add(5*time.Second), false); !errors.Is(err, k12storage.ErrTextbookCatalogJobFenced) {
		t.Fatal("same failed generation recovered twice")
	}
	claim, found, err = store.ClaimTextbookCatalogJob(ctx, "source-worker-recovered", now.Add(5*time.Second), 30*time.Second)
	if err != nil || !found || claim.IngestJobID != "catalog-ingest" || claim.DocumentGeneration != 2 {
		t.Fatalf("recovery switched source task: %+v %v", claim, err)
	}
	if err := store.PublishTextbookCatalog(ctx, claim, publication, now.Add(6*time.Second)); err != nil {
		t.Fatalf("real reindexed publication failed: %v", err)
	}
	var currentState string
	if err := db.QueryRow(`SELECT state FROM k12_textbook_manifests WHERE manifest_id=?`, claim.ManifestID).Scan(&currentState); err != nil || currentState != "ready_for_confirmation" {
		t.Fatalf("current directory did not publish: %s %v", currentState, err)
	}
	var ingestCount, originalGeneration int
	if err := db.QueryRow(`SELECT COUNT(*),MIN(document_generation) FROM kb_knowledge_jobs WHERE kind='ingest' AND document_id='catalog-doc'`).Scan(&ingestCount, &originalGeneration); err != nil || ingestCount != 1 || originalGeneration != 1 {
		t.Fatalf("fake new ingest introduced: count=%d generation=%d err=%v", ingestCount, originalGeneration, err)
	}
	var originalDigest, originalCatalog string
	if err := db.QueryRow(`SELECT source_digest,COALESCE(catalog_json,'') FROM k12_textbook_manifests WHERE manifest_id='catalog-manifest'`).Scan(&originalDigest, &originalCatalog); err != nil || originalDigest != strings.Repeat("a", 64) || originalCatalog != "" {
		t.Fatal("historical directory content changed")
	}
}
