package k12storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func practiceAssetSelectionRequest(key string) usecase.PracticeCandidateSelectionRequest {
	return usecase.PracticeCandidateSelectionRequest{IdempotencyKey: key, Grade: "五年级下", Textbook: "人教版", Provider: "test", Model: "model"}
}

func practiceAssetPaperRequest(key string) usecase.CustomPaperRequest {
	return usecase.CustomPaperRequest{IdempotencyKey: key, Scope: "unmastered", Total: "all", PerSource: 2,
		Difficulty: "same", Textbook: "人教版", Grade: "五年级下", Provider: "test", Model: "model"}
}

// 只替换生成与验算边界；资产发布、候选、任务、练习及打印均使用真实临时 SQLite。
func practiceAssetGenerator(calls *int, after func()) usecase.PracticeVariantGeneratorFunc {
	return func(context.Context, string, string, string) (usecase.SolveResult, error) {
		*calls++
		if after != nil {
			after()
		}
		return usecase.SolveResult{Solution: fmt.Sprintf("## 问题\n%d+%d=?\n\n## 答案\n6", *calls, 6-*calls)}, nil
	}
}

func TestPracticeAssets_CandidatesMixSourcesExcludePreviousAndKeepOriginalReview(t *testing.T) {
	s, d, v, source := practiceAssetFixture(t)
	ctx := context.Background()
	generated, solved := 0, 0
	d.PracticeVariant = practiceAssetGenerator(&generated, nil)
	d.Solver = practiceAssetFallbackSolver{&solved}
	selection, err := d.OpenPracticeCandidateSelection(ctx, "lele", source, practiceAssetSelectionRequest("asset-candidates"))
	if err != nil || len(selection.Candidates) != 4 || generated != 2 || solved != 2 {
		t.Fatalf("three candidate source split: %+v generate=%d solve=%d err=%v", selection, generated, solved, err)
	}
	assetID, originalID := "", ""
	for _, candidate := range selection.Candidates {
		if candidate.CandidateKind == k12.PracticeCandidateOriginal {
			originalID = candidate.CandidateID
		}
		if candidate.Problem.AssetSource != nil {
			if assetID != "" || candidate.Problem.AssetSource.AssetID != v.AssetID || candidate.QuestionMarkdown != "2+2=?" {
				t.Fatalf("duplicated or false source: %+v", candidate)
			}
			assetID = candidate.CandidateID
		}
	}
	if assetID == "" || originalID == "" {
		t.Fatal("missing asset/original candidate")
	}
	selection, err = d.GeneratePracticeCandidateBatch(ctx, "lele", selection.SelectionID,
		usecase.PracticeCandidateBatchRequest{Revision: selection.Revision, IdempotencyKey: "asset-next-batch"})
	if err != nil || generated != 5 || solved != 5 {
		t.Fatalf("next batch repeated asset instead of fallback: generate=%d solve=%d err=%v", generated, solved, err)
	}
	for _, candidate := range selection.Candidates {
		if candidate.BatchOrdinal == 2 && candidate.Problem.AssetSource != nil {
			t.Fatalf("previous candidate reused: %+v", candidate)
		}
	}
	input := k12storage.PracticeCandidateCommitInput{AgentName: "lele", SelectionID: selection.SelectionID,
		Revision: selection.Revision, CandidateIDs: []string{assetID, originalID}, IdempotencyKey: "commit-asset-candidates"}
	committed, err := d.CommitPracticeCandidateSelection(ctx, input)
	if err != nil || committed.AddedCount != 2 {
		t.Fatalf("commit: %+v %v", committed, err)
	}
	printed, _, err := d.FinalizeBasket(ctx, "lele", selection.TargetSetRecordID, "print")
	if err != nil {
		t.Fatal(err)
	}
	var originalProblemID string
	for _, item := range printed.Fields.Items {
		if item.ItemID == assetID && (item.AssetSource == nil || item.AssetSource.AssetID != v.AssetID || item.VerificationEvidence != "numeric_exec") {
			t.Fatalf("adoption source lost: %+v", item)
		}
		if item.PracticeProblemID == "" || item.ResultCorrect != nil || item.Returned {
			t.Fatalf("student work inherited from asset: %+v", item)
		}
		if item.ItemID == originalID {
			originalProblemID = item.PracticeProblemID
		}
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if receipt, err := d.CommitPracticeCandidateSelection(ctx, input); err != nil || !receipt.Replayed {
		t.Fatalf("committed replay revalidated archived asset: %+v %v", receipt, err)
	}
	after, err := d.GetPracticeSet(ctx, "lele", printed.Record.RecordID)
	if err != nil || !reflect.DeepEqual(printed.Fields, after.Fields) {
		t.Fatalf("old print changed: %+v %v", after, err)
	}
	// 原题复习不受新练已练排除影响，新打印继续分配独立作答身份。
	d.PracticeVariant, d.Solver = nil, nil
	review, err := d.OpenPracticeCandidateSelection(ctx, "lele", source, practiceAssetSelectionRequest("repeat-original"))
	if err != nil {
		t.Fatal(err)
	}
	var reviewID string
	for _, candidate := range review.Candidates {
		if candidate.CandidateKind == k12.PracticeCandidateOriginal {
			reviewID = candidate.CandidateID
		}
	}
	if _, err := d.CommitPracticeCandidateSelection(ctx, k12storage.PracticeCandidateCommitInput{AgentName: "lele", SelectionID: review.SelectionID,
		Revision: review.Revision, CandidateIDs: []string{reviewID}, IdempotencyKey: "commit-original-review"}); err != nil {
		t.Fatal(err)
	}
	reprinted, _, err := d.FinalizeBasket(ctx, "lele", review.TargetSetRecordID, "print")
	if err != nil || len(reprinted.Fields.Items) != 1 || reprinted.Fields.Items[0].PracticeProblemID == originalProblemID ||
		reprinted.Fields.Items[0].ResultCorrect != nil || reprinted.Fields.Items[0].AssetSource != nil {
		t.Fatalf("original review identity: %+v %v", reprinted.Fields, err)
	}
}

func TestPracticeAssets_CandidateStoppedBeforeCommitLeavesNoPartialBasket(t *testing.T) {
	for _, change := range []string{"archive", "target"} {
		t.Run(change, func(t *testing.T) {
			s, d, v, source := practiceAssetFixture(t)
			ctx := context.Background()
			selection, err := d.OpenPracticeCandidateSelection(ctx, "lele", source, practiceAssetSelectionRequest("candidate-stop"))
			if err != nil {
				t.Fatal(err)
			}
			var selected []string
			for _, candidate := range selection.Candidates {
				if candidate.State == k12.PracticeCandidateReady {
					selected = append(selected, candidate.CandidateID)
				}
			}
			if len(selected) != 2 {
				t.Fatalf("asset must work without generator: %+v", selection)
			}
			if change == "archive" {
				err = s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision)
			} else {
				_, err = s.DB().Exec(`UPDATE k12_mistakes SET knowledge_point='整数乘法' WHERE record_id=?`, source)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.CommitPracticeCandidateSelection(ctx, k12storage.PracticeCandidateCommitInput{AgentName: "lele", SelectionID: selection.SelectionID,
				Revision: selection.Revision, CandidateIDs: selected, IdempotencyKey: "stale-candidate-commit"}); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
				t.Fatalf("stale adoption committed: %v", err)
			}
			basket, err := d.GetPracticeSet(ctx, "lele", selection.TargetSetRecordID)
			if err != nil || len(basket.Fields.Items) != 0 {
				t.Fatalf("partial basket: %+v %v", basket, err)
			}
			generated, solved := 0, 0
			d.PracticeVariant = practiceAssetGenerator(&generated, nil)
			d.Solver = practiceAssetFallbackSolver{&solved}
			next, err := d.GeneratePracticeCandidateBatch(ctx, "lele", selection.SelectionID,
				usecase.PracticeCandidateBatchRequest{Revision: selection.Revision, IdempotencyKey: "after-stop"})
			if err != nil || generated != 3 || solved != 3 {
				t.Fatalf("existing next-batch recovery: %+v generate=%d solve=%d err=%v", next, generated, solved, err)
			}
		})
	}
}

func TestPracticeAssets_CustomPaperMixedSourcesAndHistoricalReplay(t *testing.T) {
	s, d, v, _ := practiceAssetFixture(t)
	ctx := context.Background()
	generated, solved := 0, 0
	d.PracticeVariant = practiceAssetGenerator(&generated, nil)
	d.Solver = practiceAssetFallbackSolver{&solved}
	req := practiceAssetPaperRequest("custom-mixed")
	result, err := d.GenerateCustomPaper(ctx, "lele", req)
	if err != nil || result.Added != 2 || generated != 1 || solved != 1 {
		t.Fatalf("custom mixed sources: %+v generate=%d solve=%d err=%v", result, generated, solved, err)
	}
	items := result.Set.Fields.Items
	if len(items) != 2 || items[0].AssetSource == nil || items[0].AssetSource.AssetID != v.AssetID || items[1].AssetSource != nil ||
		items[0].QuestionMarkdown == items[1].QuestionMarkdown {
		t.Fatalf("source or batch exclusion lost: %+v", items)
	}
	job, err := s.GetPracticeGenerationJobByID(ctx, "lele", result.GenerationJobID)
	if err != nil || job.GenerationOutput != "" || job.ValidationOutput != "" || job.Attempt != 0 {
		t.Fatalf("fabricated task output: %+v %v", job, err)
	}
	printed, _, err := d.FinalizeBasket(ctx, "lele", result.Set.Record.RecordID, "print")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range printed.Fields.Items {
		if item.PracticeProblemID == "" || item.ResultCorrect != nil || item.Returned {
			t.Fatalf("independent answer missing: %+v", item)
		}
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	replayed, err := d.GenerateCustomPaper(ctx, "lele", req)
	if err != nil || generated != 1 || solved != 1 || !reflect.DeepEqual(printed.Fields, replayed.Set.Fields) {
		t.Fatalf("historical replay changed or regenerated: %+v %v", replayed, err)
	}
}

func TestPracticeAssets_CustomPaperIneligibleUsesOriginalGeneration(t *testing.T) {
	for _, reason := range []string{"grade", "harder", "archive", "history", "missing-subject"} {
		t.Run(reason, func(t *testing.T) {
			s, d, v, source := practiceAssetFixture(t)
			ctx := context.Background()
			req := practiceAssetPaperRequest("custom-fallback")
			req.PerSource = 1
			switch reason {
			case "missing-subject":
				if _, err := s.DB().Exec(`UPDATE k12_mistakes SET subject='' WHERE record_id=?`, source); err != nil {
					t.Fatal(err)
				}
			case "grade":
				req.Grade = "一年级上"
			case "harder":
				req.Difficulty = "harder"
			case "archive":
				if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
					t.Fatal(err)
				}
			case "history":
				oldReq := req
				oldReq.IdempotencyKey = "prior-asset-paper"
				previous, err := d.GenerateCustomPaper(ctx, "lele", oldReq)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := d.FinalizeBasket(ctx, "lele", previous.Set.Record.RecordID, "print"); err != nil {
					t.Fatal(err)
				}
			}
			generated, solved := 0, 0
			d.PracticeVariant = practiceAssetGenerator(&generated, nil)
			d.Solver = practiceAssetFallbackSolver{&solved}
			result, err := d.GenerateCustomPaper(ctx, "lele", req)
			if err != nil || result.Added != 1 || generated != 1 || solved != 1 || result.Set.Fields.Items[0].AssetSource != nil {
				t.Fatalf("fallback: %+v generate=%d solve=%d err=%v", result, generated, solved, err)
			}
		})
	}
}

func TestPracticeAssets_CustomPaperStoppedDuringBatchRollsBack(t *testing.T) {
	s, d, v, _ := practiceAssetFixture(t)
	ctx := context.Background()
	generated, solved := 0, 0
	d.PracticeVariant = practiceAssetGenerator(&generated, func() {
		if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
			t.Fatal(err)
		}
	})
	d.Solver = practiceAssetFallbackSolver{&solved}
	req := practiceAssetPaperRequest("custom-stop-before-commit")
	if _, err := d.GenerateCustomPaper(ctx, "lele", req); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		t.Fatalf("archived asset entered basket: %v", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_sets WHERE agent_name='lele'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("half basket count=%d err=%v", count, err)
	}
	job, err := s.GetPracticeGenerationJob(ctx, "lele", req.IdempotencyKey)
	if err != nil || job.Status != k12.PracticeGenerationFailed || len(job.ResultItemIDs) != 0 {
		t.Fatalf("failed job lost: %+v %v", job, err)
	}
	d.PracticeVariant = practiceAssetGenerator(&generated, nil)
	result, err := d.GenerateCustomPaper(ctx, "lele", req)
	if err != nil || result.Added != 2 || generated != 3 || solved != 3 {
		t.Fatalf("normal retry did not fall back: %+v generate=%d solve=%d err=%v", result, generated, solved, err)
	}
	for _, item := range result.Set.Fields.Items {
		if item.AssetSource != nil {
			t.Fatalf("retry retained stopped asset: %+v", item)
		}
	}
}
