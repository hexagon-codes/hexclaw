package usecase

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 只在同页同父题内合并重复检验，保留各小题独有说明及全部来源。
func consolidateGradingReportSections(questions []RecognizedQuestion, sections []k12.TeachingReportDeepSection) []k12.TeachingReportDeepSection {
	groups := map[string]string{}
	order := map[string]int{}
	for index, q := range questions {
		order[q.ProblemID] = index
		key := "problem:" + q.ProblemID
		if q.PageAssetID != "" {
			if q.ParentProblemID != "" {
				key = q.PageAssetID + "\x00parent:" + q.ParentProblemID
			} else if len(q.SourceNumberPath) > 1 {
				if source := reportSharedPrintedSource(q); source != "" {
					section := strings.Join(q.SourceSectionPath, "\x00")
					if section == "" {
						section = q.SourceSectionLabel
					}
					key = q.PageAssetID + "\x00section:" + section + "\x00path:" + strings.Join(q.SourceNumberPath[:len(q.SourceNumberPath)-1], "\x00") + "\x00source:" + source
				}
			}
		}
		groups[q.ProblemID] = key
	}
	position := func(section k12.TeachingReportDeepSection) int {
		first := len(questions)
		for _, id := range section.ProblemIDs {
			if index, ok := order[id]; ok && index < first {
				first = index
			}
		}
		return first
	}
	sections = slices.Clone(sections)
	slices.SortStableFunc(sections, func(a, b k12.TeachingReportDeepSection) int { return position(a) - position(b) })
	sectionGroup := func(section k12.TeachingReportDeepSection) string {
		key := ""
		for _, id := range section.ProblemIDs {
			group := groups[id]
			if group == "" || key != "" && key != group {
				return ""
			}
			key = group
		}
		return key
	}
	check := func(title string) bool {
		// 带对象或独有条件的标题保留，不凭“检查”子串删除其语义。
		return slices.Contains([]string{"检查", "检验", "验算", "回代检查", "代回检查", "代入检查", "用原条件检查", "用已用空间检查", "检查结果", "检验结果", "代入原条件检验"}, strings.TrimSpace(title))
	}
	var out []k12.TeachingReportDeepSection
	seenDiagrams := map[string]bool{}
	for _, section := range sections {
		group := sectionGroup(section)
		diagrams := section.Diagrams[:0]
		for _, diagram := range section.Diagrams {
			key := group + "\x00" + diagram.AssetDigest
			if group == "" || !seenDiagrams[key] {
				diagrams = append(diagrams, diagram)
				seenDiagrams[key] = true
			}
		}
		section.Diagrams = diagrams
		merged := false
		if group != "" && check(section.Title) {
			for i := range out {
				if sectionGroup(out[i]) == group && check(out[i].Title) && reportChecksShareEquation(out[i], section) {
					out[i] = mergeReportCheckSections(out[i], section)
					merged = true
					break
				}
			}
		}
		if !merged {
			out = append(out, section)
		}
	}
	// 混合讲解中的同源算式也可共享；独有文字及方法直接引用的算式保持。
	type equationPosition struct{ section, block int }
	equations := map[string]equationPosition{}
	for i := range out {
		group := sectionGroup(out[i])
		if group == "" || len(out[i].Blocks) < 2 {
			continue
		}
		referenced := map[string]bool{}
		for _, note := range out[i].MethodNotes {
			for _, id := range note.BlockIDs {
				referenced[id] = true
			}
		}
		kept := out[i].Blocks[:0]
		for _, block := range out[i].Blocks {
			if block.LaTeX != "" && !referenced[block.ID] {
				key := group + "\x00" + reportExplanationKey(block)
				if prior, exists := equations[key]; exists && prior.section != i {
					target := &out[prior.section].Blocks[prior.block]
					for _, origin := range block.Origins {
						if !slices.Contains(target.Origins, origin) {
							target.Origins = append(target.Origins, origin)
						}
						if !slices.Contains(out[prior.section].ProblemIDs, origin.ProblemID) {
							out[prior.section].ProblemIDs = append(out[prior.section].ProblemIDs, origin.ProblemID)
						}
					}
					continue
				}
				equations[key] = equationPosition{i, len(kept)}
			}
			kept = append(kept, block)
		}
		out[i].Blocks = kept
	}
	// 只含共享算式的后组可能已清空；其来源已并入保留块，不再留下无内容的讲解组。
	out = slices.DeleteFunc(out, func(section k12.TeachingReportDeepSection) bool {
		return len(section.Blocks) == 0 && len(section.Diagrams) == 0
	})
	return out
}

// 纸面题号可能在不同分区重复，只有冻结的共享材料才能补充父题身份。
func reportSharedPrintedSource(q RecognizedQuestion) string {
	source := q.ImageSourceContext
	if source == nil || source.Version != k12.RecognitionImageScopeVersionV1 || source.ScopeDigest == "" {
		return ""
	}
	var materials []string
	for _, material := range source.Materials {
		if len(material.ForOrders) < 2 {
			continue
		}
		material.ForOrders = slices.Clone(material.ForOrders)
		slices.Sort(material.ForOrders)
		raw, err := json.Marshal(material)
		if err != nil {
			return ""
		}
		materials = append(materials, string(raw))
	}
	if len(materials) == 0 {
		return ""
	}
	slices.Sort(materials)
	return source.ScopeDigest + "\x00" + strings.Join(materials, "\x00")
}

var reportDisplayUnitParentheses = regexp.MustCompile(`([0-9])[(（]([\p{L}]+)[)）]$`)

func reportExplanationKey(block k12.TeachingReportExplanationBlock) string {
	if block.LaTeX != "" {
		readable, _ := mathtext.ProjectReadable("$" + strings.ReplaceAll(block.LaTeX, `\ `, " ") + "$")
		return "equation:" + reportDisplayUnitParentheses.ReplaceAllString(strings.Join(strings.Fields(readable), ""), "${1}${2}")
	}
	if block.Table != nil {
		raw, _ := json.Marshal(block.Table)
		return "table:" + string(raw)
	}
	return string(block.Purpose) + ":text:" + strings.TrimSpace(block.Text)
}

func reportChecksShareEquation(a, b k12.TeachingReportDeepSection) bool {
	equations := map[string]bool{}
	for _, block := range a.Blocks {
		if block.LaTeX != "" {
			equations[reportExplanationKey(block)] = true
		}
	}
	for _, block := range b.Blocks {
		if block.LaTeX != "" && equations[reportExplanationKey(block)] {
			return true
		}
	}
	return false
}

func mergeReportCheckSections(a, b k12.TeachingReportDeepSection) k12.TeachingReportDeepSection {
	for _, id := range b.ProblemIDs {
		if !slices.Contains(a.ProblemIDs, id) {
			a.ProblemIDs = append(a.ProblemIDs, id)
		}
	}
	byContent, redirects := map[string]int{}, map[string]string{}
	for i, block := range a.Blocks {
		byContent[reportExplanationKey(block)] = i
	}
	for _, block := range b.Blocks {
		key := reportExplanationKey(block)
		if i, exists := byContent[key]; exists {
			kept := &a.Blocks[i]
			redirects[block.ID] = kept.ID
			for _, origin := range block.Origins {
				found := false
				for _, prior := range kept.Origins {
					found = found || prior.ProblemID == origin.ProblemID
				}
				if !found {
					kept.Origins = append(kept.Origins, origin)
				}
			}
		} else {
			byContent[key] = len(a.Blocks)
			a.Blocks = append(a.Blocks, block)
		}
	}
	for _, note := range b.MethodNotes {
		for i, id := range note.BlockIDs {
			if kept := redirects[id]; kept != "" {
				note.BlockIDs[i] = kept
			}
		}
		a.MethodNotes = append(a.MethodNotes, note)
	}
	a.Diagrams = append(a.Diagrams, b.Diagrams...)
	return a
}
