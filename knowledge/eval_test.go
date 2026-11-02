package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexagon/evaluate"
)

// fakeEvalSearcher 按预设把 query 映射到有序文档标题，确定性单测指标计算。
type fakeEvalSearcher struct {
	results map[string][]string
}

func (f *fakeEvalSearcher) Search(_ context.Context, q string, topK int) ([]SearchHit, error) {
	var hits []SearchHit
	for i, t := range f.results[q] {
		if i >= topK {
			break
		}
		hits = append(hits, SearchHit{DocTitle: t})
	}
	return hits, nil
}

func TestRunEval_Metrics(t *testing.T) {
	ds := EvalDataset{
		{"a_top1", "qa", []string{"DocA"}},  // 命中 top1
		{"b_rank3", "qb", []string{"DocB"}}, // 命中 rank3
		{"c_miss", "qc", []string{"DocC"}},  // top3 未命中
	}
	fake := &fakeEvalSearcher{results: map[string][]string{
		"qa": {"DocA", "X", "Y"},
		"qb": {"X", "Y", "DocB"},
		"qc": {"X", "Y", "Z"},
	}}
	rep, err := RunEval(context.Background(), fake, ds, 3)
	if err != nil {
		t.Fatal(err)
	}
	// recall@1 = 1/3；recall@3 = 2/3；MRR = (1 + 1/3 + 0)/3
	if !approxEq(rep.RecallAt1, 1.0/3) {
		t.Errorf("recall@1=%v 期望 0.333", rep.RecallAt1)
	}
	if !approxEq(rep.RecallAtK, 2.0/3) {
		t.Errorf("recall@3=%v 期望 0.667", rep.RecallAtK)
	}
	if !approxEq(rep.MRR, (1+1.0/3)/3) {
		t.Errorf("MRR=%v 期望 0.444", rep.MRR)
	}
	// 排名核对
	if rep.Cases[1].Rank != 3 {
		t.Errorf("b 的 rank 应为 3，got %d", rep.Cases[1].Rank)
	}
	if rep.Cases[2].Rank != 0 {
		t.Errorf("c 未命中 rank 应为 0，got %d", rep.Cases[2].Rank)
	}
	report, err := rep.ToEvaluationReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]float64{"hit_at_1": 1.0 / 3, "hit_at_k": 2.0 / 3, "reciprocal_rank": (1 + 1.0/3) / 3} {
		if summary := report.Summary[name]; summary == nil || summary.Count != 3 || !approxEq(summary.Mean, want) {
			t.Fatalf("shared metric %s = %+v, want %v", name, summary, want)
		}
	}
	var encoded bytes.Buffer
	if exportErr := evaluate.NewJSONReporter(false).Generate(report, &encoded); exportErr != nil {
		t.Fatal(exportErr)
	}
	if !json.Valid(encoded.Bytes()) {
		t.Fatal("invalid shared JSON report")
	}
	duplicate := EvalReport{K: 3, N: 2, Cases: []EvalCaseResult{
		{Name: "same", Query: "same", TopTitle: "A", Rank: 1},
		{Name: "same", Query: "same", TopTitle: "B", Rank: 2},
	}}
	shared, err := duplicate.ToEvaluationReport(context.Background())
	if err != nil || len(shared.Results) != 2 || shared.Results[0].SampleID == shared.Results[1].SampleID {
		t.Fatalf("duplicate sample identities: report=%+v err=%v", shared, err)
	}
}

func TestKnowledgeConfigEvalFixturesMatchCurrentRetrievalContract(t *testing.T) {
	tests := []struct {
		name string
		docs []EvalDoc
	}{
		{name: "golden", docs: GoldenCorpus()},
		{name: "scenario", docs: func() []EvalDoc {
			docs := make([]EvalDoc, 0, len(scenarioCorpus))
			for _, doc := range scenarioCorpus {
				docs = append(docs, EvalDoc{Title: doc.title, Content: doc.content})
			}
			return docs
		}()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var configDoc string
			for _, doc := range tc.docs {
				if doc.Title == "HexClaw 知识库检索配置" {
					configDoc = doc.Content
					break
				}
			}
			if configDoc == "" {
				t.Fatal("missing HexClaw knowledge retrieval config fixture")
			}
			for _, want := range []string{"默认 0.85", "专用 cross-encoder", "无专用 executor 时使用 MMR"} {
				if !strings.Contains(configDoc, want) {
					t.Errorf("fixture does not describe %q: %q", want, configDoc)
				}
			}
			for _, stale := range []string{"默认 0.55", "LLM 重排"} {
				if strings.Contains(configDoc, stale) {
					t.Errorf("fixture still describes obsolete %q contract: %q", stale, configDoc)
				}
			}
		})
	}
}

func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
