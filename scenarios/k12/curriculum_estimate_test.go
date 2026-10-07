package k12_test

import (
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestAutoProgressReferenceDateAndCatalogWeights(t *testing.T) {
	lessons := make([]k12.CurriculumCatalogLesson, 8)
	catalog := k12.CurriculumCatalog{Units: []k12.CurriculumCatalogUnit{{UnitID: "long", Lessons: lessons}, {UnitID: "short", Lessons: make([]k12.CurriculumCatalogLesson, 1)}, {UnitID: "last", PageFrom: 20, PageTo: 20}}}
	cases := []struct{ name, date, term, want, start, end string }{
		{"term start", "2026-09-01T00:00:00+08:00", "五年级上", "long", "2026-09-01", "2027-01-31"},
		{"weighted chapter", "2026-11-01T00:00:00+08:00", "五年级上", "long", "2026-09-01", "2027-01-31"},
		{"term end", "2027-01-31T00:00:00+08:00", "五年级上", "last", "2026-09-01", "2027-01-31"},
		{"before lower term", "2026-02-15T00:00:00+08:00", "五年级下", "long", "2026-03-01", "2026-06-30"},
		{"after lower term", "2026-07-15T00:00:00+08:00", "五年级下", "last", "2026-03-01", "2026-06-30"},
		{"Shanghai date", "2026-08-31T16:30:00Z", "五年级上", "long", "2026-09-01", "2027-01-31"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			date, err := time.Parse(time.RFC3339, c.date)
			if err != nil {
				t.Fatal(err)
			}
			unit, basis, err := k12.EstimateCurriculumUnit(catalog, c.term, date)
			if err != nil {
				t.Fatal(err)
			}
			if unit.UnitID != c.want || basis.ReferenceTermStart != c.start || basis.ReferenceTermEnd != c.end {
				t.Fatalf("unit/basis=%s %+v", unit.UnitID, basis)
			}
			if c.name == "Shanghai date" && basis.AsOfDate != "2026-09-01" {
				t.Fatalf("Shanghai date=%s", basis.AsOfDate)
			}
		})
	}
}

func TestAutoProgressGradeUsesCoverEvidence(t *testing.T) {
	for _, c := range []struct{ text, want string }{{"义务教育教科书\n数学 五年级 上册", "五年级上"}, {"五年级上.pdf", ""}, {"一年级上册\n二年级下册", ""}} {
		if got := k12.ParseTextbookGradeTerm(c.text); got != c.want {
			t.Fatalf("cover=%q got=%q want=%q", c.text, got, c.want)
		}
	}
}
