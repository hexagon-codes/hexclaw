package usecase

import (
	"context"
	"log/slog"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var diagramSourceFraction = regexp.MustCompile(`\\(?:frac|dfrac|tfrac)\s*\{([0-9]+)\}\s*\{([0-9]+)\}`)
var diagramSourceUnitText = regexp.MustCompile(`\\(?:text|mathrm)\{([^{}]+)\}`)
var diagramSourceQuantity = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*(` + elementarymath.UnitPattern() + `)`)
var diagramFractionRatio = regexp.MustCompile(`已用(?:空间|容量)(?:占|是)总(?:容量|空间)的([0-9]+/[0-9]+)`)
var diagramFractionUsed = regexp.MustCompile(`(?:正好)?(?:用了|用去|用厂|已用)([0-9]+(?:\.[0-9]+)?)\s*(` + elementarymath.UnitPattern() + `)`)
var diagramJourneyTimes = regexp.MustCompile(`(?:要走|需要|需)([0-9]+(?:\.[0-9]+)?)(分钟|小时|秒)`)
var diagramJourneyElapsed = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)(分钟|小时|秒)后`)
var diagramJourneyGap = regexp.MustCompile(`相距([0-9]+(?:\.[0-9]+)?)\s*(` + elementarymath.UnitPattern() + `)`)
var diagramJourneyNames = regexp.MustCompile(`([\p{Han}]{1,6})从([\p{Han}]{1,6})到([\p{Han}]{1,6})(?:要走|需要|需)`)
var diagramSegmentCount = regexp.MustCompile(`([两二三四五六2-6])(?:段|部分)(?:组成|构成|拼成)`)

func diagramSourceText(value string) string {
	// 只规范化计算副本中的重复TeX转义，冻结原文不改写。
	value = strings.ReplaceAll(value, `\\`, `\`)
	value = diagramSourceFraction.ReplaceAllString(value, "$1/$2")
	value = diagramSourceUnitText.ReplaceAllString(value, "$1")
	value = strings.NewReplacer(`\(`, "", `\)`, "", "$", "", `\,`, "", "／", "/").Replace(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

// 只识别当前支持的明确关系，不用讲法模型重抽数值，也不从孩子作答取题目条件。
func teachingDiagramSourceContext(q RecognizedQuestion, answer string, evidence SolveEvidence) (TeachingDiagramContext, TeachingDiagramCandidate, bool) {
	if !photoEvidenceTrusted(evidence) {
		return TeachingDiagramContext{}, TeachingDiagramCandidate{}, false
	}
	return teachingDiagramQuantityContext(q, answer, evidence.SolverOutputDigest)
}

// 数量关系校验与学生判定分开；输入只取同题冻结题干、解答及其内容身份。
func teachingDiagramQuantityContext(q RecognizedQuestion, answer, solutionDigest string) (TeachingDiagramContext, TeachingDiagramCandidate, bool) {
	ctx := TeachingDiagramContext{ProblemID: q.ProblemID, InputRevision: q.ConfirmedVersion, ProblemInputDigest: q.InputDigest,
		SolutionDigest: solutionDigest, TargetQuantityID: "whole", Quantities: map[string]TeachingDiagramQuantity{}, Relations: map[string]TeachingDiagramRelation{}}
	if ctx.ProblemID == "" || ctx.InputRevision <= 0 || !diagramDigest(ctx.ProblemInputDigest) || !diagramDigest(ctx.SolutionDigest) {
		return ctx, TeachingDiagramCandidate{}, false
	}
	if q.Subject != "数学" || q.parentSourceUnclear {
		return ctx, TeachingDiagramCandidate{}, false
	}
	answerQuantities := diagramSourceQuantity.FindAllStringSubmatch(diagramSourceText(answer), -1)
	if len(answerQuantities) == 0 {
		return ctx, TeachingDiagramCandidate{}, false
	}
	_, printedSource := photoQuestionProblemTexts(q)
	source := diagramSourceText(printedSource)
	if ratio := distinctDiagramSourceMatches(diagramFractionRatio.FindAllStringSubmatch(source, -1)); len(ratio) > 0 {
		used := distinctDiagramSourceMatches(diagramFractionUsed.FindAllStringSubmatch(source, -1))
		if len(ratio) != 1 || len(used) != 1 || elementarymath.UnitCategory(used[0][2]) != "data" {
			return ctx, TeachingDiagramCandidate{}, false
		}
		fraction, ratioOK := new(big.Rat).SetString(ratio[0][1])
		part, partOK := new(big.Rat).SetString(used[0][1])
		if !ratioOK || !partOK || fraction.Sign() <= 0 || part.Sign() <= 0 {
			return ctx, TeachingDiagramCandidate{}, false
		}
		whole, found := diagramAnswerQuantity(answerQuantities, new(big.Rat).Quo(part, fraction), used[0][2])
		if !found {
			return ctx, TeachingDiagramCandidate{}, false
		}
		ctx.Quantities["whole"] = whole
		ctx.Quantities["used"] = TeachingDiagramQuantity{Value: used[0][1], Unit: used[0][2]}
		ctx.Relations["source_relation"] = TeachingDiagramRelation{Kind: "fraction_bar", Whole: "whole", Part: "used", Ratio: ratio[0][1]}
	} else if strings.Contains(source, "同时") && strings.Contains(source, "相向而行") {
		times := diagramJourneyTimes.FindAllStringSubmatch(source, -1)
		elapsed := diagramJourneyElapsed.FindAllStringSubmatch(source, -1)
		gap := diagramJourneyGap.FindAllStringSubmatch(source, -1)
		if len(times) != 2 || len(elapsed) != 1 || len(gap) != 1 || elementarymath.UnitCategory(gap[0][2]) != "length" {
			return ctx, TeachingDiagramCandidate{}, false
		}
		// 多项答案中选出同一已验证关系的全程量，不把路程分量当作全程。
		first, a := new(big.Rat).SetString(times[0][1])
		second, b := new(big.Rat).SetString(times[1][1])
		spent, c := new(big.Rat).SetString(elapsed[0][1])
		remaining, e := new(big.Rat).SetString(gap[0][1])
		if !a || !b || !c || !e || first.Sign() <= 0 || second.Sign() <= 0 || spent.Sign() <= 0 || remaining.Sign() <= 0 || elementarymath.NormalizeUnit(times[0][2]) != elementarymath.NormalizeUnit(times[1][2]) || elementarymath.NormalizeUnit(times[0][2]) != elementarymath.NormalizeUnit(elapsed[0][2]) {
			return ctx, TeachingDiagramCandidate{}, false
		}
		rest := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Add(new(big.Rat).Quo(spent, first), new(big.Rat).Quo(spent, second)))
		if rest.Sign() <= 0 {
			return ctx, TeachingDiagramCandidate{}, false
		}
		whole, found := diagramAnswerQuantity(answerQuantities, new(big.Rat).Quo(remaining, rest), gap[0][2])
		if !found {
			return ctx, TeachingDiagramCandidate{}, false
		}
		ctx.Quantities["whole"] = whole
		ctx.Quantities["first_time"] = TeachingDiagramQuantity{Value: times[0][1], Unit: times[0][2]}
		ctx.Quantities["second_time"] = TeachingDiagramQuantity{Value: times[1][1], Unit: times[1][2]}
		ctx.Quantities["elapsed"] = TeachingDiagramQuantity{Value: elapsed[0][1], Unit: elapsed[0][2]}
		ctx.Quantities["gap"] = TeachingDiagramQuantity{Value: gap[0][1], Unit: gap[0][2]}
		ctx.Relations["source_relation"] = TeachingDiagramRelation{Kind: "journey_line", Whole: "whole", Part: "gap", FullJourneyTimes: []string{"first_time", "second_time"}, ElapsedTime: "elapsed"}
		if names := diagramJourneyNames.FindAllStringSubmatch(source, -1); len(names) == 2 && names[0][2] == names[1][3] && names[0][3] == names[1][2] {
			relation := ctx.Relations["source_relation"]
			relation.Endpoints = [2]string{names[0][2], names[0][3]}
			relation.Travelers = [2]string{names[0][1], names[1][1]}
			ctx.Relations["source_relation"] = relation
		}
	} else if counts := diagramSegmentCount.FindAllStringSubmatch(source, -1); len(counts) == 1 &&
		(strings.Contains(source, "全长") || strings.Contains(source, "总长") || strings.Contains(source, "总重") || strings.Contains(source, "总质量")) {
		count := map[string]int{"两": 2, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6}[counts[0][1]]
		if parsed, err := strconv.Atoi(counts[0][1]); err == nil {
			count = parsed
		}
		var amounts [][]string
		for _, amount := range diagramSourceQuantity.FindAllStringSubmatch(source, -1) {
			if category := elementarymath.UnitCategory(amount[2]); category == "length" || category == "mass" {
				amounts = append(amounts, amount)
			}
		}
		if len(amounts) != count {
			return ctx, TeachingDiagramCandidate{}, false
		}
		sum := new(big.Rat)
		unit := amounts[0][2]
		if category := elementarymath.UnitCategory(unit); category != "length" && category != "mass" {
			return ctx, TeachingDiagramCandidate{}, false
		}
		segments := make([]string, 0, count)
		for i, amount := range amounts {
			number, ok := new(big.Rat).SetString(amount[1])
			if !ok || number.Sign() <= 0 || elementarymath.NormalizeUnit(amount[2]) != elementarymath.NormalizeUnit(unit) {
				return ctx, TeachingDiagramCandidate{}, false
			}
			sum.Add(sum, number)
			id := "part_" + strconv.Itoa(i+1)
			segments = append(segments, id)
			ctx.Quantities[id] = TeachingDiagramQuantity{Value: amount[1], Unit: amount[2]}
		}
		whole, found := diagramAnswerQuantity(answerQuantities, sum, unit)
		if !found {
			return ctx, TeachingDiagramCandidate{}, false
		}
		ctx.Quantities["whole"] = whole
		ctx.Relations["source_relation"] = TeachingDiagramRelation{Kind: "segment_bar", Whole: "whole", Segments: segments}
	} else {
		return ctx, TeachingDiagramCandidate{}, false
	}
	return ctx, TeachingDiagramCandidate{ProblemInputDigest: ctx.ProblemInputDigest, SolutionDigest: ctx.SolutionDigest, RelationID: "source_relation", Kind: ctx.Relations["source_relation"].Kind}, true
}

// 同题共享材料可能重复呈现同一条件；只合并完全相同的捕获量，冲突量仍分别保留。
func distinctDiagramSourceMatches(matches [][]string) [][]string {
	seen := map[string]bool{}
	var out [][]string
	for _, match := range matches {
		key := strings.Join(match[1:], "\x00")
		if !seen[key] {
			seen[key] = true
			out = append(out, match)
		}
	}
	return out
}

func diagramAnswerQuantity(values [][]string, expected *big.Rat, unit string) (TeachingDiagramQuantity, bool) {
	for _, value := range values {
		if elementarymath.NormalizeUnit(value[2]) != elementarymath.NormalizeUnit(unit) {
			continue
		}
		number, ok := new(big.Rat).SetString(value[1])
		if ok && number.Cmp(expected) == 0 {
			return TeachingDiagramQuantity{Value: value[1], Unit: value[2]}, true
		}
	}
	return TeachingDiagramQuantity{}, false
}

func (d Deps) attachPhotoTeachingDiagram(ctx context.Context, agent string, q RecognizedQuestion, item PhotoGradeItem) PhotoGradeItem {
	if item.ParentGuide == nil {
		return item
	}
	guide := *item.ParentGuide
	// 资产只由宿主构造，模型候选或旧调用不能提供资产引用。
	guide.TeachingDiagrams = nil
	item.ParentGuide = &guide
	if d.Records == nil || q.PageAssetID == "" {
		return item
	}
	evidence := item.Solve.Evidence
	if item.Grade.Solution != "" {
		evidence = item.Grade.Evidence
	}
	source, candidate, supported := teachingDiagramSourceContext(q, guide.Answer, evidence)
	if !supported {
		return item
	}
	result := BuildTeachingDiagram(source, candidate)
	if result.Status != "ready" {
		slog.DebugContext(ctx, "Teaching diagram omitted", "problem_id", q.ProblemID, "status", result.Status, "reason", result.Reason)
		return item
	}
	owner, err := d.Records.ReadyPageAssetOwnerForAgent(ctx, agent, q.PageAssetID)
	if err != nil {
		return item
	}
	assetID, err := d.Records.PutTeachingDiagramAsset(ctx, owner, agent, result.SVG, d.now())
	if err != nil {
		slog.WarnContext(ctx, "Teaching diagram asset unavailable", "problem_id", q.ProblemID, "err", err)
		return item
	}
	guide.TeachingDiagrams = []k12.TeachingDiagramAsset{{AssetID: assetID, AssetDigest: result.AssetDigest, SpecDigest: result.SpecDigest,
		RendererVersion: result.RendererVersion, ProblemID: source.ProblemID, InputRevision: source.InputRevision,
		ProblemInputDigest: source.ProblemInputDigest, SolutionDigest: source.SolutionDigest, MIME: result.MIME,
		Width: result.Width, Height: result.Height, AltText: result.AltText}}
	return item
}
