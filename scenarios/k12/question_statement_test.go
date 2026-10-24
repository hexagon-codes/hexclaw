package k12_test

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestMistakeQuestionStatementRestoresOnlyKnownPhotoEnvelopes(t *testing.T) {
	shared := "小明 $\\frac{2}{3}$ 小时走了2千米。\nRead the instruction in English.\nCurrent canonical:\n这行是教材原文。"
	current := "已知（ ）份是2千米。"
	scoped, ok := elementarymath.BuildPartCountSlotProblemFromSources(shared, current, current)
	if !ok {
		t.Fatal("valid source did not form a part-count request")
	}
	const materialMarker = "\n\nRead-only source material (do not answer as another question):\n"
	const suffix = "\n\nFor this request, solve or assess only subproblem a. Use the shared material as context; do not answer the other subproblems."
	plain := "Fill the blank.\n这个硬盘的总容量是多少？"
	material := `已用空间占总容量的\(\frac{2}{5}\)，已用4.8 TB。`
	quoted := `Translate "Read-only source material" and "Current canonical" into Chinese.`
	cases := []struct {
		name, question, want, entry string
	}{
		{"versioned source", scoped, current + "\n\n" + shared, k12.MistakeEntryPhoto},
		{"legacy source without entry metadata", scoped, current + "\n\n" + shared, ""},
		{"old exact suffix", plain + suffix, plain, k12.MistakeEntryPhoto},
		{"material envelope", plain + materialMarker + material + materialMarker + shared, plain + "\n\n" + material + "\n\n" + shared, k12.MistakeEntryPhoto},
		{"material and exact suffix", plain + materialMarker + material + suffix, plain + "\n\n" + material, k12.MistakeEntryPhoto},
		{"legitimate English", quoted, quoted, k12.MistakeEntryPhoto},
		{"manual protocol quotation", plain + materialMarker + material, plain + materialMarker + material, k12.MistakeEntryManual},
		{"incomplete versioned contract", strings.TrimSuffix(scoped, "."), strings.TrimSuffix(scoped, "."), k12.MistakeEntryPhoto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := k12.MistakeFields{Question: tc.question, EntrySource: tc.entry}
			if got := k12.MistakeQuestionStatement(fields); got != tc.want {
				t.Fatalf("source projection differs:\ngot: %q\nwant: %q", got, tc.want)
			}
			if fields.Question != tc.question {
				t.Fatal("source projection modified the stored question")
			}
		})
	}
}
