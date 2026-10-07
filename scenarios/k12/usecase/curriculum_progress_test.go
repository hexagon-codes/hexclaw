package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func progressFixtureScope() k12storage.TextbookScope {
	return k12storage.TextbookScope{OwnerID: "desktop-user", AgentName: "auto-child", Subject: "math"}
}

func newAutoProgressFixture(t *testing.T) (usecase.Deps, *sql.DB, *int64) {
	t.Helper()
	d := newDataDeps(t, "auto-child")
	db := d.Records.DB()
	date, err := time.Parse(time.RFC3339, "2026-10-06T12:00:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	clock := date.Unix()
	d.Now = func() int64 { return clock }
	profile := k12.ChildProfile{ChildName: "示例孩子", GradeTerm: "五年级上", SubjectTextbooks: k12.SubjectTextbooks{Math: "人教版", Chinese: "统编版", English: "外研版", Science: "教科版", InformationTechnology: "浙教版", Art: "人美版"}}
	meta, err := json.Marshal(k12.ApplyProfileToMeta(map[string]string{"scenario": "k12-tutor"}, profile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agents SET metadata=? WHERE name='auto-child'`, string(meta)); err != nil {
		t.Fatal(err)
	}
	seedAutoProgressCatalog(t, db, "auto-manifest", "desktop-user", "五年级上")
	return d, db, &clock
}

func seedAutoProgressCatalog(t *testing.T, db *sql.DB, id, owner, grade string) {
	t.Helper()
	doc, corpus := "doc-"+id, "corpus-"+owner
	digest := strings.Repeat("a", 64)
	catalog := k12.CurriculumCatalog{GradeTerm: grade, Subject: "math", TextbookEdition: "人教版", TextbookVersion: "2025", Title: "AI 生成示例 · 非真实学生作业教材目录", Volume: "上册", PageMin: 1, PageMax: 20, Units: []k12.CurriculumCatalogUnit{{UnitID: "u1", Title: "第一单元", PageFrom: 1, PageTo: 4, Lessons: []k12.CurriculumCatalogLesson{{LessonID: "l1", Title: "第一课", PageFrom: 1, PageTo: 2}, {LessonID: "l2", Title: "第二课", PageFrom: 3, PageTo: 4}}}, {UnitID: "u2", Title: "第二单元", PageFrom: 5, PageTo: 20}}}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	refs := make([]map[string]any, 0, 20)
	for page := 1; page <= 20; page++ {
		refs = append(refs, map[string]any{"logical_page": page, "pdf_page": page, "segment_refs": []string{"segment-" + doc}})
	}
	payload["page_refs"] = refs
	raw, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT OR IGNORE INTO kb_semantic_corpora(corpus_uid,owner_id,corpus_alias,kind,content_version,created_at,updated_at) VALUES(?,?,'default','general',1,1,1)`, corpus, owner)
	exec(`INSERT INTO kb_documents(id,title,content,source,deleted,corpus_uid,created_at,updated_at) VALUES(?,?,'fixture',?,0,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, doc, doc+".pdf", "upload:"+doc+".pdf", corpus)
	exec(`INSERT INTO kb_chunks(id,doc_id,content,chunk_index,created_at,page_start,page_end,source_digest,source_offset_start,source_offset_end) VALUES(?,?,'fixture',0,CURRENT_TIMESTAMP,1,20,?,0,7)`, "segment-"+doc, doc, digest)
	exec(`INSERT INTO kb_semantic_document_generations(owner_id,corpus_uid,document_id,content_generation,created_at) VALUES(?,?,?,1,1)`, owner, corpus, doc)
	exec(`INSERT INTO kb_semantic_document_bindings(document_id,owner_id,corpus_uid,content_generation,lifecycle_state,text_state,version,created_at,updated_at) VALUES(?,?,?,1,'active','ready',1,1,1)`, doc, owner, corpus)
	exec(`INSERT INTO k12_textbook_manifests(manifest_id,owner_id,document_id,document_generation,document_title,subject,source_digest,state,retryable,failure_message,text_index_state,vector_index_state,catalog_json,catalog_digest,created_at,updated_at) VALUES(?,?,?,1,?,'math',?,'ready_for_confirmation',0,'','ready','ready',?,?,1,1)`, id, owner, doc, doc+".pdf", digest, string(raw), strings.Repeat("b", 64))
	exec(`INSERT INTO k12_textbook_page_mappings(mapping_id,manifest_id,logical_page,pdf_page,evidence_page,evidence_offset_start,evidence_offset_end,evidence_digest,method,verification_state,document_id,document_generation,source_digest,created_at,updated_at) VALUES(?,?,1,1,1,0,1,?,'printed_anchor','verified',?,1,?,1,1)`, "proof-"+id, id, strings.Repeat("c", 64), doc, digest)
	exec(`INSERT INTO k12_textbook_manifest_segments(segment_id,manifest_id,logical_page,segment_ref,pdf_page,document_id,document_generation,source_digest,created_at,updated_at) VALUES(?,?,1,?,1,?,1,?,1,1)`, "ref-"+id, id, "segment-"+doc, doc, digest)
	for page := 2; page <= 20; page++ {
		exec(`INSERT INTO k12_textbook_page_mappings(mapping_id,manifest_id,logical_page,pdf_page,evidence_page,evidence_offset_start,evidence_offset_end,evidence_digest,method,verification_state,document_id,document_generation,source_digest,created_at,updated_at) VALUES(?,?,?,?,?,0,1,?,'printed_anchor','verified',?,1,?,1,1)`, fmt.Sprintf("proof-%s-%d", id, page), id, page, page, page, strings.Repeat("c", 64), doc, digest)
		exec(`INSERT INTO k12_textbook_manifest_segments(segment_id,manifest_id,logical_page,segment_ref,pdf_page,document_id,document_generation,source_digest,created_at,updated_at) VALUES(?,?,?,?,?,?,1,?,1,1)`, fmt.Sprintf("ref-%s-%d", id, page), id, page, "segment-"+doc, page, doc, digest)
	}
}

func TestAutoProgressPreviewReadOnlyAndPartialValuesPreserved(t *testing.T) {
	d, db, _ := newAutoProgressFixture(t)
	ctx := context.Background()
	var before, after int
	_ = db.QueryRow(`SELECT total_changes()`).Scan(&before)
	p, err := d.PreviewCurriculumProgress(ctx, "desktop-user", "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.EvidenceSource != "ai_estimated" || p.ConfirmedAt != 0 || p.Revision != 0 || p.EstimateBasis == nil {
		t.Fatalf("preview=%+v", p)
	}
	_ = db.QueryRow(`SELECT total_changes()`).Scan(&after)
	if before != after {
		t.Fatalf("preview wrote data: %d -> %d", before, after)
	}
	page := 1
	profileState, err := d.Records.GetProfileState(ctx, "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	profile := k12.ChildProfile{ChildName: profileState.ChildName, GradeTerm: profileState.GradeTerm, SubjectTextbooks: profileState.SubjectTextbooks}
	selection := k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: "auto-manifest", UnitID: "", LessonID: "l1", PageFrom: &page, PageTo: &page, EvidenceSource: "ai_estimated"}
	r, err := d.ResolveCurriculumProgress(ctx, usecase.CurriculumProgressResolveRequest{OwnerID: "desktop-user", AgentName: "auto-child", Profile: profile, Selection: &selection, RequireSelectedProgress: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Progress.UnitID != "u1" || r.Progress.LessonID != "l1" || r.Progress.RequestedPageFrom == nil || *r.Progress.RequestedPageFrom != 1 || r.Progress.EvidenceSource != "ai_estimated" {
		t.Fatalf("partial values lost: %+v", r.Progress)
	}
	adopted, err := d.Records.SaveCurriculumProgress(ctx, progressFixtureScope(), profile, r.Progress, 0, d.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err = d.EnsureCurriculumProgress(ctx, "desktop-user", "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	if p.LessonID != "l1" || p.RequestedPageFrom == nil || *p.RequestedPageFrom != 1 || p.UnitID != "u1" || p.Revision != adopted.Revision {
		t.Fatalf("automatic refresh lost user calibration: %+v", p)
	}
}

func TestAutoProgressExplicitSelectionScopeMismatchRejectsWriteOnly(t *testing.T) {
	for _, tc := range []struct {
		name, grade, volume string
	}{
		{name: "grade", grade: "四年级上", volume: "上册"},
		{name: "volume", grade: "五年级上", volume: "下册"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, db, _ := newAutoProgressFixture(t)
			if _, err := db.Exec(`UPDATE k12_textbook_manifests SET catalog_json=json_set(catalog_json,'$.volume',?) WHERE manifest_id='auto-manifest'`, tc.volume); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			state, err := d.Records.GetProfileState(ctx, "auto-child")
			if err != nil {
				t.Fatal(err)
			}
			profile := k12.ChildProfile{ChildName: state.ChildName, GradeTerm: tc.grade, SubjectTextbooks: state.SubjectTextbooks}
			page := 1
			selection := k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: "auto-manifest", UnitID: "", LessonID: "l1", PageFrom: &page, PageTo: &page, EvidenceSource: k12.CurriculumSourceAIEstimated}
			req := usecase.CurriculumProgressResolveRequest{OwnerID: "desktop-user", AgentName: "auto-child", Profile: profile, Selection: &selection}
			var before, after int
			if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			preview, err := d.ResolveCurriculumProgress(ctx, req)
			if err != nil || preview.Progress != nil {
				t.Fatalf("mismatched preview=%+v err=%v", preview, err)
			}
			req.RequireSelectedProgress = true
			write, err := d.ResolveCurriculumProgress(ctx, req)
			if !errors.Is(err, usecase.ErrInvalidInput) || write.Progress != nil {
				t.Fatalf("mismatched explicit write=%+v err=%v", write, err)
			}
			req.Selection = nil
			missing, err := d.ResolveCurriculumProgress(ctx, req)
			if err != nil || missing.Progress != nil {
				t.Fatalf("missing selection without matching catalog=%+v err=%v", missing, err)
			}
			if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("scope resolution wrote data: %d -> %d", before, after)
			}
		})
	}
}

func TestAutoProgressProfileCommandScopeMismatchPreservesStoredState(t *testing.T) {
	d, db, _ := newAutoProgressFixture(t)
	ctx := context.Background()
	state, err := d.Records.GetProfileState(ctx, "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	page := 1
	req := usecase.UpdateProfileBundleRequest{
		OwnerID: "desktop-user", AgentName: "auto-child", IdempotencyKey: "scope-mismatch-command",
		ExpectedProfileRevision: state.Revision,
		Profile:                 k12.ChildProfile{ChildName: "新档案草稿", GradeTerm: "五年级下", SubjectTextbooks: state.SubjectTextbooks},
		CurriculumProgress:      usecase.CurriculumProgressInput{Subject: "math", TextbookManifestID: "auto-manifest", LessonID: "l1", PageFrom: &page, PageTo: &page, EvidenceSource: k12.CurriculumSourceAIEstimated},
		WeeklyPracticeSettings:  usecase.WeeklyPracticeSettingsInput{Timezone: "Asia/Shanghai", ArithmeticMinutes: 2},
	}
	var before, after int
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateProfileBundle(ctx, req); !errors.Is(err, usecase.ErrInvalidInput) {
		t.Fatalf("scope mismatch command=%v", err)
	}
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("rejected profile command wrote data: %d -> %d", before, after)
	}
	stored, err := d.Records.GetProfileState(ctx, "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ChildName != state.ChildName || stored.GradeTerm != state.GradeTerm || stored.Revision != state.Revision {
		t.Fatalf("rejected command changed profile: before=%+v after=%+v", state, stored)
	}
}

func TestAutoProgressProtectsConfirmationAndRejectsStaleCAS(t *testing.T) {
	d, _, clock := newAutoProgressFixture(t)
	ctx := context.Background()
	p, err := d.SetConfirmedCurriculumProgress(ctx, usecase.ConfirmedCurriculumProgressRequest{OwnerID: "desktop-user", AgentName: "auto-child", ExpectedProgressRevision: 0, Selection: k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: "auto-manifest", Volume: "上册", UnitID: "u1", LessonID: "l1"}})
	if err != nil {
		t.Fatal(err)
	}
	*clock += 60 * 60 * 24 * 80
	preview, err := d.PreviewCurriculumProgress(ctx, "desktop-user", "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	if preview.EvidenceSource != "parent_confirmed" || preview.UnitID != "u1" || preview.ConfirmedAt != p.ConfirmedAt || preview.Revision != p.Revision {
		t.Fatalf("confirmation overwritten: %+v", preview)
	}
	_, err = d.SetConfirmedCurriculumProgress(ctx, usecase.ConfirmedCurriculumProgressRequest{OwnerID: "desktop-user", AgentName: "auto-child", ExpectedProgressRevision: 0, Selection: k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: "auto-manifest", Volume: "上册", UnitID: "u2"}})
	if err != records.ErrVersionConflict {
		t.Fatalf("stale CAS=%v", err)
	}
}

func TestAutoProgressAmbiguousOrMissingCatalogDoesNotInventLearning(t *testing.T) {
	d, db, _ := newAutoProgressFixture(t)
	seedAutoProgressCatalog(t, db, "duplicate-manifest", "desktop-user", "五年级上")
	var before, after int
	_ = db.QueryRow(`SELECT total_changes()`).Scan(&before)
	p, err := d.PreviewCurriculumProgress(context.Background(), "desktop-user", "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatalf("ambiguous candidate picked arbitrarily: %+v", p)
	}
	_ = db.QueryRow(`SELECT total_changes()`).Scan(&after)
	if before != after {
		t.Fatal("suggestion wrote learning state")
	}
	_, err = db.Exec(`UPDATE k12_textbook_manifests SET state='failed_retryable',retryable=1`)
	if err != nil {
		t.Fatal(err)
	}
	p, err = d.PreviewCurriculumProgress(context.Background(), "desktop-user", "auto-child")
	if err != nil || p != nil {
		t.Fatalf("missing catalog blocks normal flow: %+v %v", p, err)
	}
}

func TestAutoProgressProfileCommandReplayKeepsOriginalEstimateAcrossDates(t *testing.T) {
	d, _, clock := newAutoProgressFixture(t)
	ctx := context.Background()
	state, err := d.Records.GetProfileState(ctx, "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	req := usecase.UpdateProfileBundleRequest{OwnerID: "desktop-user", AgentName: "auto-child", IdempotencyKey: "auto-original-command", ExpectedProfileRevision: state.Revision, AutoCurriculumProgress: true, Profile: k12.ChildProfile{ChildName: state.ChildName, GradeTerm: state.GradeTerm, SubjectTextbooks: state.SubjectTextbooks}, WeeklyPracticeSettings: usecase.WeeklyPracticeSettingsInput{Timezone: "Asia/Shanghai", ArithmeticMinutes: 2}}
	first, err := d.UpdateProfileBundle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.CurriculumProgress == nil {
		t.Fatal("first command did not adopt estimate")
	}
	*clock += 80 * 86400
	replay, err := d.UpdateProfileBundle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.CurriculumProgress.Revision != first.CurriculumProgress.Revision || replay.CurriculumProgress.EstimateBasis.AsOfDate != first.CurriculumProgress.EstimateBasis.AsOfDate {
		t.Fatalf("replay recomputed estimate: first=%+v replay=%+v", first.CurriculumProgress, replay.CurriculumProgress)
	}
	_, head, err := d.Records.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil || head != first.CurriculumProgress.Revision {
		t.Fatalf("replay advanced lifecycle %d %v", head, err)
	}
	req.AutoCurriculumProgress = false
	req.ClearCurriculumProgress = true
	if _, err := d.UpdateProfileBundle(ctx, req); err != records.ErrVersionConflict {
		t.Fatalf("null aliases omitted command: %v", err)
	}
}

type autoProgressControlledCandidates struct{ calls int }

func (c *autoProgressControlledCandidates) FreezeWeeklyPracticeCandidateRequest(_ context.Context, r usecase.WeeklyPracticeCandidateRequest) (usecase.WeeklyPracticeCandidateRequest, error) {
	return r, nil
}
func (c *autoProgressControlledCandidates) GenerateWeeklyPracticeCandidates(context.Context, usecase.WeeklyPracticeCandidateRequest) ([]usecase.WeeklyPracticeCandidate, error) {
	c.calls++
	return []usecase.WeeklyPracticeCandidate{}, nil
}

func TestAutoProgressPlanAdoptionPreservesReplayAndClearedDraft(t *testing.T) {
	d, _, clock := newAutoProgressFixture(t)
	ctx := context.Background()
	state, err := d.Records.GetProfileState(ctx, "auto-child")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.UpdateProfileBundle(ctx, usecase.UpdateProfileBundleRequest{OwnerID: "desktop-user", AgentName: "auto-child", IdempotencyKey: "plan-settings", ExpectedProfileRevision: state.Revision, AutoCurriculumProgress: true, Profile: k12.ChildProfile{ChildName: state.ChildName, GradeTerm: state.GradeTerm, SubjectTextbooks: state.SubjectTextbooks}, WeeklyPracticeSettings: usecase.WeeklyPracticeSettingsInput{Timezone: "Asia/Shanghai", ArithmeticMinutes: 2, TextbookConsolidationEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &autoProgressControlledCandidates{}
	d.WeeklyCandidates = provider
	_, _, err = d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "auto-child", IdempotencyKey: "original-plan"})
	if err != nil {
		t.Fatal(err)
	}
	first, head, err := d.Records.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil {
		t.Fatal(err)
	}
	*clock += 86400
	calls := provider.calls
	_, replay, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "auto-child", IdempotencyKey: "original-plan"})
	if err != nil || !replay {
		t.Fatalf("old command replay=%v err=%v", replay, err)
	}
	kept, keptHead, err := d.Records.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil {
		t.Fatal(err)
	}
	if keptHead != head || kept.EstimateBasis.AsOfDate != first.EstimateBasis.AsOfDate || provider.calls != calls {
		t.Fatal("old command adopted a new date or called provider")
	}
	_, _, err = d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "auto-child", IdempotencyKey: "new-plan"})
	if err != nil {
		t.Fatal(err)
	}
	_, adoptedHead, err := d.Records.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil || adoptedHead != head+1 {
		t.Fatalf("new opt-in command did not adopt: %d %v", adoptedHead, err)
	}
	profile := k12.ChildProfile{ChildName: state.ChildName, GradeTerm: state.GradeTerm, SubjectTextbooks: state.SubjectTextbooks}
	_, err = d.Records.SaveCurriculumProgress(ctx, progressFixtureScope(), profile, nil, adoptedHead, d.Now())
	if err != nil {
		t.Fatal(err)
	}
	calls = provider.calls
	_, _, err = d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "auto-child", IdempotencyKey: "after-clear"})
	if err != nil {
		t.Fatal(err)
	}
	p, clearedHead, err := d.Records.GetCurriculumProgressState(ctx, "auto-child", "math")
	if err != nil || p != nil || clearedHead != adoptedHead+1 || provider.calls != calls {
		t.Fatalf("cleared draft revived: p=%+v head=%d calls=%d/%d err=%v", p, clearedHead, provider.calls, calls, err)
	}
}
