package knowledge

import (
	"context"
	"strconv"

	"github.com/hexagon-codes/hexagon/evaluate"
)

// ToEvaluationReport 把已取得的排名投影为共享评测报告，不重新检索或调用模型。
// hit@k仍表示首个相关标题在前k项中，不能替换为相关文档总数作分母的recall。
// 报告Duration只表示投影耗时；无原始时耗或费用时不推算对应数值。
func (r EvalReport) ToEvaluationReport(ctx context.Context) (*evaluate.EvalReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	metrics := []struct {
		name  string
		score func(int) float64
	}{
		{"hit_at_1", func(rank int) float64 {
			if rank == 1 {
				return 1
			}
			return 0
		}},
		{"hit_at_k", func(rank int) float64 {
			if rank > 0 && rank <= r.K {
				return 1
			}
			return 0
		}},
		{"reciprocal_rank", func(rank int) float64 {
			if rank > 0 {
				return 1 / float64(rank)
			}
			return 0
		}},
	}
	config := &evaluate.EvalConfig{Concurrency: 1}
	for _, metric := range metrics {
		config.Evaluators = append(config.Evaluators, evaluate.NewCustomEvaluator(metric.name, "Retrieval rank projection", func(_ context.Context, input evaluate.EvalInput) (*evaluate.EvalResult, error) {
			rank := input.Metadata["rank"].(int)
			return &evaluate.EvalResult{Score: metric.score(rank)}, nil
		}))
	}
	dataset := &evaluate.Dataset{Name: "knowledge_retrieval"}
	for index, sample := range r.Cases {
		dataset.Samples = append(dataset.Samples, evaluate.Sample{
			ID: strconv.Itoa(index), Query: sample.Query,
			Metadata: map[string]any{"rank": sample.Rank, "name": sample.Name},
		})
	}
	report, err := evaluate.NewRunner(config).EvaluateDataset(ctx, dataset, evaluate.SystemFunc(func(context.Context, string) (*evaluate.SystemResponse, error) {
		return &evaluate.SystemResponse{}, nil
	}))
	if err != nil {
		return nil, err
	}
	for index := range report.Results {
		ordinal, err := strconv.Atoi(report.Results[index].SampleID)
		if err != nil {
			return nil, err
		}
		report.Results[index].Response = r.Cases[ordinal].TopTitle
	}
	report.Metadata = map[string]any{"top_k": r.K, "requested_samples": r.N, "source": "observed_retrieval_ranks"}
	return report, nil
}
