package usecase

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type groundingFailureProbe struct {
	calls     int
	err       error
	afterCall func()
}

func (p *groundingFailureProbe) GroundSnapshotWithEvidence(ctx context.Context, snapshot GroundingSnapshot, query, grade string) (GroundingSnapshotResult, error) {
	p.calls++
	if p.afterCall != nil {
		p.afterCall()
	}
	if p.err != nil {
		return GroundingSnapshotResult{}, p.err
	}
	return (&durableGroundingRetrievalProbe{}).GroundSnapshotWithEvidence(ctx, snapshot, query, grade)
}

type groundingRejectedResponse struct{}

func (groundingRejectedResponse) Error() string {
	return "private upstream response must not be persisted"
}
func (groundingRejectedResponse) ProviderResponseStatusCode() int { return 403 }

func TestGroundingFailureReceiptsSurviveReopenWithoutResend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		queryErr    error
		wantStatus  string
		wantPayload string
		cancel      bool
		writeFails  bool
	}{
		{"not sent", ErrGroundingQueryNotSent, "failed", `{"kind":"not_sent"}`, false, false},
		{"provider rejection", groundingRejectedResponse{}, "failed", `{"kind":"provider_response","status_code":403}`, true, false},
		{"connection lost", io.EOF, "outcome_unknown", "", false, false},
		{"deadline elapsed", context.DeadlineExceeded, "outcome_unknown", "", true, false},
		{"success after cancellation", nil, "succeeded", "", true, false},
		{"failure receipt cannot commit", ErrGroundingQueryNotSent, "running", "", false, true},
		{"unknown receipt cannot commit", io.EOF, "running", "", false, true},
		{"success receipt cannot commit", nil, "running", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "grounding.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := migrate.Run(context.Background(), db, migrate.All); err != nil {
				t.Fatal(err)
			}
			if tc.writeFails {
				if _, err := db.Exec(`CREATE TRIGGER reject_receipt BEFORE UPDATE ON k12_grounding_retrieval_invocations BEGIN SELECT RAISE(ABORT, 'receipt commit rejected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := GroundingSnapshot{AgentName: "child", LearnerID: "learner", Subject: "数学",
				TextbookBindingID: "binding", TextbookManifestID: "manifest", DocumentID: "document",
				DocumentGeneration: 1, SourceDigest: strings.Repeat("a", 64), VectorRevisionID: "revision-1",
				Edition: "人教版", Volume: "下册", SegmentRefs: []string{"segment-1"},
				PageRefs: []k12.TextbookGroundingPageRef{{LogicalPage: 1, PDFPage: 1, SegmentRefs: []string{"segment-1"}}}}
			q := RecognizedQuestion{ProblemID: "problem-1", AttemptID: "attempt-1", ConfirmedVersion: 1, InputDigest: "input-1", Question: "57+38="}
			req := GradeRequest{Subject: "数学", Grade: "五年级下"}
			newSession := func(store *k12storage.Store, probe *groundingFailureProbe) *gradingGroundingSession {
				return &gradingGroundingSession{required: true, snapshot: snapshot, evidenceSource: probe, retrieval: store,
					ownerID: "owner", agentName: "child", jobID: "job-1", items: make(map[string]*gradingGroundingItemState)}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := &groundingFailureProbe{err: tc.queryErr}
			if tc.cancel {
				probe.afterCall = cancel
			}
			_, firstErr := newSession(k12storage.NewStore(db, nil), probe).resolveItem(ctx, q, req)
			if tc.wantStatus == "succeeded" {
				if firstErr != nil {
					t.Fatalf("successful evidence lost: %v", firstErr)
				}
			} else if !errors.Is(firstErr, ErrGradingGroundingUnavailable) {
				t.Fatalf("failure must remain visible: %v", firstErr)
			}
			wantReconcile := tc.wantStatus == "running" || tc.wantStatus == "outcome_unknown"
			if errors.Is(firstErr, ErrModelInvocationRequiresReconciliation) != wantReconcile {
				t.Fatalf("incorrect failure classification: %v", firstErr)
			}
			if tc.writeFails && !strings.Contains(firstErr.Error(), "receipt commit rejected") {
				t.Fatalf("commit failure hidden: %v", firstErr)
			}
			if probe.calls != 1 {
				t.Fatalf("calls=%d want 1", probe.calls)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			var status, payload string
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM k12_grounding_retrieval_invocations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT status,result_json FROM k12_grounding_retrieval_invocations`).Scan(&status, &payload); err != nil {
				t.Fatal(err)
			}
			if count != 1 || status != tc.wantStatus {
				t.Fatalf("count/status=%d/%s want 1/%s", count, status, tc.wantStatus)
			}
			if status != "succeeded" && payload != tc.wantPayload {
				t.Fatalf("failure receipt=%q want %q", payload, tc.wantPayload)
			}
			replay := &groundingFailureProbe{}
			_, replayErr := newSession(k12storage.NewStore(db, nil), replay).resolveItem(context.Background(), q, req)
			if replay.calls != 0 {
				t.Fatalf("replay sent %d new queries", replay.calls)
			}
			if errors.Is(replayErr, ErrModelInvocationRequiresReconciliation) != wantReconcile {
				t.Fatalf("replay classification drifted: %v", replayErr)
			}
			if (replayErr == nil) != (status == "succeeded") {
				t.Fatalf("replay lost terminal result: %v", replayErr)
			}
		})
	}
}
