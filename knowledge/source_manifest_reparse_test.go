package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type structuredReparseFixture struct{}

func (structuredReparseFixture) Prepare(ctx context.Context, source PersistedIngestDocument) (PreparedIngestDocument, error) {
	p, err := (reparseFixtureProcessor{"newcobalt lesson"}).Prepare(ctx, source)
	p.SourceManifest = &SourceManifest{SchemaVersion: 1, ParserVersion: "fixture-structure-v1", SourceDigest: source.SHA256, Blocks: []SourceContentBlock{{ID: "new-paragraph", Kind: "paragraph", Text: "newcobalt lesson"}}}
	return p, err
}

func TestSourceManifestReparsePublishesMatchingGeneration(t *testing.T) {
	h, old, worker, _ := newReparseFixture(t, true)
	if err := migrate.Run(h.ctx, h.db, []migrate.Migration{migrate.KnowledgeSourceManifestV112}); err != nil {
		t.Fatal(err)
	}
	_, err := h.db.Exec(`INSERT INTO kb_ingest_source_manifests(owner_id,document_id,content_generation,source_digest,manifest_json,created_at) VALUES('owner-1',?,1,'old-source','{"schema_version":1,"blocks":[{"block_id":"old-paragraph","text":"oldquartz lesson"}]}',1)`, old.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.service.ReparseDocument(h.ctx, "owner-1", "default", old.DocumentID, "structured-upgrade", 1); err != nil {
		t.Fatal(err)
	}
	worker.SetDocumentIngestProcessor(structuredReparseFixture{})
	if did, err := worker.RunOnce(h.ctx); err != nil || !did {
		t.Fatalf("prepare %v %v", did, err)
	}
	current := func(wantGeneration int, wantBlock string) {
		t.Helper()
		var generation int
		var block string
		if err := h.db.QueryRow(`SELECT b.content_generation,json_extract(m.manifest_json,'$.blocks[0].block_id') FROM kb_semantic_document_bindings b JOIN kb_ingest_source_manifests m ON m.document_id=b.document_id AND m.owner_id=b.owner_id AND m.content_generation=b.content_generation WHERE b.document_id=?`, old.DocumentID).Scan(&generation, &block); err != nil || generation != wantGeneration || block != wantBlock {
			t.Fatalf("current generation=%d block=%s err=%v", generation, block, err)
		}
	}
	current(1, "old-paragraph")
	var snapshots int
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_source_manifests WHERE document_id=?`, old.DocumentID).Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatalf("candidate snapshot missing %d %v", snapshots, err)
	}
	source, err := h.repo.GetIngestDocument(h.ctx, "owner-1", old.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := h.db.BeginTx(h.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = saveSourceManifestTx(h.ctx, tx, KnowledgeJob{OwnerID: "owner-1", DocumentID: old.DocumentID, DocumentGeneration: 2}, source, &SourceManifest{SchemaVersion: 1, ParserVersion: "changed-parser", Blocks: []SourceContentBlock{{ID: "late-block", Text: "late result"}}}, 1)
	_ = tx.Rollback()
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same revision replaced by late parse: %v", err)
	}
	if did, err := worker.RunOnce(h.ctx); err != nil || !did {
		t.Fatalf("publish %v %v", did, err)
	}
	current(2, "new-paragraph")
	var oldBlock string
	if err = h.db.QueryRow(`SELECT json_extract(manifest_json,'$.blocks[0].block_id') FROM kb_ingest_source_manifests WHERE document_id=? AND content_generation=1`, old.DocumentID).Scan(&oldBlock); err != nil || oldBlock != "old-paragraph" {
		t.Fatalf("old snapshot changed %s %v", oldBlock, err)
	}
}
