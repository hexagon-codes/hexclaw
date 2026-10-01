package k12

import (
	"reflect"
	"strings"
	"testing"
)

const sourceFormatOldBread = `一袋面包重 \(\frac{3}{10}\,\text{kg}\)，3 袋重多少千克？`
const sourceFormatHistoricalBread = `一袋面包重 \\(\\frac{3}{10}\\,\\text{kg}\\)，3 袋重多少千克？`
const sourceFormatNewBread = "一袋面包重 $\\frac{3}{10}\\,\\mathrm{kg}$，3 袋重多少千克？\n\n$\\square\\times\\square=\\square\\（\\mathrm{kg}）$"

func TestProblemAssetSourceFormat_BreadSourcesAndHistoricalEscaping(t *testing.T) {
	facts := ProblemAssetFacts{Subject: "数学", Stem: sourceFormatNewBread, AnswerContext: map[string]string{"grade_term": "六年级上"}}
	before, err := facts.ExactIdentity("owner")
	if err != nil {
		t.Fatal(err)
	}
	candidates := ProblemAssetSourceFormatCandidates(facts, true)
	if len(candidates) != 2 || candidates[0].Stem != sourceFormatOldBread || candidates[1].Stem != sourceFormatHistoricalBread {
		t.Fatalf("two historical exact lookup inputs missing: %+v", candidates)
	}
	for _, stem := range []string{sourceFormatOldBread, sourceFormatHistoricalBread} {
		old := facts
		old.Stem = stem
		if !EquivalentProblemAssetSourceFormat(facts, old, true) || !EquivalentProblemAssetSourceFormat(old, facts, true) {
			t.Fatalf("same source format rejected: %q", stem)
		}
		oldIdentity, err := old.ExactIdentity("owner")
		if err != nil || oldIdentity.FactsDigest == before.FactsDigest {
			t.Fatal("format equivalence must not change v1 exact identity")
		}
	}
	after, _ := facts.ExactIdentity("owner")
	if before != after || facts.Stem != sourceFormatNewBread || !reflect.DeepEqual(facts.AnswerContext, map[string]string{"grade_term": "六年级上"}) {
		t.Fatal("lookup changed frozen facts")
	}
}

func TestProblemAssetSourceFormat_PreservesTemplateUnitsAndFullFacts(t *testing.T) {
	base := ProblemAssetFacts{Subject: "数学", Stem: sourceFormatNewBread,
		SharedMaterial: []string{"每袋重量相同"}, Options: []ProblemAssetOption{{Label: "A", Text: "原选项"}},
		VisualFacts: []string{"原事实"}, Objects: []ProblemAssetObject{{Role: "source", Digest: "sha256:original"}},
		AnswerContext: map[string]string{"grade_term": "六年级上"}}
	old := base
	old.Stem = sourceFormatHistoricalBread
	for _, tc := range []struct {
		name   string
		change func(*ProblemAssetFacts)
	}{
		{"fraction", func(f *ProblemAssetFacts) { f.Stem = strings.Replace(f.Stem, "{3}{10}", "{6}{20}", 1) }},
		{"bag_count", func(f *ProblemAssetFacts) { f.Stem = strings.Replace(f.Stem, "3 袋", "4 袋", 1) }},
		{"word_condition", func(f *ProblemAssetFacts) { f.Stem += "每袋还增加10克。" }},
		{"unit", func(f *ProblemAssetFacts) { f.Stem = strings.ReplaceAll(f.Stem, "{kg}", "{g}") }},
		{"unit_case", func(f *ProblemAssetFacts) { f.Stem = strings.ReplaceAll(f.Stem, "{kg}", "{KG}") }},
		{"subject", func(f *ProblemAssetFacts) { f.Subject = "物理" }},
		{"material", func(f *ProblemAssetFacts) { f.SharedMaterial = []string{"每袋重量不同"} }},
		{"option", func(f *ProblemAssetFacts) { f.Options = []ProblemAssetOption{{Label: "B", Text: "原选项"}} }},
		{"visual", func(f *ProblemAssetFacts) { f.VisualFacts = []string{"不同事实"} }},
		{"object", func(f *ProblemAssetFacts) { f.Objects = []ProblemAssetObject{{Role: "source", Digest: "sha256:other"}} }},
		{"context", func(f *ProblemAssetFacts) { f.AnswerContext = map[string]string{"grade_term": "五年级上"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := old
			tc.change(&other)
			if EquivalentProblemAssetSourceFormat(base, other, true) {
				t.Fatal("different original facts accepted")
			}
		})
	}
	for _, tc := range []struct {
		name, stem string
		allow      bool
	}{
		{"no_blank_evidence", sourceFormatNewBread, false},
		{"filled_template", strings.Replace(sourceFormatNewBread, `=\square`, "=0.9", 1), true},
		{"variable_template", strings.Replace(sourceFormatNewBread, `=\square`, "=x", 1), true},
		{"template_unit", strings.Replace(sourceFormatNewBread, `\（\mathrm{kg}）`, `\（\mathrm{g}）`, 1), true},
		{"method_instruction", sourceFormatNewBread + "\n必须用加法。", true},
		{"inline_template", strings.Replace(sourceFormatNewBread, "\n\n", "", 1), true},
		{"unknown_command", strings.Replace(sourceFormatNewBread, `\mathrm`, `\unknown`, 1), true},
		{"unclosed_math", strings.Replace(sourceFormatNewBread, "$，", "，", 1), true},
		{"recursive_escaping", strings.ReplaceAll(sourceFormatHistoricalBread, `\\`, `\\\\`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			f.Stem = tc.stem
			if len(ProblemAssetSourceFormatCandidates(f, tc.allow)) != 0 || EquivalentProblemAssetSourceFormat(f, old, tc.allow) {
				t.Fatal("unsupported or nonempty source layout was removed")
			}
		})
	}
}
