package k12storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func verifiedTextbookReadFixture(t *testing.T) (*k12storage.Store, k12.VerifiedTextbookReadRequest) {
	t.Helper()
	deps, _ := weeklyTextbookSourceFixture(t, weeklyLessonPage)
	store := deps.Records
	scope, found, err := store.GetActiveTextbookGroundingScope(context.Background(), k12storage.TextbookScope{
		OwnerID: "desktop-user", AgentName: "mingming", Subject: "math",
	})
	if err != nil || !found {
		t.Fatalf("verified source missing: found=%v err=%v", found, err)
	}
	return store, k12.VerifiedTextbookReadRequest{OwnerID: "desktop-user", AgentName: "mingming", Subject: "math", Scope: scope}
}

func TestVerifiedTextbookPages_FrozenSourceSurvivesRestartAndReparse(t *testing.T) {
	store, request := verifiedTextbookReadFixture(t)
	ctx := context.Background()
	want, err := store.ReadVerifiedTextbookPages(ctx, request)
	if err != nil || len(want) != 1 {
		t.Fatalf("read source: pages=%+v err=%v", want, err)
	}
	if want[0].LogicalPage != 1 || want[0].PDFPage != 3 || want[0].Content != weeklyLessonPage ||
		!reflect.DeepEqual(want[0].SegmentRefs, []string{"catalog-segment-3"}) ||
		len(want[0].LessonTitles) != 0 {
		t.Fatalf("wrong page or lesson attribution: %+v", want[0])
	}
	path := filepath.Join(t.TempDir(), "verified-textbook.db")
	if _, err := store.DB().Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	open := func() *k12storage.Store {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		return k12storage.NewStore(db, nil)
	}
	reopened := open()
	// 发布新版本后当前块已替换，原目录和解析回执仍归属于旧版本。
	for _, statement := range []string{
		`UPDATE kb_semantic_document_bindings SET content_generation=2`,
		`UPDATE kb_documents SET content='new generation content' WHERE id='catalog-doc'`,
		`DELETE FROM kb_chunks WHERE doc_id='catalog-doc'`,
		`UPDATE k12_textbook_manifests SET state='stale' WHERE manifest_id='catalog-manifest'`,
		`UPDATE k12_textbook_bindings SET status='invalidated' WHERE textbook_manifest_id='catalog-manifest'`,
	} {
		if _, err := reopened.DB().Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := reopened.DB().Close(); err != nil {
		t.Fatal(err)
	}
	reopened = open()
	got, err := reopened.ReadVerifiedTextbookPages(ctx, request)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("frozen source changed after restart and reparse: got=%+v err=%v", got, err)
	}
	if _, found, err := reopened.GetActiveTextbookGroundingScope(ctx, k12storage.TextbookScope{
		OwnerID: request.OwnerID, AgentName: request.AgentName, Subject: request.Subject,
	}); err != nil || found {
		t.Fatalf("new task retained invalidated source: found=%v err=%v", found, err)
	}
}

func TestVerifiedTextbookPages_RejectsForeignOrChangedScope(t *testing.T) {
	store, original := verifiedTextbookReadFixture(t)
	for _, name := range []string{"owner", "agent", "subject", "binding", "manifest", "generation", "source", "page", "segment", "edition"} {
		t.Run(name, func(t *testing.T) {
			request := original
			request.Scope.PageRefs = append([]k12.TextbookGroundingPageRef(nil), original.Scope.PageRefs...)
			switch name {
			case "owner":
				request.OwnerID = "other-owner"
			case "agent":
				request.AgentName = "lele"
			case "subject":
				request.Subject = "chinese"
			case "binding":
				request.Scope.TextbookBindingID = "other-binding"
			case "manifest":
				request.Scope.TextbookManifestID = "other-manifest"
			case "generation":
				request.Scope.DocumentGeneration = 2
			case "source":
				request.Scope.SourceDigest = strings.Repeat("b", 64)
			case "page":
				request.Scope.PageRefs[0].PDFPage = 2
			case "segment":
				request.Scope.SegmentRefs = []string{"unrelated-chunk"}
				request.Scope.PageRefs[0].SegmentRefs = []string{"unrelated-chunk"}
			case "edition":
				request.Scope.Edition = "other-edition"
			}
			if pages, err := store.ReadVerifiedTextbookPages(context.Background(), request); err == nil || len(pages) != 0 {
				t.Fatalf("changed scope read original source: pages=%+v err=%v", pages, err)
			}
		})
	}
}

func TestVerifiedTextbookPages_RejectsChangedProofOrDeletedSource(t *testing.T) {
	for _, mutation := range []string{"content", "content_digest", "missing_page", "segment", "catalog", "deleted"} {
		t.Run(mutation, func(t *testing.T) {
			store, request := verifiedTextbookReadFixture(t)
			var statement string
			switch mutation {
			case "content":
				statement = `UPDATE kb_ingest_page_checkpoints SET content='wrong answer' WHERE page_number=3`
			case "content_digest":
				statement = `UPDATE kb_ingest_page_checkpoints SET content_digest='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE page_number=3`
			case "missing_page":
				statement = `DELETE FROM kb_ingest_page_checkpoints WHERE page_number=3`
			case "segment":
				statement = `DELETE FROM k12_textbook_manifest_segments WHERE manifest_id='catalog-manifest'`
			case "catalog":
				statement = `UPDATE k12_textbook_manifests SET catalog_digest='wrong' WHERE manifest_id='catalog-manifest'`
			case "deleted":
				statement = `UPDATE kb_documents SET deleted=1 WHERE id='catalog-doc'`
			}
			if _, err := store.DB().Exec(statement); err != nil {
				t.Fatal(err)
			}
			if pages, err := store.ReadVerifiedTextbookPages(context.Background(), request); err == nil || len(pages) != 0 {
				t.Fatalf("changed source returned as verified: pages=%+v err=%v", pages, err)
			}
		})
	}
}
