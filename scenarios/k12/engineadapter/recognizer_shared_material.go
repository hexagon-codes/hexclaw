package engineadapter

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 局部复读只覆盖子题像素，整页首读冻结的公共条件作为同源只读材料保留。
// 不改两次转写或学生作答，不把同一份条件伪装成第二次独立观察。
func retainInitialReadSharedMaterials(questions []usecase.RecognizedQuestion, plan k12.RecognitionLayoutPlanV2, raw string, width, height int) ([]usecase.RecognizedQuestion, error) {
	if plan.Scope == nil {
		return questions, nil
	}
	entries, err := parseRecognitionInitialReadEnvelopeV1(raw)
	if err != nil {
		return nil, err
	}
	for i := range questions {
		q := &questions[i]
		for _, target := range plan.Targets {
			if q.SourceRegion == nil || *q.SourceRegion != target.Region || !slices.Equal(q.SourceNumberPath, target.SourceNumberPath) {
				continue
			}
			entry, err := recognitionInitialReadEntryForTargetV1(entries, target)
			if err != nil {
				return nil, err
			}
			var read struct {
				Shared string `json:"shared_conditions"`
			}
			if json.Unmarshal(entry.read, &read) != nil {
				continue
			}
			shared := strings.TrimSpace(read.Shared)
			if shared == "" || shared == strings.TrimSpace(target.SourceSectionLabel) || strings.Contains(q.Question, shared) {
				continue
			}
			if q.ImageSourceContext == nil {
				q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: plan.ScopeInput.Action, ScopeDigest: plan.AuthorizedPlanDigest, TargetIDs: []string{target.TargetID}}
			}
			found := false
			for _, material := range q.ImageSourceContext.Materials {
				found = found || strings.TrimSpace(material.Text) == shared
			}
			if !found {
				q.ImageSourceContext.Materials = append(q.ImageSourceContext.Materials, k12.RecognitionImageScopeDependencyV1{
					ForOrders: []int{target.ManifestOrder}, Text: shared,
					// 条件来自整页首读，整页区域明确保留其真实可见来源。
					Region: k12.SourcePixelRegion{Width: width, Height: height},
				})
			}
		}
	}
	return questions, nil
}
