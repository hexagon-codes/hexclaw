package k12

import "testing"

func TestProblemAssetIdentity_EquivalentRepresentation(t *testing.T) {
	a := ProblemAssetFacts{Subject: "英语", Stem: "  cafe\u0301\r\nChoose a word. ",
		AnswerContext: map[string]string{"dialect": "British", "task": "spelling"}}
	b := ProblemAssetFacts{Subject: "英语", Stem: "café\nChoose a word.",
		AnswerContext: map[string]string{"task": "spelling", "dialect": "British"}}
	first, err := a.ExactIdentity("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.ExactIdentity("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("equivalent Unicode and line endings must identify the same facts")
	}
	otherOwner, err := a.ExactIdentity("owner-b")
	if err != nil {
		t.Fatal(err)
	}
	if first.Key == otherOwner.Key || first.FactsDigest != otherOwner.FactsDigest {
		t.Fatal("owner scope must differ without changing question facts")
	}
	if a.Stem != "  cafe\u0301\r\nChoose a word. " {
		t.Fatal("normalization must not modify the original expression")
	}
}

func TestProblemAssetIdentity_AnswerRelevantChanges(t *testing.T) {
	original := ProblemAssetFacts{Subject: "数学", Stem: "按图计算：2 km 等于多少米？",
		SharedMaterial: []string{"选出正确答案"},
		Options:        []ProblemAssetOption{{Label: "A", Text: "200"}, {Label: "B", Text: "2000"}},
		VisualFacts:    []string{"线段长度为2 km"},
		Objects:        []ProblemAssetObject{{Role: "diagram", Digest: "sha256:original-diagram"}},
		AnswerContext:  map[string]string{"unit_system": "metric"}}
	base, err := original.ExactIdentity("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*ProblemAssetFacts)
	}{
		{"number", func(f *ProblemAssetFacts) { f.Stem = "按图计算：3 km 等于多少米？" }},
		{"unit", func(f *ProblemAssetFacts) { f.Stem = "按图计算：2 cm 等于多少米？" }},
		{"option_order", func(f *ProblemAssetFacts) { f.Options = []ProblemAssetOption{original.Options[1], original.Options[0]} }},
		{"shared_material", func(f *ProblemAssetFacts) { f.SharedMaterial = []string{"选出错误答案"} }},
		{"visual_facts", func(f *ProblemAssetFacts) { f.VisualFacts = []string{"线段长度为2 cm"} }},
		{"diagram", func(f *ProblemAssetFacts) {
			f.Objects = []ProblemAssetObject{{Role: "diagram", Digest: "sha256:other-diagram"}}
		}},
		{"answer_context", func(f *ProblemAssetFacts) { f.AnswerContext = map[string]string{"unit_system": "imperial"} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := original
			change.mutate(&changed)
			got, err := changed.ExactIdentity("owner-a")
			if err != nil {
				t.Fatal(err)
			}
			if got.Key == base.Key || got.FactsDigest == base.FactsDigest {
				t.Fatal("answer-relevant change must not count as an exact match")
			}
		})
	}
}

func TestProblemAnswerSource_DoesNotClaimAssetAsInvocation(t *testing.T) {
	asset := ProblemAnswerSource{Kind: ProblemAnswerAsset, FactsDigest: "sha256:facts",
		AssetID: "asset-1", AssetVersion: 1, AssetRevision: 1, AdoptionID: "adoption-1"}
	if err := asset.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*ProblemAnswerSource)
	}{
		{"fake_invocation", func(s *ProblemAnswerSource) { s.InvocationID = "asset-1" }},
		{"missing_adoption", func(s *ProblemAnswerSource) { s.AdoptionID = "" }},
		{"missing_version", func(s *ProblemAnswerSource) { s.AssetVersion = 0 }},
		{"missing_revision", func(s *ProblemAnswerSource) { s.AssetRevision = 0 }},
		{"missing_facts", func(s *ProblemAnswerSource) { s.FactsDigest = "" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			invalid := asset
			change.mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("incomplete or mixed asset source must not pass the source contract")
			}
		})
	}
	for _, kind := range []ProblemAnswerSourceKind{ProblemAnswerModel, ProblemAnswerDeterministic} {
		executed := ProblemAnswerSource{Kind: kind, FactsDigest: "sha256:facts", InvocationID: "actual-invocation-1"}
		if err := executed.Validate(); err != nil {
			t.Fatal(err)
		}
		executed.AssetID = "asset-1"
		if err := executed.Validate(); err == nil {
			t.Fatal("executed answer source must not also claim an asset")
		}
	}
}
