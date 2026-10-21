package usecase

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/render"
)

const teachingDiagramSchemaVersion = "teaching_diagram_v1"

// TeachingDiagramContext 由宿主从当前冻结来源及已核验解答构造，不接受模型填写数值或归属。
type TeachingDiagramContext struct {
	ProblemID          string
	InputRevision      int
	ProblemInputDigest string
	SolutionDigest     string
	TargetQuantityID   string
	Quantities         map[string]TeachingDiagramQuantity
	Relations          map[string]TeachingDiagramRelation
}

type TeachingDiagramQuantity struct {
	Value string
	Unit  string
}

// TeachingDiagramRelation 的引用均指向同题可信量目录，顺序表示沿图从左到右的分段。
type TeachingDiagramRelation struct {
	Kind             string
	Whole            string
	Part             string
	Ratio            string
	Segments         []string
	FullJourneyTimes []string
	ElapsedTime      string
	Endpoints        [2]string
	Travelers        [2]string
}

// TeachingDiagramCandidate 只选择已有关系；无法由来源证明的候选不能带入权威量目录。
type TeachingDiagramCandidate struct {
	ProblemInputDigest string
	SolutionDigest     string
	RelationID         string
	Kind               string
}

type TeachingDiagramResult struct {
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	SchemaVersion   string `json:"schema_version,omitempty"`
	RendererVersion string `json:"renderer_version,omitempty"`
	SpecDigest      string `json:"spec_digest,omitempty"`
	AssetDigest     string `json:"asset_digest,omitempty"`
	MIME            string `json:"mime,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	AltText         string `json:"alt_text,omitempty"`
	SVG             []byte `json:"-"`
}

type diagramExactQuantity struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

// BuildTeachingDiagram 校验精确数学关系后生成资产；失败只影响可选图示，不写批改和学习状态。
func BuildTeachingDiagram(ctx TeachingDiagramContext, candidate TeachingDiagramCandidate) TeachingDiagramResult {
	fail := func(status, reason string) TeachingDiagramResult {
		return TeachingDiagramResult{Status: status, Reason: reason}
	}
	if ctx.ProblemID == "" || ctx.InputRevision <= 0 || !diagramDigest(ctx.ProblemInputDigest) || !diagramDigest(ctx.SolutionDigest) || candidate.ProblemInputDigest != ctx.ProblemInputDigest || candidate.SolutionDigest != ctx.SolutionDigest {
		return fail("invalid", "source_mismatch")
	}
	relation, ok := ctx.Relations[candidate.RelationID]
	if !ok || relation.Kind != candidate.Kind {
		return fail("invalid", "relation_mismatch")
	}
	if ctx.TargetQuantityID == "" || ctx.TargetQuantityID != relation.Whole {
		return fail("unsupported", "target_quantity_unsupported")
	}
	if relation.Kind != "fraction_bar" && relation.Kind != "segment_bar" && relation.Kind != "journey_line" {
		return fail("unsupported", "kind_unsupported")
	}
	var quantities []diagramExactQuantity
	quantity := func(id string) (*big.Rat, string, bool) {
		q, exists := ctx.Quantities[id]
		value, valid := new(big.Rat).SetString(q.Value)
		unit := elementarymath.NormalizeUnit(q.Unit)
		if !exists || !valid || value.Sign() <= 0 || unit == "" {
			return nil, "", false
		}
		quantities = append(quantities, diagramExactQuantity{ID: id, Value: value.RatString(), Unit: unit})
		return value, unit, true
	}
	whole, unit, valid := quantity(relation.Whole)
	if !valid {
		return fail("invalid", "whole_unavailable")
	}
	displayUnit := strings.TrimSpace(ctx.Quantities[relation.Whole].Unit)
	label := func(value *big.Rat) string { return diagramNumber(value) + " " + displayUnit }
	input := render.TeachingDiagramInput{Kind: relation.Kind, TotalLabel: "总量 " + label(whole)}
	switch relation.Kind {
	case "fraction_bar":
		part, partUnit, partOK := quantity(relation.Part)
		ratio, ratioOK := new(big.Rat).SetString(relation.Ratio)
		if !partOK || partUnit != unit || !ratioOK || ratio.Sign() <= 0 || ratio.Cmp(big.NewRat(1, 1)) >= 0 || new(big.Rat).Quo(part, whole).Cmp(ratio) != 0 {
			return fail("invalid", "fraction_relation_mismatch")
		}
		if !ratio.Denom().IsInt64() || ratio.Denom().Int64() > 24 {
			return fail("unsupported", "diagram_density")
		}
		parts, highlighted := ratio.Denom().Int64(), ratio.Num().Int64()
		perPart := new(big.Rat).Quo(whole, new(big.Rat).SetInt64(parts))
		fraction, _ := ratio.Float64()
		input.Title = "等分关系"
		input.EqualParts = int(parts)
		input.Segments = []render.TeachingDiagramSegment{{Fraction: fraction, Label: label(part), Highlighted: true}, {Fraction: 1 - fraction, Label: "其余部分"}}
		input.AltText = "总量分成" + big.NewInt(parts).String() + "份，标示部分占" + big.NewInt(highlighted).String() + "份共" + label(part) + "，每份" + label(perPart) + "，总量" + label(whole) + "。"
	case "segment_bar":
		input.Title = "分段数量关系"
		sum := new(big.Rat)
		for _, id := range relation.Segments {
			value, segmentUnit, segmentOK := quantity(id)
			if !segmentOK || segmentUnit != unit {
				return fail("invalid", "segment_unit_mismatch")
			}
			sum.Add(sum, value)
			fraction, _ := new(big.Rat).Quo(value, whole).Float64()
			input.Segments = append(input.Segments, render.TeachingDiagramSegment{Fraction: fraction, Label: label(value), Highlighted: len(input.Segments)%2 == 0})
		}
		if len(input.Segments) < 2 || len(input.Segments) > 6 || sum.Cmp(whole) != 0 {
			return fail("invalid", "segment_total_mismatch")
		}
		input.AltText = "各分段相加等于总量" + label(whole) + "。"
	case "journey_line":
		if elementarymath.UnitCategory(unit) != "length" {
			return fail("invalid", "journey_distance_unit_mismatch")
		}
		if len(relation.FullJourneyTimes) != 2 {
			return fail("unsupported", "journey_shape_unsupported")
		}
		elapsed, timeUnit, elapsedOK := quantity(relation.ElapsedTime)
		if !elapsedOK || elementarymath.UnitCategory(timeUnit) != "time" {
			return fail("invalid", "elapsed_time_unavailable")
		}
		fractions := make([]*big.Rat, 0, 3)
		remaining := big.NewRat(1, 1)
		for _, id := range relation.FullJourneyTimes {
			fullTime, fullUnit, fullOK := quantity(id)
			if !fullOK || fullUnit != timeUnit {
				return fail("invalid", "journey_time_unit_mismatch")
			}
			fraction := new(big.Rat).Quo(elapsed, fullTime)
			remaining.Sub(remaining, fraction)
			fractions = append(fractions, fraction)
		}
		if remaining.Sign() <= 0 {
			return fail("unsupported", "journey_already_met")
		}
		gap, gapUnit, gapOK := quantity(relation.Part)
		if !gapOK || gapUnit != unit || new(big.Rat).Mul(whole, remaining).Cmp(gap) != 0 {
			return fail("invalid", "journey_gap_mismatch")
		}
		// 沿图从左到右依次为第一人路程、未走间隔、第二人路程。
		fractions = []*big.Rat{fractions[0], remaining, fractions[1]}
		parts := big.NewInt(1)
		for i, fraction := range fractions {
			denominator := fraction.Denom()
			gcd := new(big.Int).GCD(nil, nil, parts, denominator)
			parts.Mul(new(big.Int).Quo(parts, gcd), denominator)
			if !parts.IsInt64() || parts.Int64() > 24 {
				return fail("unsupported", "diagram_density")
			}
			value := new(big.Rat).Mul(whole, fraction)
			f, _ := fraction.Float64()
			prefix := []string{"第一人 ", "相距 ", "第二人 "}[i]
			input.Segments = append(input.Segments, render.TeachingDiagramSegment{Fraction: f, Label: prefix + label(value), Highlighted: i != 1, Reverse: i == 2})
		}
		input.Title = "同时出发，相向而行"
		input.Endpoints, input.Travelers = relation.Endpoints, relation.Travelers
		input.EqualParts = int(parts.Int64())
		input.TotalLabel = "全程 " + label(whole)
		input.AltText = input.Segments[0].Label + "，" + input.Segments[2].Label + "，" + input.Segments[1].Label + "，全程" + label(whole) + "。"
	}
	// 规范摘要先于最终产物身份计算，不引入 final digest 与资产 digest 的循环。
	spec, _ := json.Marshal(struct {
		SchemaVersion      string
		ProblemID          string
		InputRevision      int
		ProblemInputDigest string
		SolutionDigest     string
		Relation           TeachingDiagramRelation
		Quantities         []diagramExactQuantity
		Geometry           render.TeachingDiagramInput
	}{teachingDiagramSchemaVersion, ctx.ProblemID, ctx.InputRevision, ctx.ProblemInputDigest, ctx.SolutionDigest, relation, quantities, input})
	asset, err := render.RenderTeachingDiagramSVG(input)
	if err != nil {
		return fail("render_failed", "svg_render_failed")
	}
	return TeachingDiagramResult{Status: "ready", SchemaVersion: teachingDiagramSchemaVersion, RendererVersion: render.TeachingDiagramRendererVersion, SpecDigest: diagramSHA(spec), AssetDigest: diagramSHA(asset.Content), MIME: "image/svg+xml", Width: asset.Width, Height: asset.Height, AltText: input.AltText, SVG: asset.Content}
}

func diagramDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func diagramSHA(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func diagramNumber(value *big.Rat) string {
	if value.IsInt() {
		return value.Num().String()
	}
	// 精确有限小数优先；不以四舍五入后的标签替代数学值。
	for places := 1; places <= 6; places++ {
		candidate := value.FloatString(places)
		parsed, _ := new(big.Rat).SetString(candidate)
		if parsed.Cmp(value) == 0 {
			return strings.TrimRight(strings.TrimRight(candidate, "0"), ".")
		}
	}
	return value.RatString()
}
