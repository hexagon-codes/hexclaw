package k12storage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/knowledge"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
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
	var ingestCount, originalGeneration int
	if err := db.QueryRow(`SELECT COUNT(*),MIN(document_generation) FROM kb_knowledge_jobs WHERE kind='ingest' AND document_id='catalog-doc'`).Scan(&ingestCount, &originalGeneration); err != nil || ingestCount != 1 || originalGeneration != 1 {
		t.Fatalf("fake new ingest introduced: count=%d generation=%d err=%v", ingestCount, originalGeneration, err)
	}
	var originalDigest, originalCatalog string
	if err := db.QueryRow(`SELECT source_digest,COALESCE(catalog_json,'') FROM k12_textbook_manifests WHERE manifest_id='catalog-manifest'`).Scan(&originalDigest, &originalCatalog); err != nil || originalDigest != strings.Repeat("a", 64) || originalCatalog != "" {
		t.Fatal("historical directory content changed")
	}
	if _, err := db.Exec(`UPDATE kb_documents SET content=content||' changed' WHERE id='catalog-doc'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTextbookCatalogSource(ctx, claim, now.Add(2*time.Second)); err == nil {
		t.Fatal("different current content borrowed original parse")
	}
}
