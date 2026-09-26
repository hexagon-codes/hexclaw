package k12storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type weeklyAssetCatalog struct{}

type weeklySavedResponseSolver struct {
	calls    *int
	question *string
}

func (s weeklySavedResponseSolver) Solve(_ context.Context, question string, _ string, _ string) (usecase.SolveResult, error) {
	*s.calls++
	*s.question = question
	return usecase.SolveResult{Solution: "## 答案\n4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec}}, nil
}

func TestWeeklyAssets_SavedInstructionPrefixResponseResumesWithoutRegeneration(t *testing.T) {
	for _, tc := range []struct {
		name, question string
		ready          bool
	}{
		{"full-width instruction", "计算：24 ÷ 6 =", true},
		{"ascii instruction", "计算:24 ÷ 6 =", true},
		{"word problem stays outside arithmetic", "计算：24个苹果分给6人，每人多少个？", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, _, _ := practiceAssetFixture(t)
			ctx := context.Background()
			plan := weeklyAssetDeps(t, &d, "lele", false)
			// 复现已成功生成、尚未验算的持久批次；响应正文保持原样。
			generated := usecase.SolveResult{Solution: "## 问题\n\n" + tc.question + "\n\n## 解答\n\n1. 想：6的4倍是24。\n2. 所以24 ÷ 6 = 4。\n\n## 答案\n\n**4**",
				Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictUnverifiable, EvidenceType: usecase.EvidenceNone}}
			request := usecase.WeeklyPracticeCandidateRequest{AgentName: "lele", PlanSection: k12.WeeklySectionArithmeticWarmup,
				MaxItems: 1, ArithmeticMinutes: 1, GradeTerm: "五年级下", Textbook: "人教版", ProfileRevision: 1,
				Targets: []usecase.WeeklyPracticeTarget{{SourceRef: "saved-division-target", Subject: "数学", GradeTerm: "五年级下",
					KnowledgePoint: "除法运算", Question: `\(18 \div 3 =\)`, SourceRevision: 1, EvidenceRefs: []string{"saved-division-target"}}}}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint map[string]any
			if err := json.Unmarshal(encoded, &checkpoint); err != nil {
				t.Fatal(err)
			}
			checkpoint["generation"] = map[string]any{"route": k12.GradingModelSnapshot{Provider: "test", Model: "weekly-model", Route: "test/weekly-model", Capability: "text"},
				"items": []any{map[string]any{"target": 0, "generate": map[string]any{"status": "succeeded", "result": generated}, "solve": map[string]any{"status": ""}}}}
			encoded, err = json.Marshal(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			batch, _, err := s.PrepareWeeklyArithmeticBatch(ctx, "lele", plan.PlanID, plan.Revision, "saved-response", strings.Repeat("a", 64), string(encoded), d.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.FinishWeeklyArithmeticGeneration(ctx, "lele", batch.BatchID, k12.WeeklyArithmeticFailedRetryable, nil, nil, "", "weekly generated problem invalid", d.Now()); err != nil {
				t.Fatal(err)
			}
			generateCalls, solveCalls, solvedQuestion := 0, 0, ""
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
				generateCalls++
				return usecase.SolveResult{}, errors.New("saved response must not regenerate")
			})
			d.Solver = weeklySavedResponseSolver{&solveCalls, &solvedQuestion}
			if _, replay, err := d.RetryWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "recover-prefix"); err != nil || replay {
				t.Fatalf("retry: replay=%v err=%v", replay, err)
			}
			stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
			if err != nil || generateCalls != 0 {
				t.Fatalf("saved generate result not reused: calls=%d err=%v", generateCalls, err)
			}
			if tc.ready {
				if stored.State != k12.WeeklyArithmeticReady || len(stored.Items) != 1 || solveCalls != 1 || solvedQuestion != tc.question {
					t.Fatalf("resume did not solve original question once: %+v calls=%d question=%q", stored, solveCalls, solvedQuestion)
				}
				item := stored.Items[0]
				if item.PromptMarkdown != tc.question || stored.AnswerKeys[item.ItemID] != "4" || item.AssetSource != nil {
					t.Fatalf("original question or source changed: %+v keys=%v", item, stored.AnswerKeys)
				}
			} else if stored.State != k12.WeeklyArithmeticFailedRetryable || len(stored.Items) != 0 || solveCalls != 0 {
				t.Fatalf("word problem accepted as arithmetic: %+v calls=%d", stored, solveCalls)
			}
			var receipt struct {
				Generation struct {
					Items []struct {
						Generate struct {
							Status string
							Result usecase.SolveResult
						}
					}
				}
			}
			if err := json.Unmarshal([]byte(stored.GenerationCheckpoint), &receipt); err != nil || len(receipt.Generation.Items) != 1 ||
				receipt.Generation.Items[0].Generate.Status != "succeeded" || !reflect.DeepEqual(receipt.Generation.Items[0].Generate.Result, generated) {
				t.Fatalf("original generated receipt changed: %+v err=%v", receipt, err)
			}
			priorCalls := solveCalls
			if _, replay, err := d.RetryWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "recover-prefix"); err != nil || !replay || generateCalls != 0 || solveCalls != priorCalls {
				t.Fatalf("replay repeated calls: replay=%v err=%v calls=%d/%d", replay, err, generateCalls, solveCalls)
			}
			replayed, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
			if err != nil || replayed.GenerationCheckpoint != stored.GenerationCheckpoint {
				t.Fatalf("replay changed receipts: %v", err)
			}
		})
	}
}

func (weeklyAssetCatalog) LookupWeeklyCurriculum(_ context.Context, req usecase.WeeklyCurriculumCatalogRequest) (k12.CurriculumCatalog, error) {
	return k12.CurriculumCatalog{Subject: req.Subject, TextbookBindingID: "weekly-binding", TextbookEdition: req.TextbookEdition,
		TextbookVersion: "2025", Title: "数学五年级下册", Volume: req.Volume, PageMin: 1, PageMax: 100,
		Units: []k12.CurriculumCatalogUnit{{UnitID: "u1", Title: "第一单元", PageFrom: 1, PageTo: 20}}}, nil
}

// 周练通过真实档案、计划与批次入口，仅模型调用边界受控。
func weeklyAssetDeps(t *testing.T, d *usecase.Deps, agent string, supplements bool) k12.WeeklyPracticePlan {
	t.Helper()
	d.Now = func() int64 { return 1785081600 }
	d.WeeklyCurriculum = weeklyAssetCatalog{}
	_, err := d.UpdateProfileBundle(context.Background(), usecase.UpdateProfileBundleRequest{
		OwnerID: "desktop-user", AgentName: agent, IdempotencyKey: "weekly-profile",
		Profile: k12.ChildProfile{ChildName: "测试学生", GradeTerm: "五年级下", SubjectTextbooks: k12.SubjectTextbooks{
			Math: "人教版", Chinese: "统编版", English: "外研版", Science: "教科版", InformationTechnology: "浙教版", Art: "人美版"}},
		CurriculumProgress:     usecase.CurriculumProgressInput{Subject: "math", TextbookBindingID: "weekly-binding", Volume: "下册", UnitID: "u1", EvidenceSource: "parent_confirmed"},
		WeeklyPracticeSettings: usecase.WeeklyPracticeSettingsInput{Timezone: "Asia/Shanghai", TextbookConsolidationEnabled: supplements, ArithmeticWarmupEnabled: true, ArithmeticMinutes: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.PracticeGenerationRoute = func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
		return k12.GradingModelSnapshot{Provider: "test", Model: "weekly-model", Route: "test/weekly-model", Capability: "text"}, nil
	}
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(d)
	plan, _, err := d.EnsureWeeklyPracticePlan(context.Background(), usecase.EnsureWeeklyPracticePlanRequest{AgentName: agent, IdempotencyKey: "weekly-plan"})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestWeeklyAssets_ProductionMixedSourcesReplayAndIndependentAttempts(t *testing.T) {
	s, d, v, _ := practiceAssetFixture(t)
	ctx := context.Background()
	plan := weeklyAssetDeps(t, &d, "lele", true)
	if plan.Tracks[1].Status != k12.WeeklyTrackFailed || !strings.Contains(plan.Tracks[1].FailureMessage, "target evidence unavailable") {
		t.Fatalf("unproven curriculum goal accepted: %+v", plan.Tracks[1])
	}
	generated, solved := 0, 0
	d.PracticeVariant = practiceAssetGenerator(&generated, nil)
	d.Solver = practiceAssetFallbackSolver{&solved}
	batch, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 2, "mixed")
	if err != nil || replay {
		t.Fatalf("create: %+v %v %v", batch, replay, err)
	}
	stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
	if err != nil || stored.State != k12.WeeklyArithmeticReady || len(stored.Items) != 2 || generated != 1 || solved != 1 {
		t.Fatalf("mixed production batch: %+v gen=%d solve=%d err=%v", stored, generated, solved, err)
	}
	if stored.Items[0].AssetSource == nil || stored.Items[0].AssetSource.AssetID != v.AssetID || stored.Items[1].AssetSource != nil {
		t.Fatalf("source attribution: %+v", stored.Items)
	}
	if !strings.Contains(stored.GenerationCheckpoint, `"status":"succeeded"`) || !strings.Contains(stored.GenerationCheckpoint, `"provider":"test"`) {
		t.Fatalf("physical receipt missing: %s", stored.GenerationCheckpoint)
	}
	if _, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 2, "mixed"); err != nil || !replay || generated != 1 || solved != 1 {
		t.Fatalf("replay repeated calls: %v %v %d %d", err, replay, generated, solved)
	}
	var attempts int
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_weekly_arithmetic_attempts WHERE agent_name='lele'`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("asset inherited student answer: %d %v", attempts, err)
	}
	// 新作答经原批次入口独立建立，来源标准答案不构成孩子的作答证据。
	d.WeeklyAssessment = weeklyAssetAssessor{}
	if _, _, err := d.StartWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "start"); err != nil {
		t.Fatal(err)
	}
	for _, item := range stored.Items {
		attempt, _, err := d.SubmitWeeklyArithmeticAttempt(ctx, "lele", batch.BatchID, item.ItemID, stored.AnswerKeys[item.ItemID], "answer:"+item.ItemID)
		if err != nil || attempt.ItemID != item.ItemID {
			t.Fatalf("independent answer: %+v %v", attempt, err)
		}
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_weekly_arithmetic_attempts WHERE agent_name='lele'`).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("new answer not persisted: %d %v", attempts, err)
	}
	second, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "next")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.GetWeeklyArithmeticBatch(ctx, "lele", second.BatchID)
	if err != nil || next.State != k12.WeeklyArithmeticReady || next.Items[0].AssetSource != nil || next.Items[0].PromptMarkdown == stored.Items[0].PromptMarkdown || generated != 2 {
		t.Fatalf("history repeated: %+v %v calls=%d", next, err, generated)
	}
}

func TestWeeklyAssets_ZeroCallsAndArchivedBeforeCommit(t *testing.T) {
	t.Run("asset only has no execution receipts", func(t *testing.T) {
		s, d, _, _ := practiceAssetFixture(t)
		plan := weeklyAssetDeps(t, &d, "lele", false)
		batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(context.Background(), "lele", plan.PlanID, plan.Revision, 1, "asset")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := s.GetWeeklyArithmeticBatch(context.Background(), "lele", batch.BatchID)
		if err != nil || stored.State != k12.WeeklyArithmeticReady || stored.Items[0].AssetSource == nil || strings.Contains(stored.GenerationCheckpoint, `"status":"sent"`) || strings.Contains(stored.GenerationCheckpoint, `"status":"succeeded"`) {
			t.Fatalf("fabricated invocation: %+v %v", stored, err)
		}
	})
	t.Run("stop during fallback prevents partial commit", func(t *testing.T) {
		s, d, v, _ := practiceAssetFixture(t)
		ctx := context.Background()
		plan := weeklyAssetDeps(t, &d, "lele", false)
		generated, solved := 0, 0
		d.PracticeVariant = practiceAssetGenerator(&generated, func() {
			if generated == 1 {
				if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
					t.Fatal(err)
				}
			}
		})
		d.Solver = practiceAssetFallbackSolver{&solved}
		batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 2, "stop")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
		if err != nil || stored.State != k12.WeeklyArithmeticFailedRetryable || len(stored.Items) != 0 {
			t.Fatalf("inactive asset committed: %+v %v", stored, err)
		}
		if _, _, err := d.RetryWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "retry"); err != nil {
			t.Fatal(err)
		}
		stored, err = s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
		if err != nil || stored.State != k12.WeeklyArithmeticReady || len(stored.Items) != 2 || stored.Items[0].AssetSource != nil || generated != 2 || solved != 2 {
			t.Fatalf("retry did not reuse successful second response: %+v %v calls=%d/%d", stored, err, generated, solved)
		}
	})
}

func TestWeeklyAssets_UnknownReceiptAndInterruptedCommitDoNotResend(t *testing.T) {
	t.Run("unknown is terminal", func(t *testing.T) {
		s, d, v, _ := practiceAssetFixture(t)
		ctx := context.Background()
		plan := weeklyAssetDeps(t, &d, "lele", false)
		if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
			t.Fatal(err)
		}
		calls, solved := 0, 0
		d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
			calls++
			return usecase.SolveResult{}, context.DeadlineExceeded
		})
		d.Solver = practiceAssetFallbackSolver{&solved}
		batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "unknown")
		if err != nil {
			t.Fatal(err)
		}
		stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
		if err != nil || stored.State != k12.WeeklyArithmeticFailedTerminal || !strings.Contains(stored.GenerationCheckpoint, "outcome_unknown") {
			t.Fatalf("unknown not durable: %+v %v", stored, err)
		}
		if _, _, err := d.RetryWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "retry"); !errors.Is(err, records.ErrIllegalTransition) {
			t.Fatalf("unknown retried: %v", err)
		}
		if _, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "unknown"); err != nil || calls != 1 || solved != 0 {
			t.Fatalf("replay sent again: %v %d/%d", err, calls, solved)
		}
	})
	t.Run("saved responses survive final write failure", func(t *testing.T) {
		s, d, v, _ := practiceAssetFixture(t)
		ctx := context.Background()
		plan := weeklyAssetDeps(t, &d, "lele", false)
		if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
			t.Fatal(err)
		}
		calls, solved := 0, 0
		d.PracticeVariant = practiceAssetGenerator(&calls, nil)
		d.Solver = practiceAssetFallbackSolver{&solved}
		if _, err := s.DB().Exec(`CREATE TRIGGER weekly_reject_ready BEFORE UPDATE OF state ON k12_weekly_arithmetic_batches WHEN NEW.state='ready' BEGIN SELECT RAISE(ABORT,'injected final write failure'); END`); err != nil {
			t.Fatal(err)
		}
		batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "crash")
		if err == nil {
			t.Fatal("final persistence failure hidden")
		}
		if _, err := s.DB().Exec(`DROP TRIGGER weekly_reject_ready`); err != nil {
			t.Fatal(err)
		}
		if _, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "crash"); err != nil || !replay || calls != 1 || solved != 1 {
			t.Fatalf("resume repeated invocation: %v %v %d/%d", err, replay, calls, solved)
		}
		stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
		if err != nil || stored.State != k12.WeeklyArithmeticReady {
			t.Fatalf("resume did not finish: %+v %v", stored, err)
		}
	})
}

func TestWeeklyAssets_TargetGradeAndTextbookEvidenceAreRequired(t *testing.T) {
	s, d, _, _ := practiceAssetFixture(t)
	ctx := context.Background()
	plan := weeklyAssetDeps(t, &d, "lele", false)
	if _, err := s.DB().Exec(`UPDATE k12_mistakes SET grade_term='四年级下' WHERE agent_name='lele'`); err != nil {
		t.Fatal(err)
	}
	calls, solved := 0, 0
	d.PracticeVariant = practiceAssetGenerator(&calls, nil)
	d.Solver = practiceAssetFallbackSolver{&solved}
	batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "wrong-grade")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
	if err != nil || stored.State != k12.WeeklyArithmeticFailedRetryable || !strings.Contains(stored.FailureMessage, "target evidence unavailable") || calls != 0 || solved != 0 {
		t.Fatalf("wrong grade became target: %+v %v", stored, err)
	}
	if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "lele", plan.PlanID, plan.Revision, 1, "without-grounding"); err == nil || !strings.Contains(err.Error(), "target evidence unavailable") {
		t.Fatalf("title guessed target: %v", err)
	}
	var pending int
	if err := s.DB().QueryRow(`SELECT count(*) FROM k12_weekly_track_refresh_commands WHERE agent_name='lele'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unstarted calls manufactured: %d %v", pending, err)
	}
}

func TestWeeklyAssets_CommandCheckpointCASAndPlanReplay(t *testing.T) {
	s, d, _, _ := practiceAssetFixture(t)
	ctx := context.Background()
	plan := weeklyAssetDeps(t, &d, "lele", false)
	request := usecase.WeeklyPracticeCandidateRequest{AgentName: "lele", PlanSection: k12.WeeklySectionTextbookConsolidation, MaxItems: 1}
	raw, _ := json.Marshal(request)
	ref := k12storage.WeeklyCandidateCheckpointRef{AgentName: "lele", PlanID: plan.PlanID, Revision: plan.Revision, Kind: "refresh", IdempotencyKey: "durable"}
	initial, err := s.PrepareWeeklyCandidateCommand(ctx, ref, strings.Repeat("a", 64), plan, string(raw), d.Now())
	if err != nil {
		t.Fatal(err)
	}
	next := `{"AgentName":"lele","generation":{"items":[{"generate":{"status":"sent"}}]}}`
	if err := s.SaveWeeklyCandidateCheckpoint(ctx, ref, initial, next); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWeeklyCandidateCheckpoint(ctx, ref, initial, `{}`); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("stale writer replaced receipt: %v", err)
	}
	other := ref
	other.IdempotencyKey = "another-key"
	if _, err := s.PrepareWeeklyCandidateCommand(ctx, other, strings.Repeat("a", 64), plan, string(raw), d.Now()); !errors.Is(err, records.ErrIllegalTransition) {
		t.Fatalf("new key resent unresolved request: %v", err)
	}
	got, err := s.GetWeeklyCandidateCheckpoint(ctx, ref)
	if err != nil || got != next {
		t.Fatalf("checkpoint changed: %s %v", got, err)
	}
	if _, _, _, err := s.CommitWeeklyTextbookRefresh(ctx, "lele", plan.PlanID, plan.Revision, "durable", strings.Repeat("a", 64), plan, false, 0, next, d.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWeeklyCandidateCheckpoint(ctx, ref, next, `{}`); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("completed command changed: %v", err)
	}
	if _, replay, _, err := s.CommitWeeklyTextbookRefresh(ctx, "lele", plan.PlanID, plan.Revision, "durable", strings.Repeat("a", 64), plan, false, 0, "", d.Now()); err != nil || !replay {
		t.Fatalf("legacy typed response changed: %v %v", replay, err)
	}
}

type weeklyAssetAssessor struct{}

func (weeklyAssetAssessor) AssessWeeklyPracticeAnswer(_ context.Context, req usecase.WeeklyPracticeAnswerRequest) (usecase.WeeklyPracticeAnswerAssessment, error) {
	return usecase.WeeklyPracticeAnswerAssessment{AssessmentID: "weekly:" + req.Item.ItemID, Result: "correct", VerificationEvidence: string(usecase.EvidenceNumericExec), Subject: "数学", KnowledgePoint: "整数加法"}, nil
}

// 固定回执模拟已保存的教材命中，页范围和身份来自真实消费回执字段。
func weeklyGroundedResult(t *testing.T) string {
	t.Helper()
	digest := strings.Repeat("a", 64)
	snapshot := usecase.GroundingSnapshot{AgentName: "mingming", LearnerID: "mingming", Subject: "数学", TextbookBindingID: "weekly-binding", TextbookManifestID: "weekly-manifest",
		DocumentID: "weekly-book", DocumentGeneration: 1, SourceDigest: digest, Edition: "人教版", Volume: "下册", SegmentRefs: []string{"book-chunk-7"},
		PageRefs: []k12.TextbookGroundingPageRef{{LogicalPage: 7, PDFPage: 9, SegmentRefs: []string{"book-chunk-7"}}}, VectorRevisionID: "revision-1"}
	receipts := []usecase.GroundingEvidenceReceipt{{TextbookBindingID: snapshot.TextbookBindingID, TextbookManifestID: snapshot.TextbookManifestID,
		DocumentID: snapshot.DocumentID, DocumentGeneration: 1, VectorRevisionID: snapshot.VectorRevisionID, QueryDigest: "sha256:" + digest, ChunkID: "book-chunk-7", LogicalPage: 7, PDFPage: 9, SourceDigest: digest, CitationDigest: digest}}
	raw, _ := json.Marshal(struct {
		Snapshot usecase.GroundingSnapshot          `json:"snapshot"`
		Receipts []usecase.GroundingEvidenceReceipt `json:"receipts"`
	}{snapshot, receipts})
	hash := sha256.New()
	hash.Write([]byte("\x00k12-grading-grounding-v1\x00"))
	hash.Write(raw)
	envelope, _ := json.Marshal(map[string]any{"schema": "k12_grading_grounded_physical_v1", "payload": map[string]string{"Solution": "## 答案\n4"},
		"grounding": map[string]any{"snapshot": snapshot, "receipts": receipts, "identity_digest": "sha256:" + hex.EncodeToString(hash.Sum(nil))}})
	return string(envelope)
}

func TestWeeklyAssets_TextbookUsesActualScopeAndEffectiveCorrection(t *testing.T) {
	s, d, _, _ := practiceAssetFixture(t)
	ctx := context.Background()
	plan := weeklyAssetDeps(t, &d, "mingming", false)
	var job, problem, solve string
	if err := s.DB().QueryRow(`SELECT job_id,problem_id,solve_invocation_id FROM k12_grading_assessment_items WHERE agent_name='mingming'`).Scan(&job, &problem, &solve); err != nil {
		t.Fatal(err)
	}
	original, err := s.GetGradingAssessmentItem(ctx, "mingming", job, problem)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.GetGradingItemInvocation(ctx, "mingming", solve)
	if err != nil {
		t.Fatal(err)
	}
	corrected := original
	corrected.GradeInvocationID = correctionProof(t, s, job, k12.Attempt{ProblemID: problem, AttemptID: inv.AttemptID, ConfirmedVersion: inv.InputRevision, InputDigest: inv.InputDigest}, k12.GradingItemOperationGrade, 2, true)
	corrected.ResultJSON = `{"Recognized":{"Question":"2+2=?","Subject":"数学","AnswerState":"present","KnowledgePoints":["两位数加法"]},"Status":"correct"}`
	corrected.ResultDigest = "sha256:weekly-effective"
	if _, _, err := s.AppendGradingAssessmentCorrection(ctx, k12.GradingAssessmentCorrection{CorrectionID: "weekly-correction", OriginalResultDigest: original.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: corrected}, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	sources, err := s.ListWeeklyCandidateTargetSources(ctx, "desktop-user", "mingming", "五年级下")
	if err != nil || len(sources) != 1 || sources[0].KnowledgePoint != "两位数加法" {
		t.Fatalf("old teaching target survived correction: %+v %v", sources, err)
	}
	if _, err := s.DB().Exec(`UPDATE k12_grading_item_invocations SET result_json=? WHERE agent_name='mingming' AND item_invocation_id=?`, weeklyGroundedResult(t), solve); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE k12_curriculum_progress SET textbook_manifest_id='weekly-manifest',verified_page_from=7,verified_page_to=7,page_verification_status='verified' WHERE agent_name='mingming'`); err != nil {
		t.Fatal(err)
	}
	generated, solved := 0, 0
	d.PracticeVariant = practiceAssetGenerator(&generated, nil)
	d.Solver = practiceAssetFallbackSolver{&solved}
	prepared, replay, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 1, "grounded")
	if err != nil || replay || prepared.Tracks[1].Status != k12.WeeklyTrackReady || generated != 1 || solved != 1 {
		t.Fatalf("grounded textbook: %+v %v %v calls=%d/%d", prepared, replay, err, generated, solved)
	}
	item := prepared.Tracks[1].Items[0]
	if item.KnowledgePoint != "两位数加法" || item.SourceKind != "assessment_variant" || item.Verification.VerifiedPageFrom == nil || *item.Verification.VerifiedPageFrom != 7 {
		t.Fatalf("wrong target source: %+v", item)
	}
	if _, replay, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 1, "grounded"); err != nil || !replay || generated != 1 || solved != 1 {
		t.Fatalf("textbook replay called provider: %v %v", replay, err)
	}
	var response string
	if err := s.DB().QueryRow(`SELECT response_json FROM k12_weekly_track_refresh_commands WHERE agent_name='mingming' AND idempotency_key='grounded'`).Scan(&response); err != nil || !strings.Contains(response, `"status":"succeeded"`) || !strings.Contains(response, `"_weekly_pending":false`) {
		t.Fatalf("textbook receipts absent: %s %v", response, err)
	}
	if _, err := s.DB().Exec(`UPDATE k12_weekly_practice_settings SET textbook_consolidation_enabled=1,revision=revision+1 WHERE agent_name='mingming'`); err != nil {
		t.Fatal(err)
	}
	automatic, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "automatic-grounded"})
	if err != nil || automatic.Tracks[1].Status != k12.WeeklyTrackReady || len(automatic.Tracks[1].Items) == 0 {
		t.Fatalf("automatic production plan failed: %+v %v", automatic, err)
	}
	before := generated
	if _, replay, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "automatic-grounded"}); err != nil || !replay || generated != before {
		t.Fatalf("automatic replay called provider: %v %v %d/%d", replay, err, generated, before)
	}
	prepared = automatic
	if _, err := s.DB().Exec(`UPDATE k12_curriculum_progress SET verified_page_from=12,verified_page_to=12 WHERE agent_name='mingming'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, prepared.Revision, 1, "wrong-page"); err == nil || !strings.Contains(err.Error(), "target evidence unavailable") || generated != before {
		t.Fatalf("unrelated page accepted: %v calls=%d", err, generated)
	}
}

func TestWeeklyAssets_FrozenPrintIsIndependentOfAssetStop(t *testing.T) {
	s, d, v, _ := practiceAssetFixture(t)
	ctx := context.Background()
	plan := weeklyAssetDeps(t, &d, "lele", false)
	batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "print")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := k12.WeeklyPracticeSnapshot{SnapshotID: "weekly-print", PlanID: plan.PlanID, PlanRevision: plan.Revision, AgentName: "lele", SnapshotDigest: strings.Repeat("b", 64), CreatedAt: d.Now(), Tracks: []k12.WeeklyPracticeTrack{{PlanSection: k12.WeeklySectionArithmeticWarmup, Items: current.Items}}, AnswerKeys: current.AnswerKeys}
	frozen, _, err := s.FreezeWeeklyPracticeSnapshot(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	replay, found, err := s.FreezeWeeklyPracticeSnapshot(ctx, snapshot)
	if err != nil || !found || !reflect.DeepEqual(replay.Tracks, frozen.Tracks) || replay.AnswerKeys[current.Items[0].ItemID] != "4" {
		t.Fatalf("printed content changed: %+v %v", replay, err)
	}
}

func TestWeeklyAssets_AdoptedAssessmentStillProvidesIndependentTarget(t *testing.T) {
	s, d, v, _ := practiceAssetFixture(t)
	ctx := context.Background()
	weeklyAssetDeps(t, &d, "mingming", false)
	job, attempt := seedItemLedgerFacts(t, s, "weekly-adopted-target")
	adoption, _, err := s.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{OwnerID: v.OwnerID, JobID: job.RecordID, ProblemID: attempt.ProblemID,
		InputRevision: attempt.ConfirmedVersion, InputDigest: attempt.InputDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, FactsDigest: v.FactsDigest})
	if err != nil {
		t.Fatal(err)
	}
	grade := correctionProof(t, s, job.RecordID, attempt, k12.GradingItemOperationGrade, 1, true)
	assessment := assessmentReceipt(job.RecordID, attempt, "", grade)
	assessment.Status = k12.GradingAssessmentCorrect
	assessment.AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, FactsDigest: v.FactsDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, AdoptionID: adoption.AdoptionID}
	assessment.ResultJSON = `{"Recognized":{"Question":"\\(2+2=\\)","Subject":"数学","AnswerState":"present","KnowledgePoints":["加法口算"]},"Status":"correct"}`
	if _, _, err := s.CommitGradingAssessmentItem(ctx, assessment, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	freezer := d.WeeklyCandidates.(interface {
		FreezeWeeklyPracticeCandidateRequest(context.Context, usecase.WeeklyPracticeCandidateRequest) (usecase.WeeklyPracticeCandidateRequest, error)
	})
	request, err := freezer.FreezeWeeklyPracticeCandidateRequest(ctx, usecase.WeeklyPracticeCandidateRequest{AgentName: "mingming", PlanSection: k12.WeeklySectionArithmeticWarmup, MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, target := range request.Targets {
		if target.SourceRef == "assessment:"+job.RecordID+":"+attempt.ProblemID {
			found = target.KnowledgePoint == "加法口算" && target.GradeTerm == "五年级下"
		}
	}
	if !found {
		t.Fatalf("adopted independent assessment lost target: %+v", request.Targets)
	}
	if sources, err := s.ListWeeklyCandidateTargetSources(ctx, "other-owner", "mingming", "五年级下"); err != nil || len(sources) != 0 {
		t.Fatalf("owner boundary crossed: %+v %v", sources, err)
	}
}

func TestWeeklyAssets_ArithmeticWithoutCurriculumUsesActualTargets(t *testing.T) {
	for _, hasTarget := range []bool{true, false} {
		name := "known target remains available"
		if !hasTarget {
			name = "missing target remains unavailable"
		}
		t.Run(name, func(t *testing.T) {
			s, d, _, _ := practiceAssetFixture(t)
			ctx := context.Background()
			plan := weeklyAssetDeps(t, &d, "lele", false)
			if _, err := s.DB().Exec(`DELETE FROM k12_curriculum_progress WHERE agent_name='lele'`); err != nil {
				t.Fatal(err)
			}
			if !hasTarget {
				if _, err := s.DB().Exec(`UPDATE k12_mistakes SET grade_term='四年级下' WHERE agent_name='lele'`); err != nil {
					t.Fatal(err)
				}
			}
			generated, solved := 0, 0
			d.PracticeVariant = practiceAssetGenerator(&generated, nil)
			d.Solver = practiceAssetFallbackSolver{&solved}
			projected, err := d.GetCurrentWeeklyPracticePlan(ctx, "lele")
			if err != nil || projected == nil {
				t.Fatalf("project without curriculum: %+v %v", projected, err)
			}
			want := k12.WeeklyManualTrackSetupRequired
			if hasTarget {
				want = k12.WeeklyManualTrackAvailable
			}
			if projected.ManualTrackRecommendations.ArithmeticWarmup.Availability != want ||
				projected.ManualTrackRecommendations.TextbookConsolidation.Availability != k12.WeeklyManualTrackSetupRequired {
				t.Fatalf("track prerequisites mixed: %+v", projected.ManualTrackRecommendations)
			}
			batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "no-curriculum")
			if err != nil {
				t.Fatal(err)
			}
			stored, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
			if err != nil {
				t.Fatal(err)
			}
			if hasTarget {
				if stored.State != k12.WeeklyArithmeticReady || len(stored.Items) != 1 || stored.Items[0].AssetSource == nil {
					t.Fatalf("known target was blocked: %+v", stored)
				}
				if _, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "no-curriculum"); err != nil || !replay {
					t.Fatalf("no-curriculum replay: %v %v", replay, err)
				}
			} else if stored.State != k12.WeeklyArithmeticFailedRetryable || len(stored.Items) != 0 ||
				!strings.Contains(stored.FailureMessage, "target evidence unavailable") {
				t.Fatalf("missing target fabricated success: %+v", stored)
			}
			if generated != 0 || solved != 0 {
				t.Fatalf("unexpected external calls: %d/%d", generated, solved)
			}
			progress, err := d.GetCurriculumProgress(ctx, "lele", "math")
			if err != nil || progress != nil {
				t.Fatalf("request manufactured curriculum: %+v %v", progress, err)
			}
		})
	}
}
