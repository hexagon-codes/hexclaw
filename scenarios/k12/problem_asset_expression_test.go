package k12

import "testing"

func TestProblemAssetExpressionFacts(t *testing.T) {
	for _, sample := range [][2]string{{`18 / 3 =`, `\(18 \div 3 =\)`}, {`7×8=`, `\(7 \times 8 =\)`}, {`56÷7=`, "56 ÷\n7 ="}} {
		a := ProblemAssetFacts{Subject: "数学", Stem: sample[0], AnswerContext: map[string]string{"grade_term": "六年级上"}}
		b := a
		b.Stem = sample[1]
		left, _ := a.ExactIdentity("owner")
		right, _ := b.ExactIdentity("owner")
		if left.Key == right.Key || !EquivalentProblemAssetExpression(a, b) {
			t.Fatalf("format candidate not distinguished from exact lookup: %q / %q", sample[0], sample[1])
		}
	}
	for _, pair := range [][2]string{
		{"18/3=", "18/2="}, {"18/3=", "3/18="}, {"18-3=", "3-18="}, {"7*8=", "8*7="},
		{"(18/3)=", "18/3="}, {"(8-3)-2=", "8-(3-2)="}, {"18/3=", "18/3=?"}, {"18/3=", "18.0/3="},
	} {
		if EquivalentProblemAssetExpression(ProblemAssetFacts{Subject: "数学", Stem: pair[0]}, ProblemAssetFacts{Subject: "数学", Stem: pair[1]}) {
			t.Fatalf("different facts treated as the same question: %q / %q", pair[0], pair[1])
		}
	}
	for _, stem := range []string{"18 cm /3 =", "1 8/3=", "计算18/3", "18/3=6", "18/()=", `\frac{18}{3}=`, "x*3=", "18/3?", "18./3="} {
		if _, ok := ProblemAssetExpressionTerms(ProblemAssetFacts{Subject: "数学", Stem: stem}); ok {
			t.Fatalf("unsupported or ambiguous input entered candidates: %q", stem)
		}
	}
	base := ProblemAssetFacts{Subject: "数学", Stem: "18/3=", SharedMaterial: []string{"单位为米"},
		Options: []ProblemAssetOption{{Label: "A", Text: "6"}, {Label: "B", Text: "5"}}, VisualFacts: []string{"线段长18米"},
		Objects: []ProblemAssetObject{{Role: "diagram", Digest: "sha256:original"}}, AnswerContext: map[string]string{"grade_term": "六年级上"}}
	for _, tc := range []struct {
		name   string
		change func(*ProblemAssetFacts)
	}{
		{"subject", func(f *ProblemAssetFacts) { f.Subject = "物理" }},
		{"shared_material_unit", func(f *ProblemAssetFacts) { f.SharedMaterial = []string{"单位为厘米"} }},
		{"options", func(f *ProblemAssetFacts) { f.Options = []ProblemAssetOption{base.Options[1], base.Options[0]} }},
		{"visual", func(f *ProblemAssetFacts) { f.VisualFacts = []string{"线段长18厘米"} }},
		{"object", func(f *ProblemAssetFacts) {
			f.Objects = []ProblemAssetObject{{Role: "diagram", Digest: "sha256:other"}}
		}},
		{"context", func(f *ProblemAssetFacts) { f.AnswerContext = map[string]string{"grade_term": "五年级上"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := base
			other.Stem = `\(18 \div 3 =\)`
			tc.change(&other)
			if EquivalentProblemAssetExpression(base, other) {
				t.Fatal("non-expression facts were ignored")
			}
		})
	}
}
