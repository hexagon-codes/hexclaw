package usecase_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func migrateSharedPracticeSources(t *testing.T, d usecase.Deps) {
	t.Helper()
	if err := migrate.Run(context.Background(), d.Records.DB(), []migrate.Migration{
		migrate.K12PracticeGenerationSourcesV86,
	}); err != nil {
		t.Fatal(err)
	}
}

func createAccumulationForPractice(
	t *testing.T, d usecase.Deps, content, commandKey string,
) string {
	t.Helper()
	d.AccumulationMetadata = &fakeAccumulationMetadataDeriver{
		output: validAccumulationMetadata(),
	}
	id, created, err := d.CreateCurrentAccumulation(
		context.Background(), "xiaoming", content, commandKey,
	)
	if err != nil || !created {
		t.Fatalf("create accumulation: id=%q created=%v err=%v", id, created, err)
	}
	return id
}

func TestAccumulationPracticeGeneration_StartIsJobOnlyAndSourceUnique(t *testing.T) {
	d := newDataDeps(t)
	ctx := context.Background()
	migrateSharedPracticeSources(t, d)
	d.AccumulationMetadata = &fakeAccumulationMetadataDeriver{
		output: validAccumulationMetadata(),
	}
	accumulationID, created, err := d.CreateCurrentAccumulation(
		ctx, "xiaoming", "a piece of cake", "create-accumulation-shared-job",
	)
	if err != nil || !created {
		t.Fatalf("create accumulation: id=%q created=%v err=%v",
			accumulationID, created, err)
	}
	first, basketID, added, err := d.GenerateCurrentDictationToBasket(
		ctx, "xiaoming", "session-1", accumulationID, false,
		"dictation:"+accumulationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != k12.DictationQueued || first.GenerationID == "" ||
		first.PracticeItemID != "" || basketID != "" || added {
		t.Fatalf("start must return job-only pending: generation=%+v basket=%q added=%v",
			first, basketID, added)
	}
	replayed, _, _, err := d.GenerateCurrentDictationToBasket(
		ctx, "xiaoming", "session-2", accumulationID, false,
		"dictation:another-entry",
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GenerationID != first.GenerationID {
		t.Fatalf("same source identity created another job: first=%s replay=%s",
			first.GenerationID, replayed.GenerationID)
	}
	var jobs, sets, items, legacy int
	if err := d.Records.DB().QueryRow(`SELECT COUNT(*)
		FROM k12_practice_generation_jobs
		WHERE source_kind='accumulation' AND source_id=?`, accumulationID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_sets`).Scan(&sets)
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_set_items`).Scan(&items)
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*)
		FROM k12_accumulation_dictation_generations`).Scan(&legacy)
	if jobs != 1 || sets != 0 || items != 0 || legacy != 0 {
		t.Fatalf("job-only source counts jobs/sets/items/legacy=%d/%d/%d/%d",
			jobs, sets, items, legacy)
	}
}

func TestAccumulationPracticeGeneration_CommitsReadyItemAndReAddsSameJob(t *testing.T) {
	d := newDataDeps(t)
	migrateSharedPracticeSources(t, d)
	ctx := context.Background()
	accumulationID := createAccumulationForPractice(
		t, d, "a piece of cake", "create-accumulation-commit",
	)
	pending, _, _, err := d.GenerateCurrentDictationToBasket(
		ctx, "xiaoming", "session-list", accumulationID, false,
		"dictation:"+accumulationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	committed, basketID, added, err := d.ProcessAccumulationPracticeGeneration(
		ctx, "xiaoming", pending.GenerationID,
	)
	if err != nil || !added || committed.Status != k12.DictationCommitted ||
		committed.GenerationID != pending.GenerationID || committed.PracticeItemID == "" {
		t.Fatalf("commit generation=%+v basket=%q added=%v err=%v",
			committed, basketID, added, err)
	}
	basket, err := d.GetPracticeSet(ctx, "xiaoming", basketID)
	if err != nil {
		t.Fatal(err)
	}
	if len(basket.Fields.Items) != 1 ||
		basket.Fields.Items[0].ItemID != committed.PracticeItemID ||
		basket.Fields.Items[0].GenerationJobID != pending.GenerationID ||
		basket.Fields.Items[0].NormalizedContentHash == "" ||
		!k12.PracticeItemPublishable(basket.Fields.Items[0]) {
		t.Fatalf("formal accumulation item=%+v", basket.Fields.Items)
	}
	firstItemID := committed.PracticeItemID
	if err := d.RemoveFromBasket(ctx, "xiaoming", basketID, firstItemID); err != nil {
		t.Fatal(err)
	}
	projection, err := d.GetAccumulation(ctx, "xiaoming", accumulationID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.DictationGeneration == nil ||
		projection.DictationGeneration.Status != k12.DictationReAdd {
		t.Fatalf("removed projection=%+v", projection.DictationGeneration)
	}
	reactivated, _, _, err := d.GenerateCurrentDictationToBasket(
		ctx, "xiaoming", "session-detail", accumulationID, false,
		"dictation:detail-entry",
	)
	if err != nil || reactivated.GenerationID != pending.GenerationID {
		t.Fatalf("reactivate=%+v err=%v", reactivated, err)
	}
	readded, readdBasketID, readdAdded, err := d.ProcessAccumulationPracticeGeneration(
		ctx, "xiaoming", reactivated.GenerationID,
	)
	if err != nil || !readdAdded || readdBasketID != basketID ||
		readded.GenerationID != pending.GenerationID ||
		readded.PracticeItemID != firstItemID {
		t.Fatalf("re-add=%+v basket=%q added=%v err=%v",
			readded, readdBasketID, readdAdded, err)
	}
	var invocations, legacy, itemCount int
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_model_invocations
		WHERE job_id=?`, pending.GenerationID).Scan(&invocations)
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*)
		FROM k12_accumulation_dictation_generations`).Scan(&legacy)
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_set_items
		WHERE generation_job_id=?`, pending.GenerationID).Scan(&itemCount)
	if invocations != 0 || legacy != 0 || itemCount != 1 {
		t.Fatalf("re-add counts invocations/legacy/items=%d/%d/%d",
			invocations, legacy, itemCount)
	}
}

func TestAccumulationPracticeGeneration_FailureKeepsOnlySameJob(t *testing.T) {
	d := newDataDeps(t)
	migrateSharedPracticeSources(t, d)
	ctx := context.Background()
	accumulationID := createAccumulationForPractice(
		t, d, strings.Repeat("长", 101), "create-accumulation-too-long",
	)
	pending, _, _, err := d.GenerateCurrentDictationToBasket(
		ctx, "xiaoming", "session", accumulationID, false,
		"dictation:"+accumulationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	failed, _, _, err := d.ProcessAccumulationPracticeGeneration(
		ctx, "xiaoming", pending.GenerationID,
	)
	if err == nil || failed.Status != k12.DictationFailed {
		t.Fatalf("failure projection=%+v err=%v", failed, err)
	}
	retried, _, _, err := d.GenerateCurrentDictationToBasket(
		ctx, "xiaoming", "session", accumulationID, false,
		"dictation:"+accumulationID,
	)
	if err != nil || retried.GenerationID != pending.GenerationID ||
		retried.Status != k12.DictationQueued {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	var jobs, sets, items int
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_generation_jobs
		WHERE source_kind='accumulation' AND source_id=?`, accumulationID).Scan(&jobs)
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_sets`).Scan(&sets)
	_ = d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_practice_set_items`).Scan(&items)
	if jobs != 1 || sets != 0 || items != 0 {
		t.Fatalf("failed counts jobs/sets/items=%d/%d/%d", jobs, sets, items)
	}
}

func TestSinglePracticeCoordinator_DispatchesAccumulationSourceJob(t *testing.T) {
	d := newDataDeps(t)
	migrateSharedPracticeSources(t, d)
	accumulationID := createAccumulationForPractice(
		t, d, "a piece of cake", "create-accumulation-coordinator",
	)
	pending, _, _, err := d.GenerateCurrentDictationToBasket(
		context.Background(), "xiaoming", "session", accumulationID, false,
		"dictation:"+accumulationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &usecase.SinglePracticeGenerationCoordinator{
		Deps: &d, Records: d.Records,
	}
	if !coordinator.StartAsync("xiaoming", pending.GenerationID) {
		t.Fatal("accumulation job was not scheduled")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := coordinator.Wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	job, err := d.Records.GetPracticeGenerationJobByID(
		context.Background(), "xiaoming", pending.GenerationID,
	)
	if err != nil || job.Status != k12.PracticeGenerationCommitted {
		t.Fatalf("coordinator job=%+v err=%v", job, err)
	}
}

func TestAccumulationDictationReAddKeepsFrozenGradeAfterProfileChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_profile", true: "changed_profile"}[changed], func(t *testing.T) {
			d := newDataDeps(t)
			migrateSharedPracticeSources(t, d)
			profiles := &feedbackGenerationProfileStore{profile: k12.ChildProfile{GradeTerm: "六年级上"}}
			d.Profiles = profiles
			ctx := t.Context()
			accumulationID := createAccumulationForPractice(t, d, "a piece of cake", "create-frozen-grade")
			pending, _, _, err := d.GenerateCurrentDictationToBasket(ctx, "xiaoming", "", accumulationID, false, "dictation:"+accumulationID)
			if err != nil {
				t.Fatal(err)
			}
			committed, basketID, added, err := d.ProcessAccumulationPracticeGeneration(ctx, "xiaoming", pending.GenerationID)
			if err != nil || !added {
				t.Fatalf("initial generation commit: added=%v err=%v", added, err)
			}
			if err := d.RemoveFromBasket(ctx, "xiaoming", basketID, committed.PracticeItemID); err != nil {
				t.Fatal(err)
			}
			if changed {
				profiles.profile.GradeTerm = "六年级下"
			}
			for _, key := range []string{"dictation:" + accumulationID, "dictation:detail-entry"} {
				replayed, _, _, err := d.GenerateCurrentDictationToBasket(ctx, "xiaoming", "", accumulationID, false, key)
				if err != nil || replayed.GenerationID != pending.GenerationID || replayed.SourceSnapshot != pending.SourceSnapshot {
					t.Fatalf("unchanged source must restore its frozen generation: changed=%v generation=%+v err=%v", changed, replayed, err)
				}
				if _, _, _, err := d.GenerateCurrentDictationToBasket(ctx, "xiaoming", "", accumulationID, true, key); err == nil {
					t.Fatal("changed explicit dictation intent must conflict with the frozen request")
				}
			}
		})
	}
}

func TestDeletedLegacyAccumulationDoesNotReappearInReviewQueue(t *testing.T) {
	d := newDataDeps(t)
	ctx := t.Context()
	id, _, err := d.AddAccumulation(ctx, "xiaoming", "legacy", k12.AccumFields{Subject: "英语", EntryType: "错词", Content: "necessary"})
	if err != nil {
		t.Fatal(err)
	}
	d.Now = func() int64 { return 1000 + usecase.FirstReviewInterval + 1 }
	initial, err := d.ReviewQueue(ctx, "xiaoming")
	if err != nil || len(initial) != 1 || initial[0].Record.RecordID != id {
		t.Fatalf("due control=%+v err=%v", initial, err)
	}
	view, err := d.GetAccumulation(ctx, "xiaoming", id)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := d.Records.TombstoneCurrentObject(ctx, "xiaoming", "accumulation", id, view.RowVersion, "delete-legacy")
	if err != nil || !receipt.Deleted {
		t.Fatalf("delete receipt=%+v err=%v", receipt, err)
	}
	after, err := d.ReviewQueue(ctx, "xiaoming")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after {
		if item.Record.RecordID == id {
			t.Fatal("successfully deleted accumulation reappeared in the review queue")
		}
	}
}
