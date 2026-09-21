package knowledge

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestDocumentSourcePreservesOriginalBytesWithoutStartingWork(t *testing.T) {
	db, service, ctx := newAsyncIngestHarness(t)
	const original = "%PDF-1.7\noriginal image and formula objects\n%%EOF"
	created, err := service.CreateDocument(ctx, "desktop-user", "default", CreateDocumentInput{
		IdempotencyKey: "source-preview", Filename: "教材.pdf", MediaType: "application/pdf",
		SizeBytes: int64(len(original)), Body: strings.NewReader(original),
	})
	if err != nil {
		t.Fatal(err)
	}
	file, source, err := service.OpenDocumentSource(ctx, "desktop-user", "default", created.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original || source.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(original))) {
		t.Fatal("source bytes or digest changed")
	}
	job, err := NewSQLiteSemanticIndexRepository(db).GetJob(ctx, "desktop-user", created.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != KnowledgeJobQueued {
		t.Fatalf("source read started work: %s", job.State)
	}
}
