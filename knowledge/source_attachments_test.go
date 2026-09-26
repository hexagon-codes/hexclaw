package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func TestSourceAttachmentsSharedBytesSurviveRestartAndOtherSourceGC(t *testing.T) {
	h := newSemanticMutationHarness(t)
	if err := migrate.Run(h.ctx, h.db, []migrate.Migration{migrate.KnowledgeSourceManifestV112}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "objects")
	if err := h.service.ConfigureDocumentIngest(root); err != nil {
		t.Fatal(err)
	}
	var docs []string
	var attachmentPath string
	for _, name := range []string{"first", "second"} {
		body := "# " + name + "\n![diagram](https://example.test/diagram.png)"
		accepted, err := h.service.CreateDocument(h.ctx, "owner-1", "default", CreateDocumentInput{IdempotencyKey: name, Filename: name + ".md", MediaType: "text/markdown", Body: strings.NewReader(body)})
		if err != nil {
			t.Fatal(err)
		}
		source, err := h.repo.GetIngestDocument(h.ctx, "owner-1", accepted.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		blob, release, err := h.service.PersistSourceAttachment(h.ctx, source, "image/png", bytes.NewReader([]byte("shared image bytes")))
		if err != nil {
			t.Fatal(err)
		}
		m := &SourceManifest{SchemaVersion: 1, ParserVersion: "attachment-fixture-v1", Blocks: []SourceContentBlock{{ID: "p1", Text: body, ObjectIDs: []string{"image1"}}}, Objects: []SourceContentObject{{ID: "image1", Kind: "image", Path: "https://example.test/diagram.png", StoragePath: blob.StoragePath, Digest: blob.SHA256, SizeBytes: blob.SizeBytes, MediaType: blob.MediaType}}}
		tx, err := h.db.BeginTx(h.ctx, nil)
		if err != nil {
			release()
			t.Fatal(err)
		}
		err = saveSourceManifestTx(h.ctx, tx, KnowledgeJob{OwnerID: "owner-1", CorpusUID: source.CorpusUID, DocumentID: source.DocumentID, DocumentGeneration: source.ContentGeneration}, source, m, time.Now().UnixMilli())
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		release()
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, source.DocumentID)
		if attachmentPath != "" && attachmentPath != blob.StoragePath {
			t.Fatal("shared bytes not deduplicated")
		}
		attachmentPath = blob.StoragePath
	}
	if err := h.service.ConfigureDocumentIngest(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(attachmentPath); err != nil {
		t.Fatalf("restart removed attachment: %v", err)
	}
	// 这里只验证附件生命周期；未执行正文解析的两个任务不参与 GC 领取。
	if _, err := h.db.Exec(`UPDATE kb_knowledge_jobs SET state='cancelled', finished_at=updated_at WHERE kind='ingest'`); err != nil {
		t.Fatal(err)
	}
	for i, doc := range docs {
		if err := h.store.Delete(h.ctx, doc); err != nil {
			t.Fatal(err)
		}
		now := time.Now().Add(time.Minute)
		worker := NewSemanticIndexWorker(h.repo, nil, workerConfig(&now, "attachment-gc", 16))
		if did, err := worker.RunOnce(h.ctx); err != nil || !did {
			t.Fatalf("GC %d: %v %v", i, did, err)
		}
		data, err := os.ReadFile(attachmentPath)
		if i == 0 && (err != nil || string(data) != "shared image bytes") {
			t.Fatalf("other source attachment removed: %v", err)
		}
		if i == 1 && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("last source orphan retained: %v", err)
		}
	}
	var manifests, blobs int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_source_manifests`).Scan(&manifests); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM kb_ingest_blobs WHERE storage_path=?`, attachmentPath).Scan(&blobs); err != nil {
		t.Fatal(err)
	}
	if manifests != 0 || blobs != 0 {
		t.Fatalf("GC residue manifests=%d blobs=%d", manifests, blobs)
	}
	payload, _ := json.Marshal(map[string]any{"shared_attachment_retained_after_first_delete": true, "attachment_removed_after_last_delete": true, "restart_retained": true})
	t.Log(string(payload))
}
