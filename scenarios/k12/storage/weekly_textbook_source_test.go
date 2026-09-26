package k12storage_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const weeklyLessonPage = "# 第一单元\n\n## 分数乘整数\n\n一朵花用3/8张纸，2朵用多少张？3/8×2=3/4。分数乘整数，用分子乘整数，分母不变。\n\n## 做一做\n\n一袋面包重3/10千克，3袋重多少千克？\n\n1"

func weeklyTextbookSourceFixture(t *testing.T, page string) (usecase.Deps, k12.WeeklyPracticePlan) {
	t.Helper()
	s, _, _ := seedTextbookCatalogMaterialization(t, page)
	// 通过实际目录 worker 发布来源，保留完整正文偏移及原始页回执。
	if _, err := s.DB().Exec(`DELETE FROM k12_textbook_catalog_jobs; UPDATE kb_documents SET content=(SELECT group_concat(content,'') FROM (SELECT content FROM kb_ingest_page_checkpoints ORDER BY page_number))`); err != nil {
		t.Fatal(err)
	}
	worker := usecase.NewTextbookCatalogWorker(s, usecase.TextbookCatalogCheckpointExtractor{}, usecase.TextbookCatalogWorkerConfig{
		WorkerID: "weekly-source", Lease: time.Minute, HeartbeatInterval: time.Second,
		ExtractTimeout: time.Second, MaxAttempts: 1, RetryBase: time.Second, RetryMax: time.Second, RecoveryBatch: 1,
	})
	if processed, err := worker.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("catalog publication: processed=%v err=%v", processed, err)
	}
	d := usecase.Deps{Records: s, TextbookOwnerID: "desktop-user", Now: func() int64 { return 1785081600 }}
	catalog, err := s.GetTextbookManifestCatalog(context.Background(), k12storage.TextbookScope{
		OwnerID: "desktop-user", AgentName: "mingming", Subject: "math",
	}, "catalog-manifest")
	if err != nil || len(catalog.Units) == 0 {
		t.Fatalf("catalog unavailable: %+v %v", catalog, err)
	}
	pageNumber := 1
	_, err = d.UpdateProfileBundle(context.Background(), usecase.UpdateProfileBundleRequest{
		OwnerID: "desktop-user", AgentName: "mingming", IdempotencyKey: "source-profile",
		Profile: k12.ChildProfile{ChildName: "测试学生", GradeTerm: "五年级下", SubjectTextbooks: k12.SubjectTextbooks{
			Math: "人教版", Chinese: "统编版", English: "外研版", Science: "教科版", InformationTechnology: "浙教版", Art: "人美版"}},
		CurriculumProgress: usecase.CurriculumProgressInput{Subject: "math", TextbookManifestID: "catalog-manifest", Volume: "下册",
			UnitID: catalog.Units[0].UnitID, PageFrom: &pageNumber, PageTo: &pageNumber, EvidenceSource: "parent_confirmed"},
		WeeklyPracticeSettings: usecase.WeeklyPracticeSettingsInput{Timezone: "Asia/Shanghai", ArithmeticMinutes: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	d.PracticeGenerationRoute = func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
		return k12.GradingModelSnapshot{Provider: "test", Model: "weekly-model", Route: "test/weekly-model", Capability: "text"}, nil
	}
	plan, _, err := d.EnsureWeeklyPracticePlan(context.Background(), usecase.EnsureWeeklyPracticePlanRequest{AgentName: "mingming", IdempotencyKey: "source-plan"})
	if err != nil {
		t.Fatal(err)
	}
	return d, plan
}

func TestWeeklyTextbookSource_NoPriorAnswersAndReplay(t *testing.T) {
	d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
	// 返回的 Deps 地址固定后接入生产供题器，模型仅在受控边界替换。
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	generated, solved := 0, 0
	d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(_ context.Context, _, prompt, _ string) (usecase.SolveResult, error) {
		generated++
		if !strings.Contains(prompt, "分数乘整数") || !strings.Contains(prompt, "3/8×2=3/4") || strings.Contains(prompt, "面包") {
			t.Fatalf("lesson source mixed or absent: %s", prompt)
		}
		return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n分子乘20，再约分。\n## 答案\n4"}, nil
	})
	var question string
	d.Solver = weeklySavedResponseSolver{&solved, &question}
	result, replay, err := d.PrepareWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, plan.Revision, 1, "page-source")
	if err != nil || replay || generated != 1 || solved != 1 {
		t.Fatalf("direct textbook prepare: err=%v replay=%v generate/solve=%d/%d", err, replay, generated, solved)
	}
	track := result.Tracks[1]
	if track.Status != k12.WeeklyTrackReady || len(track.Items) != 1 || track.Items[0].KnowledgePoint != "分数乘整数" || track.Items[0].SourceKind != "textbook_variant" || !strings.HasPrefix(track.Items[0].SourceRef, "textbook:") {
		t.Fatalf("wrong textbook projection: %+v", track)
	}
	var prior string
	if err := d.Records.DB().QueryRow(`SELECT response_json FROM k12_weekly_track_refresh_commands WHERE idempotency_key='page-source'`).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := d.PrepareWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, plan.Revision, 1, "page-source"); err != nil || !replay || generated != 1 || solved != 1 {
		t.Fatalf("replay repeated work: %v %v %d/%d", err, replay, generated, solved)
	}
	var after string
	if err := d.Records.DB().QueryRow(`SELECT response_json FROM k12_weekly_track_refresh_commands WHERE idempotency_key='page-source'`).Scan(&after); err != nil || prior != after {
		t.Fatalf("receipt changed: %v", err)
	}
	var records int
	if err := d.Records.DB().QueryRow(`SELECT (SELECT count(*) FROM k12_mistakes)+(SELECT count(*) FROM k12_grading_assessment_items)`).Scan(&records); err != nil || records != 0 {
		t.Fatalf("fabricated assessment: %d %v", records, err)
	}
	var frozen map[string]json.RawMessage
	if err := json.Unmarshal([]byte(after), &frozen); err != nil || !strings.Contains(string(frozen["_weekly_generation"]), "logical_page:1") {
		t.Fatalf("page evidence not frozen: %v", err)
	}
}

func TestWeeklyTextbookSource_RejectsStaleOrUnrelatedPage(t *testing.T) {
	for _, mutation := range []string{"generation", "page", "content"} {
		t.Run(mutation, func(t *testing.T) {
			d, _ := weeklyTextbookSourceFixture(t, weeklyLessonPage)
			progress, err := d.Records.GetCurriculumProgress(context.Background(), "mingming", "math")
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "generation":
				_, err = d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`)
			case "page":
				other := 2
				progress.VerifiedPageFrom, progress.VerifiedPageTo = &other, &other
			case "content":
				_, err = d.Records.DB().Exec(`UPDATE kb_ingest_page_checkpoints SET content='changed' WHERE page_number=3`)
			}
			if err != nil {
				t.Fatal(err)
			}
			pages, err := d.Records.ListWeeklyTextbookPages(context.Background(), k12storage.TextbookScope{OwnerID: "desktop-user", AgentName: "mingming", Subject: "math"}, progress)
			if err == nil && len(pages) != 0 {
				t.Fatalf("invalid source accepted: %+v", pages)
			}
		})
	}
}

func TestWeeklyTextbookSource_MissingLessonDoesNotGuess(t *testing.T) {
	d, plan := weeklyTextbookSourceFixture(t, "# 第一单元\n\n## 做一做\n\n3/8×2是多少？\n\n1")
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
	_, _, err := d.PrepareWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, plan.Revision, 1, "no-lesson")
	if err == nil || !strings.Contains(err.Error(), "target evidence unavailable") {
		t.Fatalf("generic exercise guessed as lesson: %v", err)
	}
}
