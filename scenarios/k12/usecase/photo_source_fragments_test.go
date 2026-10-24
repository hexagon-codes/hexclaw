package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestPhotoQuestionSourceCompactionPreservesModelInputAndRecognition(t *testing.T) {
	q := RecognizedQuestion{
		Question: "求硬盘总容量。", RawTranscription: "原识题文字", StudentAnswer: "孩子原作答",
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{
			Materials: []k12.RecognitionImageScopeDependencyV1{
				{Text: "已用4.8 TB。"},
				{Text: `已用占总容量的\(\frac{2}{5}\)。`},
				{Text: "已用4.8 TB。\n已用占总容量的2/5。"},
			},
			Recoveries: []k12.RecognitionImageSourceRecoveryV1{
				{State: "verified", Gap: k12.RecognitionImageSourceGapV1{Kind: "condition", Before: "已用", After: " TB。"}, AdoptedText: "4.8"},
				{State: "pending", Gap: k12.RecognitionImageSourceGapV1{Kind: "condition"}, AdoptedText: "unverified content must not appear"},
			},
		},
	}
	before, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	problem, source := photoQuestionProblemTexts(q)
	wantProblem := "求硬盘总容量。" +
		"\n\nRead-only source material (do not answer as another question):\n已用4.8 TB。" +
		"\n\nRead-only source material (do not answer as another question):\n" + `已用占总容量的\(\frac{2}{5}\)。` +
		"\n\nRead-only source material (do not answer as another question):\n已用4.8 TB。\n已用占总容量的2/5。" +
		"\n\nVerified source fragment for the declared condition gap (use only this fragment; do not infer other missing content or student handwriting):\n已用4.8 TB。"
	if problem != wantProblem {
		t.Fatalf("model input changed:\ngot: %q\nwant: %q", problem, wantProblem)
	}
	wantSource := "求硬盘总容量。\n\n已用4.8 TB。\n\n" + `已用占总容量的\(\frac{2}{5}\)。`
	if source != wantSource {
		t.Fatalf("source fragments not compacted without loss:\ngot: %q\nwant: %q", source, wantSource)
	}
	if strings.Contains(source, "unverified content") {
		t.Fatal("unverified recovery entered the source projection")
	}
	after, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source projection modified recognition, answer or recovery evidence")
	}
}
