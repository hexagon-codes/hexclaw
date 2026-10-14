package usecase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

const ProgressSourceOwner = "progress-owner"
const ProgressSourceAgent = "progress-child"
const ProgressSourceDocument = "progress-document"
const ProgressSourceManifest = "progress-manifest"

var ProgressSourcePages = []string{
	"AI 生成示例教材\n数学 五年级 下册\n人民教育出版社",
	"目 录\n1 第一单元 1\n2 第二单元 2",
	"第一单元\n分数乘整数。\n1",
	"第二单元\n分数除法：除以分数，乘它的倒数。\n2",
	"第二单元\n分数除法练习：3/4÷2/3=9/8。\n3",
}

func ProgressSourceDigest(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func ProgressSourceExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("seed progress source: %v", err)
	}
}

func ProgressSourceFixture(t *testing.T) (Deps, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "progress-source.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err = migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	profile := k12.ChildProfile{ChildName: "示例孩子", GradeTerm: "五年级下", SubjectTextbooks: k12.SubjectTextbooks{Math: "人教版", Chinese: "统编版", English: "外研版", Science: "教科版", InformationTechnology: "浙教版", Art: "人美版"}}
	meta, err := json.Marshal(k12.ApplyProfileToMeta(map[string]string{"scenario": "k12-tutor"}, profile))
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{ProgressSourceAgent, "progress-other-child"} {
		ProgressSourceExec(t, db, `INSERT INTO agents(name,display_name,metadata) VALUES(?,?,?)`, agent, agent, string(meta))
	}
	body := strings.Join(ProgressSourcePages, "")
	digest := ProgressSourceDigest(body)
	ProgressSourceExec(t, db, `INSERT INTO kb_semantic_corpora(corpus_uid,owner_id,corpus_alias,kind,content_version,created_at,updated_at) VALUES('progress-corpus',?,'default','general',1,1,1)`, ProgressSourceOwner)
	ProgressSourceExec(t, db, `INSERT INTO kb_ingest_blobs(owner_id,corpus_uid,sha256,storage_path,size_bytes,media_type,created_at) VALUES(?,'progress-corpus',?,'/progress-fixture.pdf',1,'application/pdf',1)`, ProgressSourceOwner, digest)
	ProgressSourceExec(t, db, `INSERT INTO kb_documents(id,title,content,source,deleted,corpus_uid,created_at,updated_at) VALUES(?,'AI示例数学五年级下册.pdf',?,'upload:progress-fixture.pdf',0,'progress-corpus',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, ProgressSourceDocument, body)
	ProgressSourceExec(t, db, `INSERT INTO kb_semantic_document_generations(owner_id,corpus_uid,document_id,content_generation,created_at) VALUES(?,'progress-corpus',?,1,1)`, ProgressSourceOwner, ProgressSourceDocument)
	ProgressSourceExec(t, db, `INSERT INTO kb_ingest_document_sources(document_id,owner_id,corpus_uid,content_generation,blob_sha256,original_name,extension,media_type,size_bytes,agent_id,page_count,created_at,updated_at) VALUES(?,?,'progress-corpus',1,?,'AI示例数学五年级下册.pdf','.pdf','application/pdf',1,?,5,1,1)`, ProgressSourceDocument, ProgressSourceOwner, digest, ProgressSourceAgent)
	ProgressSourceExec(t, db, `INSERT INTO kb_semantic_document_bindings(document_id,owner_id,corpus_uid,content_generation,lifecycle_state,text_state,version,created_at,updated_at) VALUES(?,?,'progress-corpus',1,'active','ready',1,1,1)`, ProgressSourceDocument, ProgressSourceOwner)
	ProgressSourceExec(t, db, `INSERT INTO kb_knowledge_jobs(job_id,kind,owner_id,corpus_uid,document_id,document_generation,idempotency_key,state,stage,attempt,lease_epoch,created_at,updated_at,finished_at,pages_total,pages_done) VALUES('progress-ingest','ingest',?,'progress-corpus',?,1,'progress-ingest','succeeded','publishing',1,1,1,1,1,5,5)`, ProgressSourceOwner, ProgressSourceDocument)
	offset := 0
	for i, page := range ProgressSourcePages {
		ProgressSourceExec(t, db, `INSERT INTO kb_ingest_page_checkpoints(job_id,page_number,pages_total,source_digest,extraction_mode,content,content_digest,source_offset_start,source_offset_end,lease_epoch,created_at,updated_at) VALUES('progress-ingest',?,5,?,'text',?,?,?,?,1,1,1)`, i+1, digest, page, ProgressSourceDigest(page), offset, offset+len(page))
		if i >= 2 {
			ProgressSourceExec(t, db, `INSERT INTO kb_chunks(id,doc_id,content,chunk_index,created_at,page_start,page_end,source_digest,source_offset_start,source_offset_end) VALUES(?,?,?,?,CURRENT_TIMESTAMP,?,?,?,?,?)`, fmt.Sprintf("progress-segment-%d", i+1), ProgressSourceDocument, page, i-2, i+1, i+1, digest, offset, offset+len(page))
		}
		offset += len(page)
	}
	ProgressSourceExec(t, db, `INSERT INTO k12_textbook_manifests(manifest_id,owner_id,document_id,document_generation,document_title,subject,source_digest,state,retryable,failure_message,text_index_state,vector_index_state,created_at,updated_at) VALUES(?,?,?,1,'AI示例数学五年级下册.pdf','math',?,'extracting',0,'','ready','ready',1,1)`, ProgressSourceManifest, ProgressSourceOwner, ProgressSourceDocument, digest)
	// 提取结果仅包含公开目录合同，不序列化运行时的智能体或绑定身份。
	payload := map[string]any{
		"subject": "math", "grade_term": "五年级下", "textbook_edition": "人教版",
		"textbook_version": "2025", "title": "AI 生成示例教材", "volume": "下册", "page_min": 1, "page_max": 3,
		"units": []k12.CurriculumCatalogUnit{
			{UnitID: "progress-u1", Title: "第一单元", PageFrom: 1, PageTo: 1, Lessons: []k12.CurriculumCatalogLesson{{LessonID: "progress-l1", Title: "第一课", PageFrom: 1, PageTo: 1}}},
			{UnitID: "progress-u2", Title: "第二单元", PageFrom: 2, PageTo: 3, Lessons: []k12.CurriculumCatalogLesson{{LessonID: "progress-l2", Title: "第二课", PageFrom: 2, PageTo: 3}}},
		},
	}
	refs := []map[string]any{}
	publication := k12storage.TextbookCatalogPublication{}
	for logical := 1; logical <= 3; logical++ {
		physical := logical + 2
		page := ProgressSourcePages[physical-1]
		segment := fmt.Sprintf("progress-segment-%d", physical)
		refs = append(refs, map[string]any{"logical_page": logical, "pdf_page": physical, "segment_refs": []string{segment}})
		publication.PageProofs = append(publication.PageProofs, k12storage.TextbookCatalogPageProof{LogicalPage: logical, PDFPage: physical, EvidencePage: physical, EvidenceOffsetFrom: len(page) - 1, EvidenceOffsetTo: len(page), EvidenceDigest: ProgressSourceDigest(page), Method: "printed_anchor", SegmentRefs: []string{segment}})
	}
	payload["page_refs"] = refs
	publication.CatalogJSON, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	store := k12storage.NewStore(db, nil)
	// 目录由真实worker验证并发布，替身只提供受控提取结果。
	worker := NewTextbookCatalogWorker(store, TextbookCatalogExtractorFunc(func(context.Context, k12storage.TextbookCatalogSource) (k12storage.TextbookCatalogPublication, error) {
		return publication, nil
	}), TextbookCatalogWorkerConfig{WorkerID: "progress-source-worker", Lease: time.Minute, HeartbeatInterval: time.Second, ExtractTimeout: time.Second, MaxAttempts: 1, RetryBase: time.Second, RetryMax: time.Second, RecoveryBatch: 1})
	if processed, err := worker.RunOnce(ctx); err != nil || !processed {
		t.Fatalf("publish source catalog: processed=%v, err=%v", processed, err)
	}
	deps := Deps{Records: store, TextbookOwnerID: ProgressSourceOwner, Now: func() int64 { return 1791302400 }}
	if _, err := deps.SetConfirmedCurriculumProgress(ctx, ConfirmedCurriculumProgressRequest{OwnerID: ProgressSourceOwner, AgentName: ProgressSourceAgent, ExpectedProgressRevision: 0, Selection: k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: ProgressSourceManifest, Volume: "下册", UnitID: "progress-u2", LessonID: "progress-l2"}}); err != nil {
		t.Fatalf("save actual progress and binding: %v", err)
	}
	return deps, digest
}

func ProgressSourceSnapshot(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range []string{"agents", "k12_textbook_bindings", "k12_profile_revisions", "k12_curriculum_progress", "k12_curriculum_progress_revisions"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			out[table] = append(out[table], string(raw))
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return out
}

type ProgressSourceGenerator struct {
	resolution UnitSummaryResolution
	resolved   chan UnitSummaryResolveInput
	generated  chan UnitSummaryGenerationInput
}

func (g *ProgressSourceGenerator) ResolveUnitSummary(_ context.Context, input UnitSummaryResolveInput) (UnitSummaryResolution, error) {
	g.resolved <- input
	return g.resolution, nil
}

func (g *ProgressSourceGenerator) GenerateUnitSummary(_ context.Context, input UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.generated <- input
	return k12.UnitSummaryContentV1{}, errors.Join(egress.ErrProviderResponseProcessed, errors.New("controlled stop after source freeze"))
}

func ProgressSourceRun(t *testing.T, deps Deps, req UnitSummaryRequest, resolution UnitSummaryResolution) (UnitSummaryView, *ProgressSourceGenerator) {
	t.Helper()
	ProgressSourceExec(t, deps.Records.DB(), `INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES(?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, req.SessionID, req.OwnerID)
	ProgressSourceExec(t, deps.Records.DB(), `INSERT INTO messages(id,session_id,role,content,created_at) VALUES(?,?,'user',?,CURRENT_TIMESTAMP)`, req.SourceMessageID, req.SessionID, req.FinalUserText)
	generator := &ProgressSourceGenerator{resolution: resolution, resolved: make(chan UnitSummaryResolveInput, 1), generated: make(chan UnitSummaryGenerationInput, 1)}
	coordinator := NewUnitSummaryCoordinator(deps, generator, func(_ context.Context, route k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
		return route, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	view, _, err := coordinator.StartAndWait(ctx, req)
	if err != nil || ctx.Err() != nil {
		t.Fatalf("actual coordinator did not finish: %v, context=%v", err, ctx.Err())
	}
	for deadline := time.Now().Add(time.Second); ; {
		coordinator.mu.Lock()
		active := len(coordinator.active)
		coordinator.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source worker did not release")
		}
		time.Sleep(time.Millisecond)
	}
	return view, generator
}

func ProgressSourceRequest() UnitSummaryRequest {
	return UnitSummaryRequest{OwnerID: ProgressSourceOwner, AgentName: ProgressSourceAgent, SessionID: "progress-unit-session", SourceMessageID: "progress-unit-message", IdempotencyKey: "progress-current", FinalUserText: "整理数学当前单元知识，不修改学习进度。", ModelSnapshot: k12.GradingModelSnapshot{Provider: "local-fixture", Model: "local-fixture"}}
}

func TestUnitSummaryProgressSourceUsesSavedScopeSQLite(t *testing.T) {
	for _, explicit := range []string{"saved_progress", "explicit_document", "explicit_text"} {
		t.Run(explicit, func(t *testing.T) {
			deps, digest := ProgressSourceFixture(t)
			req := ProgressSourceRequest()
			resolution := UnitSummaryResolution{Subject: "math", Intent: "generate", Format: "standard"}
			if explicit == "explicit_document" {
				resolution.UnitID, resolution.UnitTitle, resolution.MaterialDocumentIDs = "progress-u1", "第一单元", []string{ProgressSourceDocument}
				req.FinalUserText = "整理已上传数学教材的第一单元，不修改学习进度。"
			}
			if explicit == "explicit_text" {
				resolution.UnitTitle, resolution.ProvidedMaterialText = "本次材料", "本次材料：小数除法，6÷0.5=12。"
				req.FinalUserText = "整理本次材料，不修改学习进度。\n" + resolution.ProvidedMaterialText
			}
			before := ProgressSourceSnapshot(t, deps.Records.DB())
			view, generator := ProgressSourceRun(t, deps, req, resolution)
			if view.Job.State != "failed" || view.Job.FailureKind != "generation_failed" {
				t.Fatalf("controlled source capture did not reach generation: state=%s, kind=%s", view.Job.State, view.Job.FailureKind)
			}
			resolved := <-generator.resolved
			if resolved.MathProgress == nil || resolved.MathProgress.UnitID != "progress-u2" || resolved.MathCatalog == nil || resolved.MathCatalog.GradeTerm != "五年级下" || resolved.MathCatalog.Volume != "下册" {
				t.Fatalf("saved matching progress was not read: %+v", resolved)
			}
			input := <-generator.generated
			if explicit == "explicit_text" {
				if input.Context.Coverage.Level != "partial" || len(input.Context.Sources) != 1 || input.Context.Sources[0].Text != resolution.ProvidedMaterialText || input.Context.Sources[0].Origin != "provided_material" || input.Context.ProgressSource != "" {
					t.Fatalf("explicit text lost priority: %+v", input.Context)
				}
			} else {
				wantUnit, wantPages := "progress-u2", []string{"2", "3"}
				if explicit == "explicit_document" {
					wantUnit, wantPages = "progress-u1", []string{"1"}
				}
				if input.Context.UnitID != wantUnit || input.Context.Coverage.Level != "full" || len(input.Context.Sources) != len(wantPages) || len(input.Context.Coverage.Missing) != 0 {
					t.Fatalf("wrong frozen unit or coverage: %+v", input.Context)
				}
				for i, source := range input.Context.Sources {
					logical := 2 + i
					if explicit == "explicit_document" {
						logical = 1
					}
					if source.Page != wantPages[i] || source.Text != ProgressSourcePages[logical+1] || source.DocumentID != ProgressSourceDocument || source.ManifestID != ProgressSourceManifest || source.Generation != 1 || source.SourceDigest != digest || source.Origin != "textbook" || len(source.SegmentRefs) != 1 {
						t.Fatalf("source page does not match its original checkpoint: %+v", source)
					}
				}
				if explicit == "saved_progress" && input.Context.ProgressSource != k12.CurriculumSourceParentConfirmed || explicit == "explicit_document" && input.Context.ProgressSource != "" {
					t.Fatalf("wrong progress provenance: %q", input.Context.ProgressSource)
				}
			}
			if !reflect.DeepEqual(before, ProgressSourceSnapshot(t, deps.Records.DB())) {
				t.Fatal("source selection changed saved profile, binding or progress")
			}
		})
	}
}

func TestUnitSummaryProgressSourceRejectsUnrelatedOrUnavailableSQLite(t *testing.T) {
	for _, mutation := range []string{"owner", "child", "grade", "volume", "generation", "missing_page", "other_subject"} {
		t.Run(mutation, func(t *testing.T) {
			deps, _ := ProgressSourceFixture(t)
			db := deps.Records.DB()
			req := ProgressSourceRequest()
			resolution := UnitSummaryResolution{Subject: "math", Intent: "generate", Format: "standard"}
			switch mutation {
			case "owner":
				req.OwnerID = "progress-other-owner"
			case "child":
				req.AgentName = "progress-other-child"
			case "grade":
				ProgressSourceExec(t, db, `UPDATE k12_curriculum_progress SET grade_term='六年级下' WHERE agent_name=?`, ProgressSourceAgent)
			case "volume":
				ProgressSourceExec(t, db, `UPDATE k12_curriculum_progress SET volume='上册' WHERE agent_name=?`, ProgressSourceAgent)
			case "generation":
				ProgressSourceExec(t, db, `INSERT INTO kb_semantic_document_generations(owner_id,corpus_uid,document_id,content_generation,created_at) VALUES(?,'progress-corpus',?,2,2)`, ProgressSourceOwner, ProgressSourceDocument)
				ProgressSourceExec(t, db, `UPDATE kb_semantic_document_bindings SET content_generation=2 WHERE document_id=?`, ProgressSourceDocument)
			case "missing_page":
				ProgressSourceExec(t, db, `DELETE FROM kb_ingest_page_checkpoints WHERE job_id='progress-ingest' AND page_number=5`)
			case "other_subject":
				resolution.Subject = "chinese"
				req.FinalUserText = "整理语文当前单元知识，不修改学习进度。"
			}
			before := ProgressSourceSnapshot(t, db)
			view, generator := ProgressSourceRun(t, deps, req, resolution)
			if view.Job.State != "needs_input" && view.Job.State != "failed" {
				t.Fatalf("unrelated or unavailable source was accepted: %+v", view.Job)
			}
			if view.Job.Context.Coverage.Level == "full" || len(view.Job.Context.Sources) != 0 {
				t.Fatalf("unavailable source was frozen as a complete unit: %+v", view.Job.Context)
			}
			select {
			case input := <-generator.generated:
				t.Fatalf("unrelated source reached generation: %+v", input.Context)
			default:
			}
			if !reflect.DeepEqual(before, ProgressSourceSnapshot(t, db)) {
				t.Fatal("source rejection changed saved profile, binding or progress")
			}
		})
	}
}
