package k12_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestCompactQuestionSourceFragmentsPreservesDistinctFacts(t *testing.T) {
	current := "求硬盘的总容量。"
	capacity := "已用空间为4.8 TB。"
	fraction := `已用空间占总容量的\(\frac{2}{5}\)。`
	cases := []struct {
		name      string
		fragments []string
		want      string
	}{
		{"current repeated in shared", []string{current, capacity + current}, current + "\n\n" + capacity},
		{"individual and aggregate material", []string{current, capacity, fraction, capacity + "\n" + fraction}, current + "\n\n" + capacity + "\n\n" + fraction},
		{"format equivalent retains original markdown", []string{fraction, "已用空间占总容量的 ２／５。"}, fraction},
		{"missing terminal full stop", []string{"已知（ ）份是2千米", "已知（　）份是 $2$ 千米。"}, "已知（ ）份是2千米"},
		{"ratio colon formatting", []string{"男生与女生人数的比是4∶5。", "男生与女生人数的比是4：5。"}, "男生与女生人数的比是4∶5。"},
		{"new condition survives aggregate", []string{capacity, capacity + "剩余空间为7.2 TB。"}, capacity + "\n\n剩余空间为7.2 TB。"},
		{"one is not twelve", []string{"长度为1 m。", "长度为12 m。"}, "长度为1 m。\n\n长度为12 m。"},
		{"separate numbers are not one token", []string{"依次读出1 2。", "依次读出12。"}, "依次读出1 2。\n\n依次读出12。"},
		{"unit case is significant", []string{"容量为1 MB。", "容量为1 Mb。"}, "容量为1 MB。\n\n容量为1 Mb。"},
		{"different quantity relationship", []string{"甲比乙多2米。", "甲比乙少2米。"}, "甲比乙多2米。\n\n甲比乙少2米。"},
		{"legitimate English remains", []string{`Translate "Read-only source material" and "Current canonical" into Chinese.`}, `Translate "Read-only source material" and "Current canonical" into Chinese.`},
		{"English word boundaries remain", []string{"Read only the material.", "Readonly the material."}, "Read only the material.\n\nReadonly the material."},
		{"repetition within source is retained", []string{"Read it again.\nRead it again."}, "Read it again.\nRead it again."},
		{"other numbered question keeps its instructions", []string{"（1）求甲。列式：____。", "（1）求甲。列式：____。\n（2）求乙。列式：____。"}, "（1）求甲。列式：____。\n\n（2）求乙。列式：____。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.fragments...)
			if got := k12.CompactQuestionSourceFragments(tc.fragments...); got != tc.want {
				t.Fatalf("source content changed or remained duplicated:\ngot: %q\nwant: %q", got, tc.want)
			}
			if !reflect.DeepEqual(before, tc.fragments) {
				t.Fatal("compaction modified the original source fragments")
			}
		})
	}
}

func TestMistakeQuestionStatementCompactsOnlyRestoredSourceFragments(t *testing.T) {
	current := "（1）求总容量。"
	condition := "已用4.8 TB，占总容量的2/5。"
	scoped, ok := elementarymath.BuildMathSubproblem(condition+"\n"+current, current, current)
	if !ok {
		t.Fatal("valid source did not form a versioned math problem")
	}
	const separator = "\n\nRead-only source material (do not answer as another question):\n"
	fragments := "求总容量。" + separator + "已用4.8 TB。" + separator + "占总容量的2/5。" + separator + "已用4.8 TB。\n占总容量的2/5。"
	cases := []struct{ name, input, want string }{
		{"versioned current and shared", scoped, current + "\n\n" + condition},
		{"complete material markers", fragments, "求总容量。\n\n已用4.8 TB。\n\n占总容量的2/5。"},
		{"incomplete version untouched", strings.TrimSuffix(scoped, "."), strings.TrimSuffix(scoped, ".")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := k12.MistakeFields{Question: tc.input, EntrySource: k12.MistakeEntryPhoto, CanonicalAnswer: "stored answer remains private"}
			before := fields
			if got := k12.MistakeQuestionStatement(fields); got != tc.want {
				t.Fatalf("question source projection differs:\ngot: %q\nwant: %q", got, tc.want)
			}
			if !reflect.DeepEqual(before, fields) {
				t.Fatal("projection changed the original mistake or reference answer")
			}
		})
	}
}
