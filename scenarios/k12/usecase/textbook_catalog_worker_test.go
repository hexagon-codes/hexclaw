package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestTextbookCatalogWorkerHeartbeatsAndPublishesOneBoundedJob(t *testing.T) {
	repo := &fakeTextbookCatalogRepository{
		claim: k12storage.TextbookCatalogJobClaim{
			JobID: "job-1", ManifestID: "manifest-1", LeaseOwner: "worker-1",
			LeaseEpoch: 1, Attempt: 1,
		},
		found:  true,
		source: syntheticTextbookCatalogSource(),
	}
	extractor := TextbookCatalogExtractorFunc(func(
		ctx context.Context, source k12storage.TextbookCatalogSource,
	) (k12storage.TextbookCatalogPublication, error) {
		select {
		case <-ctx.Done():
			return k12storage.TextbookCatalogPublication{}, ctx.Err()
		case <-time.After(70 * time.Millisecond):
		}
		return (TextbookCatalogCheckpointExtractor{}).Extract(ctx, source)
	})
	worker := NewTextbookCatalogWorker(repo, extractor, TextbookCatalogWorkerConfig{
		WorkerID: "worker-1", Lease: 45 * time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond, ExtractTimeout: time.Second,
		MaxAttempts: 3, RetryBase: time.Second, RetryMax: time.Minute,
	})

	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.renewals < 2 {
		t.Fatalf("lease renewals=%d want >=2 during extraction", repo.renewals)
	}
	if repo.publishes != 1 || len(repo.failures) != 0 {
		t.Fatalf("publishes/failures=%d/%d want 1/0", repo.publishes, len(repo.failures))
	}
}

func TestTextbookCatalogWorkerBoundsRetryAndFailsEvidenceErrorsTerminal(t *testing.T) {
	tests := []struct {
		name       string
		attempt    int
		extractErr error
		terminal   bool
		code       string
		message    string
	}{
		{name: "deterministic proof absent", attempt: 1, extractErr: ErrTextbookCatalogEvidenceInsufficient, terminal: true,
			code: "catalog_evidence_incomplete", message: "textbook catalog evidence insufficient"},
		{name: "conflicting printed page anchors", attempt: 1,
			extractErr: fmt.Errorf("%w: printed page anchors have conflicting offsets", ErrTextbookCatalogEvidenceInsufficient), terminal: true,
			code: "catalog_evidence_incomplete", message: "textbook catalog evidence insufficient: printed page anchors have conflicting offsets"},
		{name: "transient below max", attempt: 2, extractErr: errors.New("sqlite busy"), terminal: false,
			code: "catalog_transient_failure", message: "sqlite busy"},
		{name: "transient exhausted", attempt: 3, extractErr: errors.New("sqlite busy"), terminal: true,
			code: "catalog_transient_failure", message: "sqlite busy"},
		{name: "provider cause without body", attempt: 2,
			extractErr: errors.New(`catalog extraction failed: HTTP 429, body: {"error":{"message":"Provider returned error","code":429},"metadata":{"raw":"private-provider-body"}}`),
			code:       "catalog_transient_failure", message: "Provider returned error (code: 429)"},
		{name: "empty failure detail", attempt: 2, extractErr: errors.New(""),
			code: "catalog_transient_failure", message: "Textbook catalog extraction failed"},
		{name: "long ASCII failure", attempt: 2, extractErr: errors.New(strings.Repeat("a", 2048)),
			code: "catalog_transient_failure", message: strings.Repeat("a", 1024)},
		{name: "long Chinese failure", attempt: 2, extractErr: errors.New(strings.Repeat("中", 342)),
			code: "catalog_transient_failure", message: strings.Repeat("中", 341)},
		{name: "Chinese exact byte boundary", attempt: 2, extractErr: errors.New(strings.Repeat("中", 341) + "!"),
			code: "catalog_transient_failure", message: strings.Repeat("中", 341) + "!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeTextbookCatalogRepository{
				claim: k12storage.TextbookCatalogJobClaim{
					JobID: "job", ManifestID: "manifest", LeaseOwner: "worker",
					LeaseEpoch: 1, Attempt: tt.attempt,
				},
				found: true, source: syntheticTextbookCatalogSource(),
			}
			worker := NewTextbookCatalogWorker(repo, TextbookCatalogExtractorFunc(func(
				context.Context, k12storage.TextbookCatalogSource,
			) (k12storage.TextbookCatalogPublication, error) {
				return k12storage.TextbookCatalogPublication{}, tt.extractErr
			}), TextbookCatalogWorkerConfig{
				WorkerID: "worker", Lease: time.Second, HeartbeatInterval: 500 * time.Millisecond,
				ExtractTimeout: time.Second, MaxAttempts: 3,
				RetryBase: 2 * time.Second, RetryMax: 10 * time.Second,
				Now: func() time.Time { return time.UnixMilli(10_000) },
			})
			processed, err := worker.RunOnce(context.Background())
			if !processed || err != tt.extractErr {
				t.Fatalf("processed=%v err=%v want unchanged extraction error", processed, err)
			}
			if len(repo.failures) != 1 || repo.failures[0].Terminal != tt.terminal {
				t.Fatalf("failure=%+v want terminal=%v", repo.failures, tt.terminal)
			}
			if message := repo.failures[0].Message; len(message) > 1024 || !utf8.ValidString(message) {
				t.Fatalf("failure message bytes=%d validUTF8=%v", len(message), utf8.ValidString(message))
			}
			if repo.failures[0].Code != tt.code || repo.failures[0].Message != tt.message {
				t.Fatalf("failure=%+v want code=%q message=%q", repo.failures[0], tt.code, tt.message)
			}
			if !tt.terminal && !repo.failures[0].RetryAt.After(time.UnixMilli(10_000)) {
				t.Fatalf("retry deadline=%v must be in the future", repo.failures[0].RetryAt)
			}
			if tt.terminal && !repo.failures[0].RetryAt.IsZero() {
				t.Fatalf("terminal failure scheduled a retry: %v", repo.failures[0].RetryAt)
			}
		})
	}
}

type fakeTextbookCatalogRepository struct {
	mu        sync.Mutex
	claim     k12storage.TextbookCatalogJobClaim
	found     bool
	claimErr  error
	source    k12storage.TextbookCatalogSource
	sourceErr error
	renewals  int
	publishes int
	failures  []k12storage.TextbookCatalogFailure
}

func (f *fakeTextbookCatalogRepository) RecoverTextbookCatalogJobs(context.Context, time.Time, int) error {
	return nil
}

func (f *fakeTextbookCatalogRepository) ClaimTextbookCatalogJob(
	context.Context, string, time.Time, time.Duration,
) (k12storage.TextbookCatalogJobClaim, bool, error) {
	return f.claim, f.found, f.claimErr
}

func (f *fakeTextbookCatalogRepository) RenewTextbookCatalogJob(
	context.Context, k12storage.TextbookCatalogJobClaim, time.Time, time.Duration,
) error {
	f.mu.Lock()
	f.renewals++
	f.mu.Unlock()
	return nil
}

func (f *fakeTextbookCatalogRepository) LoadTextbookCatalogSource(
	context.Context, k12storage.TextbookCatalogJobClaim, time.Time,
) (k12storage.TextbookCatalogSource, error) {
	return f.source, f.sourceErr
}

func (f *fakeTextbookCatalogRepository) PublishTextbookCatalog(
	context.Context, k12storage.TextbookCatalogJobClaim,
	k12storage.TextbookCatalogPublication, time.Time,
) error {
	f.mu.Lock()
	f.publishes++
	f.mu.Unlock()
	return nil
}

func (f *fakeTextbookCatalogRepository) FailTextbookCatalogJob(
	_ context.Context, _ k12storage.TextbookCatalogJobClaim,
	failure k12storage.TextbookCatalogFailure, _ time.Time,
) error {
	f.mu.Lock()
	f.failures = append(f.failures, failure)
	f.mu.Unlock()
	return nil
}
