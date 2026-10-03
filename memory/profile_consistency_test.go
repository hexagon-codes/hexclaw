package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type consistencySynth func(context.Context, []string, string) (string, error)

func (f consistencySynth) Synthesize(ctx context.Context, facts []string, prev string) (string, error) {
	return f(ctx, facts, prev)
}

type consistencyEditor func(context.Context, []MemoryEntry, string, string) (ProfileEditPlan, error)

func (f consistencyEditor) PlanProfileEdit(ctx context.Context, e []MemoryEntry, b, a string) (ProfileEditPlan, error) {
	return f(ctx, e, b, a)
}

func TestProfileDeletionRefreshAndColdRead(t *testing.T) {
	opts := Options{Dir: t.TempDir(), Enabled: true}
	fm, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err = fm.SaveEntry("用户偏好蓝色", "preference", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = fm.SaveEntry("用户偏好橙色", "fact", "manual"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	syn := consistencySynth(func(_ context.Context, f []string, prev string) (string, error) {
		calls++
		if prev != "" {
			t.Fatal("stale profile sent as fact")
		}
		return strings.Join(f, "。"), nil
	})
	if _, err = fm.DistillProfileForRole(context.Background(), "", syn, DistillProfileConfig{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var deleted string
	for _, e := range fm.ParseEntries() {
		if e.Content == "用户偏好橙色" {
			deleted = e.ID
		}
	}
	if err = fm.DeleteEntry(deleted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fm.LoadContext(), "橙色") {
		t.Fatal("deleted fact survived through stale profile")
	}
	for _, e := range fm.ParseEntries() {
		if isProfileEntry(e) {
			t.Fatal("stale profile is visible")
		}
	}
	cold, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cold.LoadContext(), "橙色") {
		t.Fatal("cold read revived deleted profile fact")
	}
	if _, err = cold.DistillProfileForRole(context.Background(), "", syn, DistillProfileConfig{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = cold.DistillProfileForRole(context.Background(), "", syn, DistillProfileConfig{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("refresh sent %d calls, want 2 source versions", calls)
	}
	entries := cold.ParseEntries()
	if len(entries) != 2 {
		t.Fatalf("want one source and one profile, got %d", len(entries))
	}
	if strings.Contains(cold.LoadContext(), "橙色") {
		t.Fatal("rebuilt summary revived deleted fact")
	}
}

func TestProfileRejectsInFlightStaleAndUnknownReplay(t *testing.T) {
	opts := Options{Dir: t.TempDir(), Enabled: true}
	fm, _ := New(opts)
	if err := fm.SaveEntry("用户喜欢蓝色", "preference", "manual"); err != nil {
		t.Fatal(err)
	}
	id := fm.ParseEntries()[0].ID
	_, err := fm.DistillProfileForRole(context.Background(), "", consistencySynth(func(_ context.Context, _ []string, _ string) (string, error) {
		if err := fm.UpdateEntry(id, "用户喜欢绿色"); err != nil {
			t.Fatal(err)
		}
		return "用户喜欢蓝色", nil
	}), DistillProfileConfig{}, time.Now())
	if !errors.Is(err, ErrProfileStale) {
		t.Fatalf("want stale rejection, got %v", err)
	}
	calls := 0
	unknown := consistencySynth(func(context.Context, []string, string) (string, error) { calls++; return "", context.DeadlineExceeded })
	if _, err = fm.DistillProfileForRole(context.Background(), "", unknown, DistillProfileConfig{}, time.Now()); !errors.Is(err, ErrProfileOutcomeUnknown) {
		t.Fatal(err)
	}
	cold, _ := New(opts)
	if _, err = cold.DistillProfileForRole(context.Background(), "", unknown, DistillProfileConfig{}, time.Now()); !errors.Is(err, ErrProfileOutcomeUnknown) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unknown profile call was resent")
	}
}

func TestProfileCorrectionCommitsSourcesAndSummaryTogether(t *testing.T) {
	opts := Options{Dir: t.TempDir(), Enabled: true}
	fm, _ := New(opts)
	if err := fm.SaveStructuredEntry("用户喜欢蓝色", "preference", "chat_extract", "", EntryMeta{Pinned: true, HitCount: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := fm.UpsertProfileForRole("用户喜欢蓝色", ""); err != nil {
		t.Fatal(err)
	}
	entries := fm.ParseEntries()
	var source, profile MemoryEntry
	for _, e := range entries {
		if isProfileEntry(e) {
			profile = e
		} else {
			source = e
		}
	}
	calls := 0
	editor := consistencyEditor(func(_ context.Context, _ []MemoryEntry, b, a string) (ProfileEditPlan, error) {
		calls++
		return ProfileEditPlan{Changes: []ProfileEditChange{{ID: source.ID, Before: source.Content, OldSpan: "蓝色", NewSpan: "绿色"}}}, nil
	})
	if err := fm.EditProfile(context.Background(), profile.ProfileRevision, "用户喜欢绿色", editor); err != nil {
		t.Fatal(err)
	}
	if err := fm.EditProfile(context.Background(), profile.ProfileRevision, "用户喜欢绿色", editor); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("replayed edit called model twice")
	}
	cold, _ := New(opts)
	entries = cold.ParseEntries()
	if len(entries) != 2 {
		t.Fatal("edit duplicated or lost entries")
	}
	for _, e := range entries {
		if strings.Contains(e.Content, "蓝色") {
			t.Fatal("old correction survived")
		}
		if e.ID == source.ID && (!e.Pinned || e.HitCount != 7 || e.Source != "chat_extract" || !e.ManualCorrection) {
			t.Fatalf("source metadata lost: %+v", e)
		}
	}
}
