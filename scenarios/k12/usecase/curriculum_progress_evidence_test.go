package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 本地完整批改夹具提供成功回执；来源关联与查询均使用隔离 SQLite，不调用真实 Provider 或 IM。
func TestCurriculumProgressHomeworkEvidenceScopesReceiptAndPreservesProgress(t *testing.T) {
	o, jobID, _, _, _ := completedProblemGroundingFixture(t)
	ctx := context.Background()
	job, err := o.deps.GetGradingJob(ctx, "mingming", jobID)
	if err != nil {
		t.Fatal(err)
	}
	db := o.deps.Records.DB()
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO k12_image_task_dispatches
			(dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,source_digest,
			 task_intent,status,target_object_type,target_object_id,classification_route_snapshot_json,
			 classification_invocation_id,route_policy_snapshot_json,idempotency_key,request_digest,created_at,updated_at)
			VALUES('evidence-dispatch','mingming','learner-1','desktop','evidence-message','[]','source',
			 'completed_homework','routed','homework_submission',?,'{}','classifier-evidence','{}','evidence-dispatch','request',100,999)`,
			[]any{job.Fields.SubmissionID}},
		{`INSERT INTO k12_image_task_owner_scopes(dispatch_id,owner_scope,agent_name,created_at)
			VALUES('evidence-dispatch','desktop-user','mingming',100)`, nil},
		{`INSERT INTO k12_homework_submissions
			(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,
			 task_intent,status,grading_job_id,idempotency_key,created_at,updated_at)
			VALUES(?,'evidence-dispatch','mingming','learner-1','desktop','evidence-message','[]',
			 'completed_homework','completed',?,'evidence-homework',100,999)`,
			[]any{job.Fields.SubmissionID, jobID}},
	}
	for _, query := range queries {
		if _, err := db.ExecContext(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	scope, found, err := o.deps.Records.GetActiveTextbookGroundingScope(ctx, k12storage.TextbookScope{
		OwnerID: "desktop-user", AgentName: "mingming", Subject: "math",
	})
	if err != nil || !found {
		t.Fatalf("active textbook scope: found=%v err=%v", found, err)
	}
	catalog := k12.CurriculumCatalog{
		AgentName: "mingming", Subject: "math", TextbookBindingID: scope.TextbookBindingID,
		TextbookManifestID: scope.TextbookManifestID, TextbookEdition: scope.Edition,
		TextbookVersion: "2022", Volume: scope.Volume,
		Units: []k12.CurriculumCatalogUnit{
			{UnitID: "u1", PageFrom: 1, PageTo: 1},
			{UnitID: "u2", PageFrom: 2, PageTo: 2},
		},
	}
	var changesBefore int
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"other-owner", "mingming"}, {"desktop-user", "other-child"}} {
		candidates, err := o.deps.Records.ListCurriculumProgressHomeworkCandidates(ctx, pair[0], pair[1])
		if err != nil || len(candidates) != 0 {
			t.Fatalf("candidate scope leaked another owner or child: scope=%v candidates=%+v err=%v", pair, candidates, err)
		}
	}
	var firstHash string
	for range 2 {
		unit, sourceAt, hash, found, err := o.deps.curriculumProgressHomeworkEvidence(ctx, "desktop-user", "mingming", catalog, scope, nil)
		if err != nil || !found || unit != "u1" || sourceAt != 100 || len(hash) != 64 {
			t.Fatalf("stored content suggestion: unit=%s sourceAt=%d hash=%q found=%v err=%v", unit, sourceAt, hash, found, err)
		}
		if firstHash != "" && hash != firstHash {
			t.Fatalf("read replay changed receipt hash: %s -> %s", firstHash, hash)
		}
		firstHash = hash
	}
	for _, test := range []struct {
		name    string
		owner   string
		agent   string
		scope   k12.TextbookGroundingScope
		current *k12.CurriculumProgress
	}{
		{name: "another owner", owner: "other-owner", agent: "mingming", scope: scope},
		{name: "another child", owner: "desktop-user", agent: "other-child", scope: scope},
		{name: "changed document generation", owner: "desktop-user", agent: "mingming", scope: func() k12.TextbookGroundingScope { value := scope; value.DocumentGeneration++; return value }()},
		{name: "changed source content", owner: "desktop-user", agent: "mingming", scope: func() k12.TextbookGroundingScope {
			value := scope
			value.SourceDigest = strings.Repeat("e", 64)
			return value
		}()},
		{name: "explicit progress is protected", owner: "desktop-user", agent: "mingming", scope: scope,
			current: &k12.CurriculumProgress{EvidenceSource: "parent_confirmed", UnitID: "u2"}},
		{name: "old review cannot lower adopted AI unit", owner: "desktop-user", agent: "mingming", scope: scope,
			current: &k12.CurriculumProgress{EvidenceSource: "ai_estimated", TextbookBindingID: scope.TextbookBindingID,
				TextbookManifestID: scope.TextbookManifestID, UnitID: "u2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			unit, _, _, found, err := o.deps.curriculumProgressHomeworkEvidence(ctx, test.owner, test.agent, catalog, test.scope, test.current)
			if err != nil || found || unit != "" {
				t.Fatalf("unusable source changed suggestion: unit=%s found=%v err=%v", unit, found, err)
			}
		})
	}
	var changesAfter int
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil || changesAfter != changesBefore {
		t.Fatalf("read-only suggestion mutated SQLite: before=%d after=%d err=%v", changesBefore, changesAfter, err)
	}
}

func TestCurriculumProgressHomeworkEvidenceRequiresUniqueUnitPageMapping(t *testing.T) {
	scope := k12.TextbookGroundingScope{TextbookBindingID: "binding", TextbookManifestID: "manifest",
		DocumentID: "document", DocumentGeneration: 1, SourceDigest: strings.Repeat("a", 64),
		PageRefs: []k12.TextbookGroundingPageRef{{LogicalPage: 5, PDFPage: 6, SegmentRefs: []string{"segment"}}}}
	receipt := ProblemGroundingReceipt{ProblemID: "problem", GroundingEvidenceReceipt: GroundingEvidenceReceipt{
		TextbookBindingID: "binding", TextbookManifestID: "manifest", DocumentID: "document", DocumentGeneration: 1,
		SourceDigest: scope.SourceDigest, LogicalPage: 5, PDFPage: 6, ChunkID: "segment"}}
	for _, test := range []struct {
		name  string
		units []k12.CurriculumCatalogUnit
	}{
		{name: "page outside catalog", units: []k12.CurriculumCatalogUnit{{UnitID: "first", PageFrom: 1, PageTo: 3}}},
		{name: "overlapping units", units: []k12.CurriculumCatalogUnit{{UnitID: "first", PageFrom: 1, PageTo: 5}, {UnitID: "second", PageFrom: 5, PageTo: 9}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			unit, found := curriculumProgressEvidenceUnit(k12.CurriculumCatalog{Units: test.units}, scope, nil, []ProblemGroundingReceipt{receipt})
			if found || unit != "" {
				t.Fatalf("ambiguous catalog produced progress: unit=%s found=%v", unit, found)
			}
		})
	}
	second := receipt
	second.LogicalPage, second.PDFPage, second.ChunkID = 8, 9, "second-segment"
	scope.PageRefs = append(scope.PageRefs, k12.TextbookGroundingPageRef{LogicalPage: 8, PDFPage: 9, SegmentRefs: []string{"second-segment"}})
	unit, found := curriculumProgressEvidenceUnit(k12.CurriculumCatalog{Units: []k12.CurriculumCatalogUnit{
		{UnitID: "first", PageFrom: 1, PageTo: 5}, {UnitID: "second", PageFrom: 6, PageTo: 9},
	}}, scope, nil, []ProblemGroundingReceipt{receipt, second})
	if found || unit != "" {
		t.Fatalf("multi-unit review selected a current unit: unit=%s found=%v", unit, found)
	}
}
