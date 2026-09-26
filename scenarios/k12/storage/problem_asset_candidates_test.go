package k12storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func candidatePublication(t *testing.T, s *k12storage.Store, id, owner, stem, answer string) k12.ProblemAssetPublication {
	t.Helper()
	ctx := context.Background()
	job, attempt := seedItemLedgerFacts(t, s, id)
	inv := itemInvocation(job.RecordID, attempt, k12.GradingItemOperationSolve, 1)
	inv.InvocationID = "candidate-solve:" + id
	inv.ExecutionKind = k12.GradingExecutionLocalDeterministic
	if _, _, err := s.PrepareGradingItemInvocation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSent(ctx, inv.AgentName, inv.InvocationID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"Solution": answer, "Evidence": map[string]string{"Verdict": "agree", "EvidenceType": "numeric_exec"}})
	if _, err := s.MarkGradingItemInvocationSucceeded(ctx, inv.AgentName, inv.InvocationID, "sha256:"+id, string(raw)); err != nil {
		t.Fatal(err)
	}
	facts := k12.ProblemAssetFacts{Subject: "数学", Stem: stem, AnswerContext: map[string]string{"grade_term": "六年级上"}}
	identity, err := facts.ExactIdentity(owner)
	if err != nil {
		t.Fatal(err)
	}
	return k12.ProblemAssetPublication{OwnerID: owner, PublicationID: id, Facts: facts, Answer: answer, AnswerResultJSON: string(raw),
		Verification: k12.ProblemAssetVerification{AgentName: inv.AgentName, InvocationID: inv.InvocationID, InputDigest: inv.InputDigest, ResultDigest: "sha256:" + id,
			FactsDigest: identity.FactsDigest, Kind: k12.ProblemAnswerDeterministic, Policy: "local-deterministic-v1"}}
}

func TestProblemAssetCandidatesRepresentativeQueries(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	for i, pair := range [][2]string{{"18+3=", "21"}, {"18-3=", "15"}, {"18*3=", "54"}, {"3/18=", "1/6"}} {
		p := candidatePublication(t, s, fmt.Sprintf("distractor-%d", i), "desktop-user", pair[0], pair[1])
		if _, _, err := s.PublishProblemAsset(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for i, sample := range []struct{ stored, query, terms, answer string }{
		{`\(18 \div 3 =\)`, "18 / 3 =", `"18" AND "3"`, "6"},
		{"7×8=", `\(7 \times 8 =\)`, `"7" AND "8"`, "56"},
		{"56÷7=", "56 ÷\n7 =", `"56" AND "7"`, "8"},
	} {
		p := candidatePublication(t, s, fmt.Sprintf("representative-%d", i), "desktop-user", sample.stored, sample.answer)
		v, _, err := s.PublishProblemAsset(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		facts := p.Facts
		facts.Stem = sample.query
		if _, err = s.FindExactProblemAsset(ctx, p.OwnerID, facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("expected exact miss: %v", err)
		}
		var count int
		if err = s.DB().QueryRow(`SELECT COUNT(*) FROM k12_problem_asset_candidates_fts WHERE k12_problem_asset_candidates_fts MATCH ? AND owner_id=?`, sample.terms, p.OwnerID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		got, err := s.FindEquivalentProblemAsset(ctx, p.OwnerID, facts)
		elapsed := time.Since(start)
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Fatalf("candidate: %+v %v", got, err)
		}
		t.Logf("representative=%d candidate_rows=%d lookup_elapsed=%s exact_miss=true answer=%s", i, count, elapsed, got.Answer)
		if _, err = s.FindEquivalentProblemAsset(ctx, "other-owner", facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("owner leaked: %v", err)
		}
		if err = s.ArchiveProblemAsset(ctx, p.OwnerID, v.AssetID, v.Revision); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FindEquivalentProblemAsset(ctx, p.OwnerID, facts); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("archived candidate returned: %v", err)
		}
	}
}

func TestProblemAssetCandidatesMigrationAndExactIndependence(t *testing.T) {
	var previous []migrate.Migration
	for _, m := range migrate.All {
		if m.Version < 114 {
			previous = append(previous, m)
		}
	}
	s, _ := problemAssetStoreWithMigrations(t, previous)
	ctx := context.Background()
	p := candidatePublication(t, s, "before-candidate-index", "desktop-user", `\(18 \div 3 =\)`, "6")
	original, _, err := s.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Run(ctx, s.DB(), migrate.All); err != nil {
		t.Fatal(err)
	}
	facts := p.Facts
	facts.Stem = "18 / 3 ="
	got, err := s.FindEquivalentProblemAsset(ctx, p.OwnerID, facts)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("backfill: %+v %v", got, err)
	}
	if err = s.ArchiveProblemAsset(ctx, p.OwnerID, original.AssetID, original.Revision); err != nil {
		t.Fatal(err)
	}
	replacement := candidatePublication(t, s, "replacement-indexed", p.OwnerID, p.Facts.Stem, "6")
	replacement.ReplacesVersion, replacement.ExpectedRevision = 1, 2
	newVersion, _, err := s.PublishProblemAsset(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.FindEquivalentProblemAsset(ctx, p.OwnerID, facts)
	if err != nil || !reflect.DeepEqual(got, newVersion) || got.Version != 2 {
		t.Fatalf("current replacement: %+v %v", got, err)
	}
	old, err := s.GetProblemAssetVersion(ctx, p.OwnerID, original.AssetID, 1)
	if err != nil || !reflect.DeepEqual(old, original) {
		t.Fatalf("migration rewrote history: %+v %v", old, err)
	}
	var count int
	if err = s.DB().QueryRow(`SELECT COUNT(*) FROM k12_problem_asset_candidates_fts WHERE asset_id=?`, original.AssetID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("version index count=%d err=%v", count, err)
	}
	// 在回滚事务中验证派生索引随源版本删除，原始历史不会被测试改写。
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM k12_problem_asset_publications WHERE owner_id=? AND publication_id=?`, p.OwnerID, p.PublicationID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM k12_problem_asset_versions WHERE owner_id=? AND asset_id=? AND asset_version=1`, p.OwnerID, original.AssetID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_problem_asset_candidates_fts WHERE asset_id=?`, original.AssetID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("deleted version remained indexed: %d %v", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`DROP TABLE k12_problem_asset_candidates_fts`); err != nil {
		t.Fatal(err)
	}
	got, err = s.FindExactProblemAsset(ctx, p.OwnerID, p.Facts)
	if err != nil || !reflect.DeepEqual(got, newVersion) {
		t.Fatalf("exact lookup depended on FTS: %+v %v", got, err)
	}
}
