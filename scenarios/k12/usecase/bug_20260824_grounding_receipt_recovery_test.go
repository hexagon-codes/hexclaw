package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type groundingRecoveryNoSecondSearch struct {
	calls int
}

func (s *groundingRecoveryNoSecondSearch) fail() error {
	s.calls++
	return errors.New("restart must not search grounding again")
}

func (s *groundingRecoveryNoSecondSearch) Ground(
	context.Context, string, string, string,
) (string, bool, error) {
	return "", false, s.fail()
}

func (s *groundingRecoveryNoSecondSearch) FreezeGroundingSnapshot(
	context.Context, GroundingSnapshot,
) (GroundingSnapshot, error) {
	return GroundingSnapshot{}, s.fail()
}

func (s *groundingRecoveryNoSecondSearch) GroundSnapshot(
	context.Context, GroundingSnapshot, string, string,
) (string, bool, error) {
	return "", false, s.fail()
}

func (s *groundingRecoveryNoSecondSearch) GroundSnapshotWithEvidence(
	context.Context, GroundingSnapshot, string, string,
) (GroundingSnapshotResult, error) {
	return GroundingSnapshotResult{}, s.fail()
}

func groundingRecoveryDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func groundingRecoveryReceipt() GroundingEvidenceReceipt {
	return GroundingEvidenceReceipt{
		TextbookBindingID:  "binding-recovery",
		TextbookManifestID: "manifest-recovery",
		DocumentID:         "document-recovery",
		DocumentGeneration: 7,
		VectorRevisionID:   "revision-recovery",
		QueryDigest:        "sha256:" + groundingRecoveryDigest("本次批改查询"),
		ChunkID:            "chunk-recovery",
		LogicalPage:        54,
		PDFPage:            57,
		SourceDigest:       strings.Repeat("a", 64),
		CitationDigest:     groundingRecoveryDigest("本次批改实际消费的教材命中"),
	}
}

// 历史终稿重启后只从已关联的 page-summary 成功回执恢复教材证据，
// 不再次调用检索或 Provider，也不创建新的 summary。
func TestBUG20260824GroundingReceiptRecoversFromPageSummaryWithoutSecondCall(t *testing.T) {
	fixture := prepareFinalSummaryCrashFixture(t)
	fixture.tips.Sections[0].SourceLabel = TutoringTipsSourceTextbook
	fixture.tips.GroundingEvidenceReceipts = []GroundingEvidenceReceipt{
		groundingRecoveryReceipt(),
	}
	resultJSON, err := json.Marshal(fixture.tips)
	if err != nil {
		t.Fatal(err)
	}
	fixture.resultJSON = string(resultJSON)
	if _, err := fixture.orchestrator.deps.Records.MarkModelInvocationSucceededWithResult(
		context.Background(),
		fixture.invocation.AgentName,
		fixture.invocation.InvocationID,
		modelInvocationResultDigest(fixture.tips),
		fixture.resultJSON,
		"",
	); err != nil {
		t.Fatalf("persist page-summary receipt: %v", err)
	}
	assessments, err := fixture.orchestrator.deps.Records.ListGradingAssessmentItems(
		context.Background(), fixture.job.Record.AgentName, fixture.job.Record.RecordID,
	)
	if err != nil || len(assessments) != 1 {
		t.Fatalf("load historical final assessment: count=%d err=%v", len(assessments), err)
	}
	ordered, err := json.Marshal([]string{assessments[0].ResultDigest})
	if err != nil {
		t.Fatal(err)
	}
	legacyArtifact := k12.GradingFinalArtifact{
		AgentName: fixture.job.Record.AgentName, JobID: fixture.job.Record.RecordID,
		StructureVersion: k12.GradingFinalArtifactStructureVersion,
		CoverageStatus:   k12.GradingFinalArtifactCoverageComplete, TotalCount: 1, PublishedCount: 1,
		OrderedCurrentDigestsJSON: string(ordered),
		CanonicalMarkdown:         renderCanonicalGradingFinal(nil, &fixture.tips),
		SummaryInvocationID:       fixture.invocation.InvocationID,
		CreatedAt:                 fixture.orchestrator.deps.now(), UpdatedAt: fixture.orchestrator.deps.now(),
	}
	legacyArtifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(legacyArtifact)
	if _, _, err := fixture.orchestrator.deps.Records.CommitGradingFinalArtifact(
		context.Background(), legacyArtifact, 0,
	); err != nil {
		t.Fatalf("persist historical final artifact: %v", err)
	}

	restarted := fixture.restartedFinalizer()
	grounding := &groundingRecoveryNoSecondSearch{}
	restarted.deps.Grounding = grounding
	artifact, err := restarted.finalizeGradingPage(
		context.Background(), fixture.run, fixture.job,
	)
	if err != nil {
		t.Fatalf("restart finalization: %v", err)
	}
	if fixture.provider.calls != 0 {
		t.Fatalf("restart repeated Provider %d times", fixture.provider.calls)
	}
	if grounding.calls != 0 {
		t.Fatalf("restart repeated grounding %d times", grounding.calls)
	}
	if artifact.SummaryInvocationID != fixture.invocation.InvocationID {
		t.Fatalf("summary invocation=%q want %q",
			artifact.SummaryInvocationID, fixture.invocation.InvocationID)
	}

	projection, err := restarted.ImageTaskHomeworkProjection(
		context.Background(), fixture.job.Record.AgentName, fixture.job.Record.RecordID,
	)
	if err != nil {
		t.Fatalf("public homework projection: %v", err)
	}
	want := []GroundingEvidenceReceipt{groundingRecoveryReceipt()}
	if !reflect.DeepEqual(projection.GroundingEvidenceReceipts, want) {
		t.Fatalf("restarted receipt=%+v want %+v",
			projection.GroundingEvidenceReceipts, want)
	}
	if fixture.provider.calls != 0 || grounding.calls != 0 {
		t.Fatalf("public recovery repeated calls: Provider=%d grounding=%d", fixture.provider.calls, grounding.calls)
	}

	stored, err := restarted.deps.Records.GetModelInvocation(
		context.Background(), fixture.invocation.AgentName, fixture.invocation.InvocationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var durable TutoringTips
	if err := json.Unmarshal([]byte(stored.ResultJSON), &durable); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(durable.GroundingEvidenceReceipts, want) {
		t.Fatalf("durable page-summary receipt=%+v want %+v",
			durable.GroundingEvidenceReceipts, want)
	}
}

func TestBUG20260824CorruptDurableGroundingReceiptFailsClosed(t *testing.T) {
	fixture := prepareFinalSummaryCrashFixture(t)
	fixture.tips.Sections[0].SourceLabel = TutoringTipsSourceTextbook
	receipt := groundingRecoveryReceipt()
	receipt.QueryDigest = "sha256:not-a-digest"
	fixture.tips.GroundingEvidenceReceipts = []GroundingEvidenceReceipt{receipt}
	raw, err := json.Marshal(fixture.tips)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.orchestrator.deps.Records.MarkModelInvocationSucceededWithResult(
		context.Background(),
		fixture.invocation.AgentName,
		fixture.invocation.InvocationID,
		modelInvocationDigest(raw),
		string(raw),
		"",
	); err != nil {
		t.Fatal(err)
	}
	assessments, err := fixture.orchestrator.deps.Records.ListGradingAssessmentItems(
		context.Background(), fixture.job.Record.AgentName, fixture.job.Record.RecordID,
	)
	if err != nil || len(assessments) != 1 {
		t.Fatalf("load historical final assessment: count=%d err=%v", len(assessments), err)
	}
	ordered, err := json.Marshal([]string{assessments[0].ResultDigest})
	if err != nil {
		t.Fatal(err)
	}
	legacyArtifact := k12.GradingFinalArtifact{
		AgentName: fixture.job.Record.AgentName, JobID: fixture.job.Record.RecordID,
		StructureVersion: k12.GradingFinalArtifactStructureVersion,
		CoverageStatus:   k12.GradingFinalArtifactCoverageComplete, TotalCount: 1, PublishedCount: 1,
		OrderedCurrentDigestsJSON: string(ordered),
		CanonicalMarkdown:         renderCanonicalGradingFinal(nil, &fixture.tips),
		SummaryInvocationID:       fixture.invocation.InvocationID,
		CreatedAt:                 fixture.orchestrator.deps.now(), UpdatedAt: fixture.orchestrator.deps.now(),
	}
	legacyArtifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(legacyArtifact)
	if _, _, err := fixture.orchestrator.deps.Records.CommitGradingFinalArtifact(
		context.Background(), legacyArtifact, 0,
	); err != nil {
		t.Fatalf("persist historical final artifact: %v", err)
	}
	restarted := fixture.restartedFinalizer()
	grounding := &groundingRecoveryNoSecondSearch{}
	restarted.deps.Grounding = grounding
	if _, err := restarted.finalizeGradingPage(
		context.Background(), fixture.run, fixture.job,
	); err != nil {
		t.Fatalf("read historical final artifact: %v", err)
	}
	if _, err := restarted.ImageTaskHomeworkProjection(
		context.Background(), fixture.job.Record.AgentName, fixture.job.Record.RecordID,
	); !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("corrupt durable grounding receipt was not rejected: %v", err)
	}
	if fixture.provider.calls != 0 || grounding.calls != 0 {
		t.Fatalf("corrupt durable receipt repeated calls: Provider=%d grounding=%d", fixture.provider.calls, grounding.calls)
	}
}

func TestBUG20260824GroundingReceiptIdentityDoesNotEnterCanonicalMarkdown(t *testing.T) {
	receipt := groundingRecoveryReceipt()
	tips := TutoringTips{
		Sections: []TutoringTipsSection{{
			Title: "这页在练什么", Content: "教材中的本次辅导依据",
			SourceLabel: TutoringTipsSourceTextbook,
		}},
		GroundingEvidenceReceipts: []GroundingEvidenceReceipt{receipt},
	}
	markdown := renderCanonicalGradingFinal(nil, &tips)
	for _, internal := range []string{
		receipt.TextbookBindingID,
		receipt.TextbookManifestID,
		receipt.DocumentID,
		receipt.VectorRevisionID,
		receipt.QueryDigest,
		receipt.ChunkID,
		receipt.SourceDigest,
		receipt.CitationDigest,
	} {
		if strings.Contains(markdown, internal) {
			t.Fatalf("canonical Markdown leaked grounding identity %q: %s", internal, markdown)
		}
	}
}
