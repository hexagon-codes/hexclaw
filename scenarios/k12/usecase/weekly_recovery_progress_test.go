package usecase

import (
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"testing"
)

func TestWeeklyRecoveryProgress_LegacyEstimateDoesNotClaimVerifiedPages(t *testing.T) {
	from, to := 27, 45
	frozen := k12.CurriculumProgress{AgentName: "child", Revision: 1, UnitID: "fractions", EvidenceSource: "ai_estimated", PageVerificationStatus: "not_requested", VerifiedPageFrom: &from, VerifiedPageTo: &to}
	current := frozen
	current.VerifiedPageFrom, current.VerifiedPageTo = nil, nil
	if !weeklyRecoveryProgressMatches(frozen, current) {
		t.Fatal("legacy estimate cleanup blocked unchanged source")
	}
	for _, change := range []struct {
		name   string
		mutate func(*k12.CurriculumProgress)
	}{
		{"changed unit", func(p *k12.CurriculumProgress) { p.UnitID = "other" }},
		{"changed revision", func(p *k12.CurriculumProgress) { p.Revision++ }},
		{"confirmed source", func(p *k12.CurriculumProgress) { p.EvidenceSource = "parent_confirmed" }},
		{"requested pages", func(p *k12.CurriculumProgress) { p.RequestedPageFrom = &from }},
		{"real verification", func(p *k12.CurriculumProgress) { p.PageVerificationStatus = "verified" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			next := current
			change.mutate(&next)
			if weeklyRecoveryProgressMatches(frozen, next) {
				t.Fatal("accepted a changed source")
			}
		})
	}
	if frozen.VerifiedPageFrom == nil || frozen.VerifiedPageTo == nil {
		t.Fatal("mutated frozen source")
	}
}
