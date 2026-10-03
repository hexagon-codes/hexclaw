package k12storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

func TestGroundingRetrievalInvocationClaimAndSuccessSurviveRestartWithoutDuplicate(t *testing.T) {
	db, err := sqlitefixture.Memory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, nil)
	claim := GroundingRetrievalInvocationClaim{
		OwnerID: "owner", AgentName: "child", JobID: "job-1", ProblemID: "problem-1",
		Operation: "k12_grounding_retrieval", GroundingSnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		QueryDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		DocumentID:  "doc-1", DocumentGeneration: 1, RevisionID: "revision-1",
		ProfileConfigHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ScopeDigest:       "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Provider:          "hexclaw-gpt", Model: "gpt-5.6-luna",
	}
	first, err := store.ClaimGroundingRetrievalInvocation(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Fresh || first.Status != GroundingRetrievalInvocationStatusRunning || first.InvocationID == "" {
		t.Fatalf("first claim=%+v", first)
	}
	second, err := NewStore(db, nil).ClaimGroundingRetrievalInvocation(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fresh || second.InvocationID != first.InvocationID {
		t.Fatalf("duplicate retrieval claim=%+v first=%+v", second, first)
	}
	result := GroundingRetrievalInvocationResult{
		ResultJSON:         `{"text":"教材命中","found":true}`,
		QueryReceiptDigest: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		HitSetDigest:       "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		CitationSetDigest:  "1111111111111111111111111111111111111111111111111111111111111111",
		Provider:           claim.Provider,
		Model:              claim.Model,
		RevisionID:         claim.RevisionID,
		ProfileConfigHash:  claim.ProfileConfigHash,
	}
	if err := store.SaveGroundingRetrievalInvocation(ctx, first, result); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewStore(db, nil).ClaimGroundingRetrievalInvocation(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Fresh || restarted.Status != GroundingRetrievalInvocationStatusSucceeded ||
		restarted.ResultJSON != result.ResultJSON || restarted.HitSetDigest != result.HitSetDigest {
		t.Fatalf("restart did not reuse retrieval result=%+v", restarted)
	}
	if err := store.SaveGroundingRetrievalInvocation(ctx, first, result); err != nil {
		t.Fatalf("same result must be idempotent: %v", err)
	}
}

func TestGroundingRetrievalTerminalReceiptsCannotOverwriteEachOther(t *testing.T) {
	for _, terminal := range []string{"succeeded", "failed", "outcome_unknown"} {
		t.Run(terminal, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipts.db")
			db, err := sqlitefixture.Open(path, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			if err := migrate.Run(ctx, db, migrate.All); err != nil {
				t.Fatal(err)
			}
			store := NewStore(db, nil)
			claim := GroundingRetrievalInvocationClaim{OwnerID: "owner", AgentName: "child", JobID: "job", ProblemID: "problem", Operation: "k12_grounding_retrieval",
				GroundingSnapshotDigest: strings.Repeat("a", 64), QueryDigest: strings.Repeat("b", 64)}
			invocation, err := store.ClaimGroundingRetrievalInvocation(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			result := GroundingRetrievalInvocationResult{ResultJSON: `{"found":true}`, QueryReceiptDigest: strings.Repeat("c", 64), HitSetDigest: strings.Repeat("d", 64), CitationSetDigest: strings.Repeat("e", 64)}
			failure := GroundingRetrievalFailure{Kind: "not_sent"}
			writes := map[string]func() error{
				"succeeded":       func() error { return store.SaveGroundingRetrievalInvocation(ctx, invocation, result) },
				"failed":          func() error { return store.MarkGroundingRetrievalInvocationFailed(ctx, invocation, failure) },
				"outcome_unknown": func() error { return store.MarkGroundingRetrievalInvocationOutcomeUnknown(ctx, invocation, "") },
			}
			if err := writes[terminal](); err != nil {
				t.Fatal(err)
			}
			before, err := store.ClaimGroundingRetrievalInvocation(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			for target, write := range writes {
				if err := write(); (err == nil) != (target == terminal) {
					t.Fatalf("%s -> %s error=%v", terminal, target, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := NewStore(db, nil).ClaimGroundingRetrievalInvocation(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			if after.Fresh || after.Status != before.Status || after.ResultJSON != before.ResultJSON || after.UpdatedAt != before.UpdatedAt {
				t.Fatalf("terminal receipt changed: before=%+v after=%+v", before, after)
			}
		})
	}
}
