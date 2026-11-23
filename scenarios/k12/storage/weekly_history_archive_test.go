package k12storage

import (
	"testing"

	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestSameArchivedWeekUsesOriginalAttemptIdentityForLegacyOrder(t *testing.T) {
	prior := WeeklyHistoryAttempt{
		Attempt:           k12.WeeklyPracticeAttempt{ItemID: "item-1"},
		RequestSnapshotID: "source-snapshot",
		Key:               "original-command",
		Digest:            "original-answer",
	}
	incoming := prior
	incoming.OrderID = k12.WeeklyPracticeAttemptID(prior.RequestSnapshotID, prior.Attempt.ItemID, prior.Key)
	archived := WeeklyHistoryArchive{Attempts: []WeeklyHistoryAttempt{prior}}
	restore := WeeklyHistoryArchive{Attempts: []WeeklyHistoryAttempt{incoming}}
	if !sameArchivedWeek(archived, restore) {
		t.Fatal("legacy attempt with the original order identity must match")
	}
	restore.Attempts[0].OrderID = k12.WeeklyPracticeAttemptID("target-snapshot", prior.Attempt.ItemID, prior.Key)
	if sameArchivedWeek(archived, restore) {
		t.Fatal("a different order identity must conflict")
	}
	restore.Attempts[0].OrderID = incoming.OrderID
	restore.Attempts[0].Digest = "edited-answer"
	if sameArchivedWeek(archived, restore) {
		t.Fatal("a different answer must conflict")
	}
}

func TestSameArchivedWeekKeepsOrderProvenanceAcrossLegacyHops(t *testing.T) {
	source := WeeklyHistoryAttempt{
		Attempt: k12.WeeklyPracticeAttempt{
			ItemID: "item-1", SnapshotID: "intermediate",
			AttemptID: k12.WeeklyPracticeAttemptID("intermediate", "item-1", "answer"),
		},
		RequestSnapshotID: "original", Key: "answer", Digest: "original-answer",
	}
	target := source
	target.Attempt.SnapshotID = "target"
	target.Attempt.AttemptID = k12.WeeklyPracticeAttemptID("target", "item-1", "answer")
	archived := WeeklyHistoryArchive{Attempts: []WeeklyHistoryAttempt{target}}
	check := func(source WeeklyHistoryAttempt) bool {
		source.FreezeMigrationOrder()
		source.Attempt.SnapshotID = target.Attempt.SnapshotID
		source.Attempt.AttemptID = target.Attempt.AttemptID
		return sameArchivedWeek(archived, WeeklyHistoryArchive{Attempts: []WeeklyHistoryAttempt{source}})
	}
	if !check(source) {
		t.Fatal("same attempt from an intermediate archive must match a legacy target")
	}
	explicit := source
	explicit.OrderID = source.Attempt.AttemptID
	if check(explicit) {
		t.Fatal("an explicitly different incoming order must still conflict")
	}
	archived.Attempts[0].OrderID = "frozen-order"
	if check(source) {
		t.Fatal("an existing frozen order must still conflict")
	}
}

func TestSameArchivedWeekChecksEffectiveOrderOfLegacyAttempts(t *testing.T) {
	first := WeeklyHistoryAttempt{
		Attempt:           k12.WeeklyPracticeAttempt{AttemptID: "target-a", ItemID: "item-1"},
		RequestSnapshotID: "original", Key: "first", Updated: 100,
	}
	second := first
	second.Attempt.AttemptID = "target-b"
	second.Key = "second"

	for _, tc := range []struct {
		name        string
		secondItem  string
		secondTime  int64
		firstOrder  string
		secondOrder string
		wantSame    bool
	}{
		{"same order", "item-1", 100, "source-a", "source-b", true},
		{"reversed within a second", "item-1", 100, "source-b", "source-a", false},
		{"timestamp takes precedence", "item-1", 101, "source-b", "source-a", true},
		{"separate items", "item-2", 100, "source-b", "source-a", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			priorSecond := second
			priorSecond.Attempt.ItemID, priorSecond.Updated = tc.secondItem, tc.secondTime
			prior := WeeklyHistoryArchive{Attempts: []WeeklyHistoryAttempt{first, priorSecond}}
			incomingFirst, incomingSecond := first, priorSecond
			incomingFirst.OrderID, incomingFirst.inferredOrderID = tc.firstOrder, tc.firstOrder
			incomingSecond.OrderID, incomingSecond.inferredOrderID = tc.secondOrder, tc.secondOrder
			incoming := WeeklyHistoryArchive{Attempts: []WeeklyHistoryAttempt{incomingFirst, incomingSecond}}
			if got := sameArchivedWeek(prior, incoming); got != tc.wantSame {
				t.Fatalf("archive equality = %v, want %v", got, tc.wantSame)
			}
		})
	}
}
