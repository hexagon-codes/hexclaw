package k12storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

// 真实临时 SQLite 保存验证回执、发布与练习；模型边界可控，不访问外部服务。
func practiceAssetFixture(t *testing.T) (*k12storage.Store, usecase.Deps, k12.ProblemAssetVersion, string) {
	t.Helper()
	ctx := context.Background()
	s, _ := problemAssetStore(t)
	if _, err := s.DB().Exec(`INSERT INTO agents(name) VALUES('lele')`); err != nil {
		t.Fatal(err)
	}
	p := problemAssetPublication(t, s)
	p.Facts.AnswerContext = map[string]string{"grade_term": "五年级下"}
	identity, err := p.Facts.ExactIdentity(p.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	p.Verification.FactsDigest = identity.FactsDigest
	inv, err := s.GetGradingItemInvocation(ctx, "mingming", p.Verification.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	grade := inv
	grade.InvocationID, grade.Operation, grade.RequestDigest = "practice-asset-grade", k12.GradingItemOperationGrade, "sha256:practice-grade"
	if _, _, err := s.PrepareGradingItemInvocation(ctx, grade); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSent(ctx, grade.AgentName, grade.InvocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkGradingItemInvocationSucceeded(ctx, grade.AgentName, grade.InvocationID, "sha256:practice-grade-result", `{"correct":true}`); err != nil {
		t.Fatal(err)
	}
	receipt := assessmentReceipt(inv.JobID, k12.Attempt{ProblemID: inv.ProblemID, AttemptID: inv.AttemptID,
		ConfirmedVersion: inv.InputRevision, InputDigest: inv.InputDigest}, inv.InvocationID, grade.InvocationID)
	receipt.Status = k12.GradingAssessmentCorrect
	receipt.ResultJSON = `{"Recognized":{"Question":"2+2=?","Subject":"数学","AnswerState":"present","StudentAnswer":"4","KnowledgePoints":["整数加法"]},"Status":"correct"}`
	if _, _, err := s.CommitGradingAssessmentItem(ctx, receipt, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	v, _, err := s.PublishAssessedProblemAsset(ctx, p, inv.JobID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := k12.NewMistakeRecord("lele", "practice-session", k12.MistakeFields{
		Subject: "数学", Question: "1+1=?", KnowledgePoint: "整数加法", CanonicalAnswer: "2", GradeTerm: "五年级下",
		EntrySource: k12.MistakeEntryPhoto, ErrorCause: "计算失误",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, source); err != nil {
		t.Fatal(err)
	}
	return s, usecase.Deps{Records: s, TextbookOwnerID: "desktop-user", Now: func() int64 { return 1000 },
		PracticeGenerationRoute: func(_ context.Context, snapshot k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
			snapshot.Route = snapshot.Provider + "/" + snapshot.Model
			snapshot.Capability = "text"
			return snapshot, nil
		},
	}, v, source.RecordID
}

func practiceAssetQuery() k12.PracticeAssetQuery {
	return k12.PracticeAssetQuery{OwnerID: "desktop-user", AgentName: "lele", Subject: "数学", GradeTerm: "五年级下",
		KnowledgePoint: "整数加法", OriginalQuestion: "1+1=?"}
}

func practiceAssetRequest(key string) usecase.SinglePracticeGenerationRequest {
	return usecase.SinglePracticeGenerationRequest{IdempotencyKey: key, Grade: "五年级下", Textbook: "人教版", Difficulty: "same", Provider: "test", Model: "model"}
}

func TestPracticeAssets_TargetHistoryAndOriginalReview(t *testing.T) {
	s, _, v, _ := practiceAssetFixture(t)
	ctx := context.Background()
	base := practiceAssetQuery()
	for _, field := range []string{"owner", "subject", "grade", "point", "missing"} {
		q := base
		switch field {
		case "owner":
			q.OwnerID = "other-owner"
		case "subject":
			q.Subject = "语文"
		case "grade":
			q.GradeTerm = "一年级上"
		case "point":
			q.KnowledgePoint = "整数乘法"
		case "missing":
			q.KnowledgePoint = ""
		}
		if _, err := s.FindPracticeProblemAsset(ctx, q); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
			t.Fatalf("%s mismatch selected an asset: %v", field, err)
		}
	}
	if got, err := s.FindPracticeProblemAsset(ctx, base); err != nil || got.Version.AssetID != v.AssetID {
		t.Fatalf("same owner different child asset unavailable: %+v %v", got, err)
	}
	practiced := base
	practiced.AgentName = "mingming"
	if _, err := s.FindPracticeProblemAsset(ctx, practiced); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("new practice ignored historical graded answer: %v", err)
	}
	original := base
	original.OriginalQuestion = "2+2=?"
	if _, err := s.FindPracticeProblemAsset(ctx, original); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("new variant repeated source question: %v", err)
	}
	rec, err := k12.NewPracticeSetRecord("lele", "history", k12.PracticeSetFields{SourceKind: k12.PracticeSourceManual, Title: "历史练习",
		Items: []k12.PracticeItem{{ItemID: "legacy-item", Subject: "数学", QuestionMarkdown: "2+2=?", ExpectedAnswerMarkdown: "4", VerificationStatus: k12.PracticeItemVerified}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindPracticeProblemAsset(ctx, base); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("new practice repeated practiced item: %v", err)
	}
	original.OriginalReview = true
	if _, err := s.FindPracticeProblemAsset(ctx, original); err != nil {
		t.Fatalf("original review excluded historical question: %v", err)
	}
	got, err := s.Get(ctx, rec.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := k12.ParsePracticeSetFields(got.Fields)
	if err != nil || legacy.Items[0].AssetSource != nil {
		t.Fatalf("legacy null source changed: %+v %v", legacy, err)
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindPracticeProblemAsset(ctx, original); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("archived asset selected: %v", err)
	}
}

func TestPracticeAssets_EntryReusesWithoutCallsAndFreezesIndependentPaper(t *testing.T) {
	s, d, v, source := practiceAssetFixture(t)
	ctx := context.Background()
	pending, err := d.StartSinglePracticeGeneration(ctx, "lele", source, practiceAssetRequest("asset-entry"))
	if err != nil {
		t.Fatal(err)
	}
	joined, err := d.ProcessSinglePracticeGeneration(ctx, "lele", pending.GenerationJobID)
	if err != nil || joined.Item == nil || joined.Item.AssetSource == nil || joined.Item.QuestionMarkdown != "2+2=?" {
		t.Fatalf("asset entry: %+v %v", joined, err)
	}
	calls, err := s.ListPracticeGenerationInvocations(ctx, "lele", pending.GenerationJobID)
	if err != nil || len(calls) != 0 {
		t.Fatalf("fabricated practice calls: %+v %v", calls, err)
	}
	job, err := s.GetPracticeGenerationJobByID(ctx, "lele", pending.GenerationJobID)
	if err != nil || job.GenerationOutput != "" || job.ValidationOutput != "" || job.Attempt != 0 {
		t.Fatalf("fabricated outputs: %+v %v", job, err)
	}
	finalized, _, err := d.FinalizeBasket(ctx, "lele", joined.PracticeSetID, "print")
	if err != nil {
		t.Fatal(err)
	}
	item := finalized.Fields.Items[0]
	if item.PracticeProblemID == "" || item.PracticeProblemID == source || item.PracticeProblemID == v.AssetID || item.ResultCorrect != nil || item.Returned {
		t.Fatalf("asset answer inherited student attempt: %+v", item)
	}
	before := k12.RenderPaperMarkdown(finalized.Fields, k12.PaperKindQuestion, k12.PaperMeta{})
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := d.GetPracticeSet(ctx, "lele", finalized.Record.RecordID)
	if err != nil || !reflect.DeepEqual(finalized.Fields, after.Fields) {
		t.Fatalf("historical paper changed: %+v %v", after.Fields, err)
	}
	if printed := k12.RenderPaperMarkdown(after.Fields, k12.PaperKindQuestion, k12.PaperMeta{}); before != printed {
		t.Fatal("printed snapshot was rewritten")
	}
	replay, err := d.ProcessSinglePracticeGeneration(ctx, "lele", pending.GenerationJobID)
	if err != nil || replay.Item == nil || replay.Item.AssetSource == nil {
		t.Fatalf("historical job replay rechecks archived source: %+v %v", replay, err)
	}
}

func TestPracticeAssets_ArchiveBeforeCommitRollsBack(t *testing.T) {
	s, d, v, sourceID := practiceAssetFixture(t)
	ctx := context.Background()
	candidate, err := s.FindPracticeProblemAsset(ctx, practiceAssetQuery())
	if err != nil {
		t.Fatal(err)
	}
	pending, err := d.StartSinglePracticeGeneration(ctx, "lele", sourceID, practiceAssetRequest("asset-stop-before-commit"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.GetPracticeGenerationJobByID(ctx, "lele", pending.GenerationJobID)
	if err != nil {
		t.Fatal(err)
	}
	item := k12.PracticeItem{ItemID: job.ResultItemIDs[0], SourceProblemID: sourceID, SourceMistakeSummary: job.SourceSummary,
		Subject: "数学", QuestionMarkdown: "2+2=?", ExpectedAnswerMarkdown: "4", VerificationStatus: k12.PracticeItemVerified,
		GenerationStatus: k12.PracticeItemGenerationReady, GenerationJobID: job.GenerationJobID, AssetSource: &candidate.Source,
		NormalizedContentHash: "frozen-candidate", AddedVia: k12.PracticeAddedViaSingleVariant}
	rec, err := k12.NewPracticeSetRecord("lele", "practice-session", k12.PracticeSetFields{SourceKind: k12.PracticeSourceSingleVariant, Title: "待打印", Items: []k12.PracticeItem{item}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CommitPracticeGeneration(ctx, rec, -1, job); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("stale adoption committed: %v", err)
	}
	current, err := s.GetPracticeGenerationJobByID(ctx, "lele", job.GenerationJobID)
	if err != nil || current.Status != k12.PracticeGenerationQueued {
		t.Fatalf("failed commit mutated job: %+v %v", current, err)
	}
	var sets int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_sets WHERE agent_name='lele'`).Scan(&sets); err != nil || sets != 0 {
		t.Fatalf("partial basket=%d %v", sets, err)
	}
	// 归档后回到既有 Generate+Solve，由真实调用次数和账本证明分支，不能伪造资产采用。
	generated, validated := 0, 0
	d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
		generated++
		return usecase.SolveResult{Solution: "## 问题\n3+3=?\n\n## 答案\n6"}, nil
	})
	d.Solver = practiceAssetFallbackSolver{calls: &validated}
	joined, err := d.ProcessSinglePracticeGeneration(ctx, "lele", job.GenerationJobID)
	if err != nil || generated != 1 || validated != 1 || joined.Item == nil || joined.Item.AssetSource != nil {
		t.Fatalf("fallback: generated=%d validated=%d view=%+v err=%v", generated, validated, joined, err)
	}
	calls, err := s.ListPracticeGenerationInvocations(ctx, "lele", job.GenerationJobID)
	if err != nil || len(calls) != 2 || calls[0].Status != k12.ModelInvocationSucceeded || calls[1].Status != k12.ModelInvocationSucceeded {
		t.Fatalf("fallback call receipts: %+v %v", calls, err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(current.RequestSnapshot), &snapshot); err != nil || snapshot["knowledge_point"] != "整数加法" {
		t.Fatalf("target snapshot changed: %v %v", snapshot, err)
	}
}

type practiceAssetFallbackSolver struct{ calls *int }

func (s practiceAssetFallbackSolver) Solve(context.Context, string, string, string) (usecase.SolveResult, error) {
	*s.calls++
	return usecase.SolveResult{Solution: "## 答案\n6", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec}}, nil
}

func TestPracticeAssets_MigrationPreservesPrintedHistory(t *testing.T) {
	var previous []migrate.Migration
	for _, migration := range migrate.All {
		if migration.Version < migrate.K12PracticeAssetSourceV113.Version {
			previous = append(previous, migration)
		}
	}
	s, _ := problemAssetStoreWithMigrations(t, previous)
	ctx := context.Background()
	if _, err := s.DB().Exec(`INSERT INTO k12_practice_sets
		(record_id,agent_name,status,source_kind,title,paper_no,finalized_at,finalized_via,dedupe_key,created_at,updated_at)
		VALUES('legacy-paper','mingming','assigned','manual','历史试卷','P-OLD-01',100,'print','legacy-paper',100,100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO k12_practice_set_items
		(set_record_id,item_index,item_id,subject,question_markdown,expected_answer_markdown,verification_status,paper_seq,practice_problem_id)
		VALUES('legacy-paper',0,'legacy-item','数学','2+2=?','4','verified',1,'legacy-practice-problem')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(ctx, s.DB(), migrate.All); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Get(ctx, "legacy-paper")
	if err != nil {
		t.Fatal(err)
	}
	fields, err := k12.ParsePracticeSetFields(rec.Fields)
	if err != nil || len(fields.Items) != 1 || fields.Items[0].AssetSource != nil || fields.Items[0].PracticeProblemID != "legacy-practice-problem" ||
		fields.Items[0].QuestionMarkdown != "2+2=?" || fields.Items[0].ExpectedAnswerMarkdown != "4" || fields.PaperNo != "P-OLD-01" || fields.FinalizedAt != 100 {
		t.Fatalf("migration rewrote historical paper: %+v %v", fields, err)
	}
}
