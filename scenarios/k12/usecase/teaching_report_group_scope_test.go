package usecase

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestReportChecksRespectPrintedSectionAndSharedSource(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sections   [2]string
		shared     bool
		wantGroups int
	}{
		{"same parent with shared printed material", [2]string{"基础", "基础"}, true, 1},
		{"reused parent number in another section", [2]string{"基础", "拓展"}, true, 2},
		{"printed number alone is not shared provenance", [2]string{"基础", "基础"}, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qs := []RecognizedQuestion{
				{ProblemID: "first", PageAssetID: "same-page", SourceNumberPath: []string{"3", "(1)"}, SourceSectionPath: []string{tc.sections[0]}},
				{ProblemID: "second", PageAssetID: "same-page", SourceNumberPath: []string{"3", "(2)"}, SourceSectionPath: []string{tc.sections[1]}},
			}
			if tc.shared {
				for i := range qs {
					qs[i].ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, ScopeDigest: "same-frozen-page", TargetIDs: []string{qs[i].ProblemID}, Materials: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1, 2}, Region: k12.SourcePixelRegion{X: 10, Y: 10, Width: 100, Height: 30}, Text: "已用空间占总容量的2/5，已用4.8 TB。"}}}
				}
			}
			sections := []k12.TeachingReportDeepSection{
				{ID: "first-check", Title: "检查", ProblemIDs: []string{"first"}, Blocks: []k12.TeachingReportExplanationBlock{{ID: "first-equation", LaTeX: `12\times\frac25=4.8`, Origins: []k12.TeachingReportContentOrigin{{ProblemID: "first"}}}}},
				{ID: "second-check", Title: "检查", ProblemIDs: []string{"second"}, Blocks: []k12.TeachingReportExplanationBlock{{ID: "second-equation", LaTeX: `12\times\frac25=4.8`, Origins: []k12.TeachingReportContentOrigin{{ProblemID: "second"}}}}},
			}
			got := consolidateGradingReportSections(qs, sections)
			if len(got) != tc.wantGroups {
				t.Fatalf("source groups=%d, want %d; unrelated printed scopes must remain separate", len(got), tc.wantGroups)
			}
			if tc.wantGroups == 2 && (len(got[0].ProblemIDs) != 1 || len(got[1].ProblemIDs) != 1) {
				t.Fatal("a check borrowed the other printed scope")
			}
			if tc.wantGroups == 1 && (len(got[0].ProblemIDs) != 2 || len(got[0].Blocks[0].Origins) != 2) {
				t.Fatal("merging a genuinely shared check lost its source lineage")
			}
		})
	}
}

func TestMixedReportSectionsPreserveOrderAndSharedEquationOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, secondEquation string
		methodReference      bool
		wantEquations        int
	}{
		{"same relation with display unit parentheses", `12\times\frac25=4.8\ (\mathrm{TB})`, false, 1},
		{"different physical unit", `12\times\frac25=4.8\ (\mathrm{GB})`, false, 2},
		{"method-specific equation reference", `12\times\frac25=4.8\ (\mathrm{TB})`, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qs := []RecognizedQuestion{{ProblemID: "first", PageAssetID: "page", ParentProblemID: "parent"}, {ProblemID: "second", PageAssetID: "page", ParentProblemID: "parent"}}
			first := k12.TeachingReportDeepSection{ID: "first-section", Title: "先求一份", ProblemIDs: []string{"first"}, Blocks: []k12.TeachingReportExplanationBlock{{ID: "first-explanation", Text: "先用已知容量除以对应份数。", Origins: []k12.TeachingReportContentOrigin{{ProblemID: "first"}}}, {ID: "first-equation", LaTeX: `12\times\frac25=4.8\,\mathrm{TB}`, Origins: []k12.TeachingReportContentOrigin{{ProblemID: "first"}}}}}
			second := k12.TeachingReportDeepSection{ID: "second-section", Title: "由部分求整体", ProblemIDs: []string{"second"}, Blocks: []k12.TeachingReportExplanationBlock{{ID: "second-explanation", Text: "容量与无单位的比例不能混填。", Origins: []k12.TeachingReportContentOrigin{{ProblemID: "second"}}}, {ID: "second-equation", LaTeX: tc.secondEquation, Origins: []k12.TeachingReportContentOrigin{{ProblemID: "second"}}}}}
			if tc.methodReference {
				second.MethodNotes = []k12.TeachingReportMethodNote{{Name: "代回", BlockIDs: []string{"second-equation"}}}
			}
			got := consolidateGradingReportSections(qs, []k12.TeachingReportDeepSection{second, first})
			if len(got) != 2 || got[0].ID != "first-section" || got[1].ID != "second-section" {
				t.Fatal("completion order replaced source question order")
			}
			count := 0
			for _, section := range got {
				for _, block := range section.Blocks {
					if block.LaTeX != "" {
						count++
					}
				}
			}
			if count != tc.wantEquations {
				t.Fatalf("shared equations=%d, want %d; keys=%q/%q", count, tc.wantEquations, reportExplanationKey(first.Blocks[1]), reportExplanationKey(second.Blocks[1]))
			}
			if got[1].Blocks[0].Text != "容量与无单位的比例不能混填。" {
				t.Fatal("unique explanation was removed")
			}
			if tc.wantEquations == 1 && (len(got[0].Blocks[1].Origins) != 2 || len(got[0].ProblemIDs) != 2) {
				t.Fatal("shared equation lost one source")
			}
		})
	}
}
