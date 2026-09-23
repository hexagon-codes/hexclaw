package k12storage_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func problemAssetStore(t *testing.T) (*k12storage.Store, string) {
	return problemAssetStoreWithMigrations(t, migrate.All)
}

func problemAssetStoreWithMigrations(t *testing.T, migrations []migrate.Migration) (*k12storage.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "assets.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err = migrate.Run(context.Background(), db, migrations); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO agents(name) VALUES('mingming')`); err != nil {
		t.Fatal(err)
	}
	registry := scenario.NewRegistry()
	if err = registry.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
		t.Fatal(err)
	}
	return k12storage.NewStore(db, registry.Records), path
}

func TestProblemAssets_AssessmentCommitChecksCurrentAdoption(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	v, _, err := s.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	receipts := make([]k12.GradingAssessmentItem, 0, 2)
	for _, key := range []string{"asset-assessment-one", "asset-assessment-two"} {
		job, attempt := seedItemLedgerFacts(t, s, key)
		a, _, err := s.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{OwnerID: v.OwnerID, JobID: job.RecordID,
			ProblemID: attempt.ProblemID, InputRevision: attempt.ConfirmedVersion, InputDigest: attempt.InputDigest,
			AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, FactsDigest: v.FactsDigest})
		if err != nil {
			t.Fatal(err)
		}
		grade := itemInvocation(job.RecordID, attempt, k12.GradingItemOperationGrade, 1)
		grade.InvocationID = "grade-" + job.RecordID
		if _, _, err := s.PrepareGradingItemInvocation(ctx, grade); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkGradingItemInvocationSent(ctx, grade.AgentName, grade.InvocationID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkGradingItemInvocationSucceeded(ctx, grade.AgentName, grade.InvocationID, "sha256:grade", "{\"correct\":true}"); err != nil {
			t.Fatal(err)
		}
		r := assessmentReceipt(job.RecordID, attempt, "", grade.InvocationID)
		r.Status = k12.GradingAssessmentCorrect
		r.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, FactsDigest: v.FactsDigest,
			AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, AdoptionID: a.AdoptionID}
		receipts = append(receipts, r)
	}
	first, created, err := s.CommitGradingAssessmentItem(ctx, receipts[0], k12storage.GradingAssessmentEffects{})
	if err != nil || !created || first.SolveInvocationID != "" || first.AnswerSource == nil {
		t.Fatalf("asset commit: %+v %v %v", first, created, err)
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CommitGradingAssessmentItem(ctx, receipts[1], k12storage.GradingAssessmentEffects{}); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("stale in-flight answer committed: %v", err)
	}
	replay, created, err := s.CommitGradingAssessmentItem(ctx, receipts[0], k12storage.GradingAssessmentEffects{})
	if err != nil || created || replay.ResultDigest != first.ResultDigest {
		t.Fatalf("historical receipt changed: %+v %v %v", replay, created, err)
	}
}

func TestProblemAssets_MigrationPreservesHistoricalAssessment(t *testing.T) {
	var old []migrate.Migration
	for _, m := range migrate.All {
		if m.Version < migrate.K12ProblemAssetsV107.Version {
			old = append(old, m)
		}
	}
	s, _ := problemAssetStoreWithMigrations(t, old)
	ctx := context.Background()
	job, attempt := seedItemLedgerFacts(t, s, "asset-legacy-assessment")
	solve, grade := successfulAssessmentInvocations(t, s, job.RecordID, attempt)
	_, err := s.DB().Exec(`INSERT INTO k12_grading_assessment_items
		(agent_name,job_id,problem_id,attempt_id,confirmed_version,input_digest,status,result_json,result_digest,
		solve_invocation_id,grade_invocation_id,projection_status,created_at,updated_at)
		VALUES('mingming',?,?,?,?,?,'correct','{"legacy":true}','sha256:legacy',?,?,'committed',100,100)`,
		job.RecordID, attempt.ProblemID, attempt.AttemptID, attempt.ConfirmedVersion, attempt.InputDigest, solve, grade)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, s.DB(), migrate.All); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListGradingAssessmentItems(ctx, "mingming", job.RecordID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("historical rows: %+v %v", rows, err)
	}
	r := rows[0]
	if r.ResultJSON != `{"legacy":true}` || r.ResultDigest != "sha256:legacy" || r.SolveInvocationID != solve || r.GradeInvocationID != grade || r.AnswerSource != nil {
		t.Fatalf("historical source rewritten: %+v", r)
	}
	var broken int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&broken); err != nil || broken != 0 {
		t.Fatalf("foreign keys=%d err=%v", broken, err)
	}
}

func problemAssetPublication(t *testing.T, s *k12storage.Store) k12.ProblemAssetPublication {
	t.Helper()
	ctx := context.Background()
	job, attempt := seedItemLedgerFacts(t, s, "asset-publication")
	inv := itemInvocation(job.RecordID, attempt, k12.GradingItemOperationSolve, 1)
	inv.ExecutionKind = k12.GradingExecutionLocalDeterministic
	if _, _, err := s.PrepareGradingItemInvocation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSent(ctx, inv.AgentName, inv.InvocationID); err != nil {
		t.Fatal(err)
	}
	raw := `{"Solution":"4","Evidence":{"Verdict":"agree","EvidenceType":"numeric_exec"}}`
	if _, err := s.MarkGradingItemInvocationSucceeded(ctx, inv.AgentName, inv.InvocationID, "sha256:verified-four", raw); err != nil {
		t.Fatal(err)
	}
	facts := k12.ProblemAssetFacts{Subject: "数学", Stem: "2+2=?"}
	identity, err := facts.ExactIdentity("desktop-user")
	if err != nil {
		t.Fatal(err)
	}
	return k12.ProblemAssetPublication{OwnerID: "desktop-user", PublicationID: "publication-one", Facts: facts, Answer: "4", AnswerResultJSON: raw,
		Verification: k12.ProblemAssetVerification{AgentName: inv.AgentName, InvocationID: inv.InvocationID, InputDigest: inv.InputDigest,
			ResultDigest: "sha256:verified-four", FactsDigest: identity.FactsDigest, Kind: k12.ProblemAnswerDeterministic, Policy: "deterministic-v1"}}
}

func assetAdoption(v k12.ProblemAssetVersion, job string) k12.ProblemAssetAdoption {
	return k12.ProblemAssetAdoption{OwnerID: v.OwnerID, JobID: job, ProblemID: "question-1", InputRevision: 1, InputDigest: "sha256:" + job,
		AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, FactsDigest: v.FactsDigest}
}

func TestProblemAssets_PublishAndAdoptDurable(t *testing.T) {
	s, path := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	v, created, err := s.PublishProblemAsset(ctx, p)
	if err != nil || !created || v.Answer != "4" || v.Version != 1 {
		t.Fatalf("publication: %+v %v %v", v, created, err)
	}
	if again, created, err := s.PublishProblemAsset(ctx, p); err != nil || created || again.AssetID != v.AssetID {
		t.Fatalf("publication replay: %+v %v %v", again, created, err)
	}
	adoption, created, err := s.AdoptProblemAsset(ctx, assetAdoption(v, "attempt-one"))
	if err != nil || !created {
		t.Fatalf("adoption: %+v %v %v", adoption, created, err)
	}
	if again, created, err := s.AdoptProblemAsset(ctx, assetAdoption(v, "attempt-one")); err != nil || created || again != adoption {
		t.Fatalf("adoption replay: %+v %v %v", again, created, err)
	}
	other, created, err := s.AdoptProblemAsset(ctx, assetAdoption(v, "attempt-two"))
	if err != nil || !created || other.AdoptionID == adoption.AdoptionID {
		t.Fatalf("independent answer input: %+v %v %v", other, created, err)
	}
	for table, want := range map[string]int{"k12_problem_assets": 1, "k12_problem_asset_versions": 1, "k12_problem_asset_publications": 1, "k12_problem_asset_adoptions": 2, "k12_grading_item_invocations": 1, "k12_model_physical_invocations": 0} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s count=%d want=%d error=%v", table, count, want, err)
		}
	}
	var events int
	if err = s.DB().QueryRow(`SELECT count(*) FROM outbox_events WHERE event_type='k12.problem_asset.published'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("publication event count=%d error=%v", events, err)
	}
	if err = s.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted := k12storage.NewStore(db, nil)
	got, err := restarted.FindExactProblemAsset(ctx, p.OwnerID, p.Facts)
	if err != nil || got.AssetID != v.AssetID || got.Answer != "4" {
		t.Fatalf("restart lost answer: %+v %v", got, err)
	}
	if got, err := restarted.GetProblemAssetAdoption(ctx, p.OwnerID, adoption.AdoptionID); err != nil || got != adoption {
		t.Fatalf("restart lost receipt: %+v %v", got, err)
	}
}

func TestProblemAssets_ArchivePreservesHistory(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	v, _, err := s.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.AdoptProblemAsset(ctx, assetAdoption(v, "attempt-one"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FindExactProblemAsset(ctx, p.OwnerID, p.Facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("archived still matches: %v", err)
	}
	if _, _, err = s.AdoptProblemAsset(ctx, assetAdoption(v, "attempt-two")); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("archived allowed new adoption: %v", err)
	}
	if err = s.ValidateProblemAssetAdoption(ctx, a.OwnerID, a.AdoptionID); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("inflight adoption still eligible: %v", err)
	}
	if history, err := s.GetProblemAssetAdoption(ctx, a.OwnerID, a.AdoptionID); err != nil || history != a {
		t.Fatalf("history mutated: %+v %v", history, err)
	}
	if _, created, err := s.PublishProblemAsset(ctx, p); err != nil || created {
		t.Fatalf("old publication replay: %v %v", created, err)
	}
	if _, err = s.FindExactProblemAsset(ctx, p.OwnerID, p.Facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("replay revived asset: %v", err)
	}
}

func TestProblemAssets_ConflictsPreserveOriginal(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	v, _, err := s.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	p.Answer = "5"
	p.AnswerResultJSON = `{"Solution":"5","Evidence":{"Verdict":"agree","EvidenceType":"numeric_exec"}}`
	if _, _, err = s.PublishProblemAsset(ctx, p); !errors.Is(err, k12storage.ErrProblemAssetConflict) {
		t.Fatalf("publication identity changed: %v", err)
	}
	p.PublicationID = "different-source"
	if _, _, err = s.PublishProblemAsset(ctx, p); !errors.Is(err, k12storage.ErrProblemAssetEvidence) {
		t.Fatalf("conflicting answer overwritten: %v", err)
	}
	got, err := s.FindExactProblemAsset(ctx, p.OwnerID, p.Facts)
	if err != nil || got.AssetID != v.AssetID || got.Answer != "4" {
		t.Fatalf("original lost: %+v %v", got, err)
	}
}

func TestProblemAssets_VerificationReceiptMustMatch(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	p.Verification.ResultDigest = "sha256:unrelated-result"
	if _, _, err := s.PublishProblemAsset(ctx, p); !errors.Is(err, k12storage.ErrProblemAssetEvidence) {
		t.Fatalf("mismatched proof published: %v", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_problem_assets`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed publication leaked head: %d %v", count, err)
	}
}
