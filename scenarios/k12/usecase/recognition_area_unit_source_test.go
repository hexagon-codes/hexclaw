package usecase

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestScopedMaterialAreaUnitLayoutKeepsOriginalFacts(t *testing.T) {
	shared := "如下图，有大小两个圆。\n\\(r=3\\text{ cm}\\)，\\(d=4\\text{ cm}\\)\n→　①　②"
	plain := "将这两个圆按图①所示放置，涂色部分的面积是（　　）cm²。"
	for _, tc := range []struct {
		name, raw, first string
		conflict         bool
	}{
		{"latex_area", `将这两个圆按图①所示放置，涂色部分的面积是（ ）\(\mathrm{cm}^2\)。`, shared + "\n" + plain, false},
		{"repeated_escape", `将这两个圆按图①所示放置，涂色部分的面积是（ ）\\(\\mathrm{cm}^2\\)。`, shared + "\n" + plain, false},
		{"braced_power", `将这两个圆按图①所示放置，涂色部分的面积是（ ）\(\mathrm{cm}^{2}\)。`, shared + "\n" + plain, false},
		{"changed_unit", `将这两个圆按图①所示放置，涂色部分的面积是（ ）\(\mathrm{m}^2\)。`, shared + "\n" + plain, true},
		{"changed_number", `将这两个圆按图①所示放置，涂色部分的面积是（ ）\(\mathrm{cm}^2\)。`, strings.Replace(shared, "r=3", "r=4", 1) + "\n" + plain, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			confidence := .99
			q := RecognizedQuestion{Question: plain, RawTranscription: tc.raw, CanonicalMarkdown: plain,
				Subject: "数学", AnswerState: AnswerStateBlank, CanonicalVersion: 1,
				RecognitionConfidence: &confidence, SourceNumberPath: []string{"1", "1"},
				EvidenceTranscriptions: []string{tc.first, tc.raw},
				ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1,
					ScopeDigest: "sha256:" + strings.Repeat("a", 64), TargetIDs: []string{"target-1"},
					Materials: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1, 2}, Text: shared,
						Region: k12.SourcePixelRegion{X: 16, Y: 14, Width: 898, Height: 272}}}},
			}
			before := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
			got := NormalizeRecognizedQuestionForInitialReadMode(q, k12.RecognitionLayoutManifestWithContentV1)
			if got.ConfirmationRequired != tc.conflict {
				t.Fatalf("confirmation=%v want=%v reasons=%v", got.ConfirmationRequired, tc.conflict, got.ConfirmationReasons)
			}
			if got.RawTranscription != before.RawTranscription || !reflect.DeepEqual(got.EvidenceTranscriptions, before.EvidenceTranscriptions) ||
				!reflect.DeepEqual(got.ImageSourceContext, before.ImageSourceContext) {
				t.Fatal("comparison changed frozen text, units or source material")
			}
		})
	}
}
