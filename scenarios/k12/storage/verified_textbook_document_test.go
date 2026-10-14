package k12storage_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func verifiedDocumentFixture(t *testing.T, bound bool) (*k12storage.Store, k12storage.VerifiedTextbookDocumentRequest) {
	t.Helper()
	store, first, firstDigest := seedTextbookCatalogMaterialization(t)
	const second = "第二课\n一个数除以分数等于乘其倒数。\n2"
	secondSum := sha256.Sum256([]byte(second))
	secondDigest := hex.EncodeToString(secondSum[:])
	sourceDigest := strings.Repeat("a", 64)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE kb_ingest_document_sources SET page_count=4,agent_id='mingming' WHERE document_id='catalog-doc'`, nil},
		{`UPDATE kb_knowledge_jobs SET pages_total=4,pages_done=4 WHERE job_id='catalog-ingest'`, nil},
		{`UPDATE kb_ingest_page_checkpoints SET pages_total=4 WHERE job_id='catalog-ingest'`, nil},
		{`INSERT INTO kb_chunks(id,doc_id,content,chunk_index,created_at,page_start,page_end,source_digest,source_offset_start,source_offset_end)
		 VALUES('catalog-segment-4','catalog-doc',?,1,CURRENT_TIMESTAMP,4,4,?,(SELECT sum(length(CAST(content AS BLOB))) FROM kb_ingest_page_checkpoints),(SELECT sum(length(CAST(content AS BLOB))) FROM kb_ingest_page_checkpoints)+?)`, []any{second, sourceDigest, len(second)}},
		{`INSERT INTO kb_ingest_page_checkpoints(job_id,page_number,pages_total,source_digest,extraction_mode,content,content_digest,source_offset_start,source_offset_end,lease_epoch,created_at,updated_at)
		 VALUES('catalog-ingest',4,4,?,'text',?,?,(SELECT sum(length(CAST(content AS BLOB))) FROM kb_ingest_page_checkpoints),(SELECT sum(length(CAST(content AS BLOB))) FROM kb_ingest_page_checkpoints)+?,1,1,1)`, []any{sourceDigest, second, secondDigest, len(second)}},
		{`UPDATE kb_documents SET content=(SELECT group_concat(content,'') FROM (SELECT content FROM kb_ingest_page_checkpoints ORDER BY page_number)) WHERE id='catalog-doc'`, nil},
		{`DELETE FROM k12_textbook_catalog_jobs`, nil},
	} {
		if _, err := store.DB().Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	publication := validTextbookCatalogPublication(first, firstDigest)
	publication.CatalogJSON = []byte(`{"subject":"math","textbook_edition":"人教版","textbook_version":"2025","title":"数学五年级下册","volume":"下册","page_min":1,"page_max":2,"units":[{"unit_id":"u1","title":"第一单元","page_from":1,"page_to":2,"lessons":[{"lesson_id":"l1","title":"第1课","page_from":1,"page_to":1},{"lesson_id":"l2","title":"第2课","page_from":2,"page_to":2}]}],"page_refs":[{"logical_page":1,"pdf_page":3,"segment_refs":["catalog-segment-3"]},{"logical_page":2,"pdf_page":4,"segment_refs":["catalog-segment-4"]}]}`)
	publication.PageProofs = append(publication.PageProofs, k12storage.TextbookCatalogPageProof{LogicalPage: 2, PDFPage: 4, EvidencePage: 4, EvidenceOffsetFrom: strings.LastIndex(second, "2"), EvidenceOffsetTo: len(second), EvidenceDigest: secondDigest, Method: "printed_anchor", SegmentRefs: []string{"catalog-segment-4"}})
	ctx := context.Background()
	worker := usecase.NewTextbookCatalogWorker(store, usecase.TextbookCatalogExtractorFunc(func(context.Context, k12storage.TextbookCatalogSource) (k12storage.TextbookCatalogPublication, error) {
		return publication, nil
	}), usecase.TextbookCatalogWorkerConfig{WorkerID: "document-worker", Lease: time.Minute, HeartbeatInterval: time.Second, ExtractTimeout: time.Second, MaxAttempts: 1, RetryBase: time.Second, RetryMax: time.Second, RecoveryBatch: 1})
	if processed, err := worker.RunOnce(ctx); err != nil || !processed {
		t.Fatalf("publish actual source checkpoint: processed=%v, err=%v", processed, err)
	}
	if bound {
		if _, err := store.DB().Exec(`INSERT INTO k12_textbook_bindings(textbook_binding_id,owner_id,agent_name,subject,textbook_manifest_id,document_id,document_generation,status,created_at,updated_at)
		 VALUES('real-binding','desktop-user','mingming','math','catalog-manifest','catalog-doc',1,'active',1,1)`); err != nil {
			t.Fatal(err)
		}
	}
	request := k12storage.VerifiedTextbookDocumentRequest{OwnerID: "desktop-user", AgentName: "mingming", DocumentID: "catalog-doc", DocumentGeneration: 1, SourceDigest: sourceDigest}
	if source, err := store.ReadVerifiedTextbookDocument(ctx, request); err != nil || len(source.Pages) != 2 {
		t.Fatalf("fixture did not publish a readable source: pages=%d, err=%v", len(source.Pages), err)
	}
	return store, request
}

func documentSourceSnapshot(t *testing.T, db *sql.DB, tables ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

var documentProtectedTables = []string{"agents", "k12_textbook_bindings", "k12_profile_revisions", "k12_curriculum_progress", "k12_curriculum_progress_revisions"}

func TestVerifiedTextbookDocumentReadsWithoutBindingSQLite(t *testing.T) {
	store, request := verifiedDocumentFixture(t, false)
	before := documentSourceSnapshot(t, store.DB(), documentProtectedTables...)
	source, err := store.ReadVerifiedTextbookDocument(context.Background(), request)
	if err != nil || len(source.Pages) != 2 {
		t.Fatalf("document source unavailable: %+v, err=%v", source, err)
	}
	if source.Catalog.TextbookManifestID != "catalog-manifest" || source.Catalog.TextbookBindingID != "" || source.Catalog.Units[0].UnitID != "u1" || source.Catalog.Units[0].PageFrom != 1 || source.Catalog.Units[0].PageTo != 2 || source.Pages[0].LogicalPage != 1 || source.Pages[0].PDFPage != 3 || source.Pages[1].LogicalPage != 2 || source.Pages[1].PDFPage != 4 || !strings.Contains(source.Pages[1].Content, "倒数") {
		t.Fatalf("wrong document proof or invented binding: %+v", source)
	}
	catalog, err := store.GetVerifiedTextbookDocumentCatalog(context.Background(), request)
	if err != nil || !reflect.DeepEqual(catalog, source.Catalog) {
		t.Fatalf("directory differs from actual page source: %+v, %v", catalog, err)
	}
	if _, found, err := store.GetActiveTextbookGroundingScopeReadOnly(context.Background(), k12storage.TextbookScope{OwnerID: request.OwnerID, AgentName: request.AgentName, Subject: "math"}); err != nil || found {
		t.Fatalf("read created active binding: found=%v, err=%v", found, err)
	}
	if !reflect.DeepEqual(before, documentSourceSnapshot(t, store.DB(), documentProtectedTables...)) {
		t.Fatal("document read changed binding/profile/progress")
	}
}

func TestVerifiedTextbookDocumentRejectsIdentityOrProofDriftSQLite(t *testing.T) {
	for _, change := range []string{"owner", "agent", "generation", "source_digest", "content", "content_digest", "segment", "catalog_digest", "current_generation"} {
		t.Run(change, func(t *testing.T) {
			store, request := verifiedDocumentFixture(t, false)
			var statement string
			switch change {
			case "owner":
				request.OwnerID = "other-owner"
			case "agent":
				request.AgentName = "lele"
			case "generation":
				request.DocumentGeneration = 2
			case "source_digest":
				request.SourceDigest = strings.Repeat("b", 64)
			case "content":
				statement = `UPDATE kb_ingest_page_checkpoints SET content='changed body' WHERE page_number=4`
			case "content_digest":
				statement = `UPDATE kb_ingest_page_checkpoints SET content_digest='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE page_number=4`
			case "segment":
				statement = `UPDATE k12_textbook_manifest_segments SET document_generation=2 WHERE pdf_page=4`
			case "catalog_digest":
				statement = `UPDATE k12_textbook_manifests SET catalog_digest='wrong' WHERE manifest_id='catalog-manifest'`
			case "current_generation":
				statement = `UPDATE kb_semantic_document_bindings SET content_generation=2 WHERE document_id='catalog-doc'`
			}
			if statement != "" {
				if _, err := store.DB().Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			before := documentSourceSnapshot(t, store.DB(), documentProtectedTables...)
			if source, err := store.ReadVerifiedTextbookDocument(context.Background(), request); err == nil || len(source.Pages) != 0 {
				t.Fatalf("changed %s accepted: %+v, %v", change, source, err)
			}
			if !reflect.DeepEqual(before, documentSourceSnapshot(t, store.DB(), documentProtectedTables...)) {
				t.Fatalf("rejecting %s changed binding/profile/progress", change)
			}
		})
	}
}

type documentUnitGenerator struct {
	resolved  chan usecase.UnitSummaryResolveInput
	generated chan usecase.UnitSummaryGenerationInput
	unitID    string
}

func (g *documentUnitGenerator) ResolveUnitSummary(_ context.Context, input usecase.UnitSummaryResolveInput) (usecase.UnitSummaryResolution, error) {
	g.resolved <- input
	return usecase.UnitSummaryResolution{Subject: "math", Intent: "generate", UnitID: g.unitID, UnitTitle: "第一单元", MaterialDocumentIDs: []string{"catalog-doc"}, Format: "standard"}, nil
}

func (g *documentUnitGenerator) GenerateUnitSummary(_ context.Context, input usecase.UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.generated <- input
	return k12.UnitSummaryContentV1{}, errors.Join(egress.ErrProviderResponseProcessed, errors.New("controlled stop after source freeze"))
}

func TestUnitSummaryExplicitDocumentFreezesVerifiedUnitSQLite(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "full"
		if missing {
			name = "missing_page"
		}
		t.Run(name, func(t *testing.T) {
			store, source := verifiedDocumentFixture(t, false)
			if missing {
				if _, err := store.DB().Exec(`DELETE FROM kb_ingest_page_checkpoints WHERE page_number=4`); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			before := documentSourceSnapshot(t, store.DB(), documentProtectedTables...)
			if _, err := store.DB().Exec(`INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES('unit-session','desktop-user',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`INSERT INTO messages(id,session_id,role,content,created_at) VALUES('unit-message','unit-session','user','整理数学五年级下册.pdf的第一单元，不改档案',CURRENT_TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			generator := &documentUnitGenerator{resolved: make(chan usecase.UnitSummaryResolveInput, 1), generated: make(chan usecase.UnitSummaryGenerationInput, 1), unitID: "u1"}
			coordinator := usecase.NewUnitSummaryCoordinator(usecase.Deps{Records: store, TextbookOwnerID: source.OwnerID}, generator, func(_ context.Context, route k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
				return route, nil
			})
			wait, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			view, _, err := coordinator.StartAndWait(wait, usecase.UnitSummaryRequest{OwnerID: source.OwnerID, AgentName: source.AgentName, SessionID: "unit-session", SourceMessageID: "unit-message", IdempotencyKey: "unit-current", FinalUserText: "整理数学五年级下册.pdf的第一单元，不改档案", ModelSnapshot: k12.GradingModelSnapshot{Provider: "local-fixture", Model: "local-fixture"}})
			if err != nil || view.Job.State != "failed" || view.Job.FailureKind != "generation_failed" {
				t.Fatalf("controlled source pipeline did not finish: state=%s, kind=%s, err=%v", view.Job.State, view.Job.FailureKind, err)
			}
			select {
			case input := <-generator.resolved:
				if input.MathCatalog != nil || len(input.Materials) != 1 || input.Materials[0].TextbookCatalog == nil || input.Materials[0].TextbookCatalog.TextbookManifestID != "catalog-manifest" {
					t.Fatalf("explicit catalog not supplied independently of active binding: %+v", input)
				}
			default:
				t.Fatal("resolver was not reached")
			}
			select {
			case input := <-generator.generated:
				count, level := 2, "full"
				if missing {
					count, level = 1, "partial"
				}
				if input.Context.UnitID != "u1" || input.Context.UnitTitle != "第一单元" || input.Context.UnitNumber != "" || input.Context.Coverage.Level != level || len(input.Context.Sources) != count {
					t.Fatalf("wrong frozen coverage: %+v", input.Context)
				}
				if missing && !reflect.DeepEqual(input.Context.Coverage.Missing, []string{"教材第2页"}) {
					t.Fatalf("missing page not disclosed: %+v", input.Context.Coverage)
				}
				for _, page := range input.Context.Sources {
					if page.Origin != "textbook" || page.DocumentID != "catalog-doc" || page.Generation != 1 || page.ManifestID != "catalog-manifest" || page.SourceDigest != source.SourceDigest || len(page.SegmentRefs) != 1 || strings.TrimSpace(page.Text) == "" {
						t.Fatalf("frozen page does not identify its real source: %+v", page)
					}
				}
			default:
				t.Fatal("generation source freeze was not reached")
			}
			if !reflect.DeepEqual(before, documentSourceSnapshot(t, store.DB(), documentProtectedTables...)) {
				t.Fatal("unit task wrote a textbook binding/profile/progress")
			}
		})
	}
}

func TestVerifiedTextbookDocumentPreservesStrictBindingReaderSQLite(t *testing.T) {
	store, source := verifiedDocumentFixture(t, true)
	ctx := context.Background()
	scope, found, err := store.GetActiveTextbookGroundingScopeReadOnly(ctx, k12storage.TextbookScope{OwnerID: source.OwnerID, AgentName: source.AgentName, Subject: "math"})
	if err != nil || !found {
		t.Fatalf("real binding unavailable: found=%v, err=%v", found, err)
	}
	request := k12.VerifiedTextbookReadRequest{OwnerID: source.OwnerID, AgentName: source.AgentName, Subject: "math", Scope: scope}
	boundPages, err := store.ReadVerifiedTextbookPages(ctx, request)
	document, documentErr := store.ReadVerifiedTextbookDocument(ctx, source)
	if err != nil || documentErr != nil || !reflect.DeepEqual(boundPages, document.Pages) {
		t.Fatalf("binding source changed: %v, %v", err, documentErr)
	}
	if _, err := store.DB().Exec(`DELETE FROM kb_ingest_page_checkpoints WHERE page_number=4`); err != nil {
		t.Fatal(err)
	}
	if pages, err := store.ReadVerifiedTextbookPages(ctx, request); err == nil || len(pages) != 0 {
		t.Fatal("old binding reader accepted a missing source page")
	}
	request.Scope.TextbookBindingID = ""
	if pages, err := store.ReadVerifiedTextbookPages(ctx, request); err == nil || len(pages) != 0 {
		t.Fatal("old binding validation became optional")
	}
}

type frozenDocumentGenerator struct{ calls atomic.Int32 }

func (g *frozenDocumentGenerator) ResolveUnitSummary(context.Context, usecase.UnitSummaryResolveInput) (usecase.UnitSummaryResolution, error) {
	g.calls.Add(1)
	return usecase.UnitSummaryResolution{}, errors.New("unexpected frozen resolution call")
}

func (g *frozenDocumentGenerator) GenerateUnitSummary(context.Context, usecase.UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.calls.Add(1)
	return k12.UnitSummaryContentV1{}, errors.New("unexpected frozen generation call")
}

func TestUnitSummaryDocumentSourcePreservesFrozenUnknownSQLite(t *testing.T) {
	for _, stage := range []string{"resolve", "generate"} {
		t.Run(stage, func(t *testing.T) {
			store, source := verifiedDocumentFixture(t, false)
			ctx := context.Background()
			if _, err := store.DB().Exec(`INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES('old-session','desktop-user',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`INSERT INTO messages(id,session_id,role,content,created_at) VALUES('old-message','old-session','user','整理数学材料',CURRENT_TIMESTAMP)`); err != nil {
				t.Fatal(err)
			}
			var text string
			if err := store.DB().QueryRow(`SELECT content FROM kb_documents WHERE id='catalog-doc'`).Scan(&text); err != nil {
				t.Fatal(err)
			}
			route := k12.GradingModelSnapshot{Provider: "local-fixture", Model: "local-fixture"}
			req := usecase.UnitSummaryRequest{OwnerID: source.OwnerID, AgentName: source.AgentName, SessionID: "old-session", SourceMessageID: "old-message", IdempotencyKey: "old-unit", FinalUserText: "整理数学材料", ModelSnapshot: route}
			// 模拟旧版本已冻结的真实材料输入：没有新目录字段，也不能恢复时补入。
			input := usecase.UnitSummaryResolveInput{Request: req, Materials: []k12storage.UnitSummaryMaterial{{DocumentID: source.DocumentID, Title: "数学五年级下册.pdf", Subject: "math", Generation: source.DocumentGeneration, SourceDigest: source.SourceDigest, Preview: text}}}
			inputJSON, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			frozen := k12.UnitSummaryContext{Subject: "math", UnitTitle: "数学材料", ModelSnapshot: route, Coverage: k12.UnitSummaryCoverage{Level: "partial", Missing: []string{"未核对教材完整单元范围"}}, Sources: []k12.UnitSummaryFrozenSource{{UnitSummarySource: k12.UnitSummarySource{Ref: "material:catalog-doc", Origin: "provided_material", Label: "数学五年级下册.pdf", DocumentID: source.DocumentID, Generation: source.DocumentGeneration, SourceDigest: source.SourceDigest}, Text: text}}}
			if stage == "resolve" {
				frozen = k12.UnitSummaryContext{ModelSnapshot: route}
			}
			a, _, err := store.CreateUnitSummaryAttempt(ctx, k12.UnitSummaryAttempt{OwnerID: source.OwnerID, AgentName: source.AgentName, SessionID: req.SessionID, SourceMessageID: req.SourceMessageID, IdempotencyKey: req.IdempotencyKey, FinalUserText: req.FinalUserText, RequestJSON: string(raw), RequestDigest: k12.UnitSummaryDigest(req), ResolveInputJSON: string(inputJSON), Context: frozen, Requirements: k12.UnitSummaryRequirements{Format: "standard"}})
			if err != nil {
				t.Fatal(err)
			}
			a, claimed, err := store.ClaimUnitSummaryAttempt(ctx, source.OwnerID, source.AgentName, a.AttemptID, "old-worker", time.Now().Unix()+600)
			if err != nil || !claimed {
				t.Fatalf("claim old attempt: %v, %v", err, claimed)
			}
			callInput, invocationStage := any(input), "unit_summary_resolve"
			if stage == "generate" {
				a, _, err = store.BindUnitSummaryDocument(ctx, a, "old-scope")
				if err != nil {
					t.Fatal(err)
				}
				next := a
				r, _ := json.Marshal(usecase.UnitSummaryResolution{Subject: "math", Intent: "generate", UnitTitle: "数学材料", MaterialDocumentIDs: []string{source.DocumentID}, Format: "standard"})
				next.ResolutionJSON = string(r)
				a, err = store.CheckpointUnitSummaryAttempt(ctx, a, next)
				if err != nil {
					t.Fatal(err)
				}
				callInput, invocationStage = usecase.UnitSummaryGenerationInput{Context: frozen, FinalUserText: req.FinalUserText, Requirements: a.Requirements}, "unit_summary_generate"
			}
			inv, _, err := store.PrepareUnitSummaryModelInvocation(ctx, k12.ModelInvocation{InvocationID: "old-modelinv", AgentName: source.AgentName, JobID: a.AttemptID, Stage: invocationStage, RequestDigest: k12.UnitSummaryDigest(callInput), Attempt: 1, RouteSnapshot: route})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.MarkUnitSummaryModelInvocationSent(ctx, source.AgentName, inv.InvocationID, "old-provider-key"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.MarkUnitSummaryModelInvocationOutcomeUnknown(ctx, source.AgentName, inv.InvocationID, "provider_outcome_unknown"); err != nil {
				t.Fatal(err)
			}
			next := a
			next.State, next.Stage, next.InvocationRef = "reconciling", stage, inv.InvocationID
			next.LeaseOwner, next.LeaseExpiresAt = "", 0
			a, err = store.CheckpointUnitSummaryAttempt(ctx, a, next)
			if err != nil {
				t.Fatal(err)
			}
			before := documentSourceSnapshot(t, store.DB(), "k12_unit_summary_model_invocations", "k12_unit_summary_documents", "k12_unit_summary_revisions")
			generator := &frozenDocumentGenerator{}
			coordinator := usecase.NewUnitSummaryCoordinator(usecase.Deps{Records: store, TextbookOwnerID: source.OwnerID}, generator, func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return route, nil })
			wait, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if view, replayed, err := coordinator.StartAndWait(wait, req); err != nil || !replayed || view.Job.State != "reconciling" {
				t.Fatalf("unknown replay changed its task: %+v, %v, %v", view.Job, replayed, err)
			}
			// 等待已过期租约的原任务实际只读对账，不把立即返回当作后台零重发的证据。
			var observed k12.UnitSummaryAttempt
			for {
				observed, err = store.GetUnitSummaryAttempt(wait, source.OwnerID, source.AgentName, a.AttemptID)
				if err != nil {
					t.Fatal(err)
				}
				if observed.Revision > a.Revision && observed.LeaseOwner == "" && observed.State == "reconciling" {
					break
				}
				if wait.Err() != nil {
					t.Fatal("old unknown reconciliation was not observed")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if observed.ResolveInputJSON != string(inputJSON) || !reflect.DeepEqual(observed.Context, frozen) || generator.calls.Load() != 0 || !reflect.DeepEqual(before, documentSourceSnapshot(t, store.DB(), "k12_unit_summary_model_invocations", "k12_unit_summary_documents", "k12_unit_summary_revisions")) {
				t.Fatalf("new directory reinterpreted or resent old %s: calls=%d", stage, generator.calls.Load())
			}
		})
	}
}
