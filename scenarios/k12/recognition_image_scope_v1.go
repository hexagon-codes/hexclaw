package k12

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

const RecognitionImageScopeVersionV1 = "image_scope_v1"

// 范围输入随任务冻结；缺少版本的历史任务继续使用原有整页合同。
type RecognitionImageScopeInputV1 struct {
	Version       string `json:"version"`
	MessageIntent string `json:"message_intent,omitempty"`
	Action        string `json:"action,omitempty"`
}

type RecognitionImageScopeDependencyV1 struct {
	ForOrders []int             `json:"for_orders"`
	Region    SourcePixelRegion `json:"region"`
	Text      string            `json:"text"`
}

type RecognitionImageScopeExclusionV1 struct {
	Order  int    `json:"order"`
	Reason string `json:"reason"`
}

type RecognitionImageSourceGapV1 struct {
	Order  int    `json:"order"`
	Kind   string `json:"kind"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type RecognitionImageSourceRecoveryV1 struct {
	Gap                RecognitionImageSourceGapV1 `json:"gap"`
	State              string                      `json:"state"`
	AdoptedText        string                      `json:"adopted_text,omitempty"`
	SourceKind         string                      `json:"source_kind,omitempty"`
	DocumentID         string                      `json:"document_id,omitempty"`
	DocumentGeneration int64                       `json:"document_generation,omitempty"`
	SourceDigest       string                      `json:"source_digest,omitempty"`
	CitationDigest     string                      `json:"citation_digest,omitempty"`
	LogicalPage        int                         `json:"logical_page,omitempty"`
	PDFPage            int                         `json:"pdf_page,omitempty"`
	URL                string                      `json:"url,omitempty"`
}

// 来源上下文与 OCR 原文分离，随原任务运行文件及输入摘要冻结。
type RecognitionImageSourceContextV1 struct {
	ScopeDigest string                              `json:"scope_digest"`
	TargetIDs   []string                            `json:"target_ids"`
	Materials   []RecognitionImageScopeDependencyV1 `json:"materials,omitempty"`
	Recoveries  []RecognitionImageSourceRecoveryV1  `json:"recoveries,omitempty"`
}

func RecognitionImageScopeInputIdentityV1(input *RecognitionImageScopeInputV1) string {
	if input == nil {
		return ""
	}
	raw, _ := json.Marshal(input)
	return recognitionLayoutSHA256(raw)
}

// 选择引用原始清单序号，持久目标标识仍由服务端生成。材料只读，不产生评估。
type RecognitionImageScopeV1 struct {
	Version        string                              `json:"version"`
	Source         string                              `json:"source"`
	SelectedOrders []int                               `json:"selected_orders"`
	Basis          string                              `json:"basis"`
	Dependencies   []RecognitionImageScopeDependencyV1 `json:"dependencies"`
	Excluded       []RecognitionImageScopeExclusionV1  `json:"excluded"`
	Gaps           []RecognitionImageSourceGapV1       `json:"gaps,omitempty"`
}

type recognitionImageScopeContextKey struct{}

func WithRecognitionImageScopeV1(ctx context.Context, input *RecognitionImageScopeInputV1) context.Context {
	if input == nil {
		return ctx
	}
	return context.WithValue(ctx, recognitionImageScopeContextKey{}, *input)
}

func RecognitionImageScopeInputFromContextV1(ctx context.Context) (RecognitionImageScopeInputV1, bool) {
	v, ok := ctx.Value(recognitionImageScopeContextKey{}).(RecognitionImageScopeInputV1)
	return v, ok && v.Version == RecognitionImageScopeVersionV1
}

func ParseRecognitionImageScopeV1(raw string) (*RecognitionImageScopeV1, error) {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("image scope envelope is missing")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sanitizeInitialReadModelJSONV1(raw[start:end+1])), &envelope); err != nil {
		return nil, err
	}
	data, exists := envelope["scope"]
	if !exists {
		return nil, nil
	}
	var scope RecognitionImageScopeV1
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return nil, err
	}
	if scope.Version != RecognitionImageScopeVersionV1 || (scope.Source != "explicit" && scope.Source != "inferred") || strings.TrimSpace(scope.Basis) == "" || len(scope.SelectedOrders) == 0 {
		return nil, fmt.Errorf("image scope identity is invalid")
	}
	return &scope, nil
}

// 全部观察与原响应逐项对照；选中精确集合不能代替原始候选完整性。
func ValidateRecognitionImageScopeSourceV1(plan RecognitionLayoutPlanV2, raw string) error {
	if plan.Scope == nil {
		return nil
	}
	entries, err := CanonicalRecognitionLayoutInitialReadEntriesV1(raw)
	if err != nil || len(entries) != len(plan.ObservedTargets) {
		return fmt.Errorf("image scope observation exact-set drifted")
	}
	for _, target := range plan.ObservedTargets {
		if target.ManifestOrder < 1 || target.ManifestOrder > len(entries) || target.OriginalRegion == nil {
			return fmt.Errorf("image scope observation order is invalid")
		}
		var entry RecognitionLayoutManifestTargetV2
		if json.Unmarshal(entries[target.ManifestOrder-1], &entry) != nil || entry.ManifestOrder != target.ManifestOrder || entry.Region != *target.OriginalRegion || !slices.Equal(entry.SourceNumberPath, target.SourceNumberPath) || entry.DisplayLabel != target.DisplayLabel || !slices.Equal(entry.SourceSectionPath, target.SourceSectionPath) || entry.SourceSectionLabel != target.SourceSectionLabel {
			return fmt.Errorf("image scope observation source drifted")
		}
	}
	return nil
}

func validateRecognitionImageScopeV1(plan RecognitionLayoutPlanV2) error {
	if plan.Scope == nil {
		if plan.ScopeInput != nil || len(plan.ObservedTargets) != 0 {
			return fmt.Errorf("legacy plan contains image scope fields")
		}
		return nil
	}
	if plan.InitialReadMode != RecognitionLayoutManifestWithContentV1 || plan.ScopeInput == nil || plan.ScopeInput.Version != RecognitionImageScopeVersionV1 || plan.Scope.Version != RecognitionImageScopeVersionV1 || len(plan.ObservedTargets) < len(plan.Targets) || len(plan.ObservedTargets) > recognitionLayoutTargetLimitV2 {
		return fmt.Errorf("image scope plan identity is invalid")
	}
	byOrder := make(map[int]RecognitionLayoutTargetV2, len(plan.ObservedTargets))
	for _, candidate := range plan.ObservedTargets {
		if candidate.ManifestOrder < 1 || candidate.ManifestOrder > len(plan.ObservedTargets) || byOrder[candidate.ManifestOrder].TargetID != "" {
			return fmt.Errorf("image scope candidate order is invalid")
		}
		byOrder[candidate.ManifestOrder] = candidate
	}
	selected := map[int]bool{}
	for _, order := range plan.Scope.SelectedOrders {
		if selected[order] || byOrder[order].TargetID == "" {
			return fmt.Errorf("image scope selection is invalid")
		}
		selected[order] = true
	}
	if len(selected) != len(plan.Targets) {
		return fmt.Errorf("image scope selected exact-set drifted")
	}
	for _, target := range plan.Targets {
		if !selected[target.ManifestOrder] || !reflect.DeepEqual(target, byOrder[target.ManifestOrder]) {
			return fmt.Errorf("image scope execution target drifted")
		}
	}
	accounted := map[int]bool{}
	for order := range selected {
		accounted[order] = true
	}
	for _, exclusion := range plan.Scope.Excluded {
		if accounted[exclusion.Order] || byOrder[exclusion.Order].TargetID == "" || strings.TrimSpace(exclusion.Reason) == "" {
			return fmt.Errorf("image scope exclusion is invalid")
		}
		accounted[exclusion.Order] = true
	}
	if len(accounted) != len(byOrder) {
		return fmt.Errorf("image scope does not account for all candidates")
	}
	for _, dependency := range plan.Scope.Dependencies {
		if strings.TrimSpace(dependency.Text) == "" || len(dependency.ForOrders) == 0 || dependency.Region.X < 0 || dependency.Region.Y < 0 || dependency.Region.Width <= 0 || dependency.Region.Height <= 0 {
			return fmt.Errorf("image scope dependency is invalid")
		}
		seen := map[int]bool{}
		for _, order := range dependency.ForOrders {
			if !selected[order] || seen[order] {
				return fmt.Errorf("image scope dependency target is invalid")
			}
			seen[order] = true
		}
	}
	for _, gap := range plan.Scope.Gaps {
		if !selected[gap.Order] || (gap.Kind != "quotation" && gap.Kind != "instruction" && gap.Kind != "condition" && gap.Kind != "handwriting") || strings.TrimSpace(gap.Before+gap.After) == "" {
			return fmt.Errorf("image source gap is invalid")
		}
	}
	return nil
}
