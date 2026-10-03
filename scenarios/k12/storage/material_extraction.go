package k12storage

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var materialQuestionNumber = regexp.MustCompile(`^\s*(?:[-*]\s+)?(?:第([0-9]+)题[：:.、]?|([0-9]+)[、)]\s*|([0-9]+)\.\s+)(.*)$`)
var materialArabicDigit = regexp.MustCompile(`[0-9]`)
var materialArithmetic = regexp.MustCompile(`^[0-9\s.+\-*/×÷()（）=？?]+$`)
var materialNumber = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
var materialBinaryOperator = regexp.MustCompile(`[0-9)）?？]\s*[+\-*/×÷]\s*[+\-]?\s*[0-9(（?？]`)
var materialSeparator = regexp.MustCompile(`^(?:-\s*){3,}$|^(?:\*\s*){3,}$|^(?:_\s*){3,}$`)
var materialBarePage = regexp.MustCompile(`^[0-9]+$`)

func materialLayoutDecoration(text string) bool {
	text = strings.TrimSpace(text)
	return materialSeparator.MatchString(text) || strings.HasPrefix(text, "**") &&
		strings.HasSuffix(text, "**") && len(text) > 4 &&
		materialBarePage.MatchString(strings.TrimSpace(text[2:len(text)-2]))
}

// materialArithmeticStem 区分真实二元算式与 Markdown 装饰、孤立页码。
func materialArithmeticStem(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "**") && strings.HasSuffix(text, "**") && len(text) > 4 {
		text = strings.TrimSpace(text[2 : len(text)-2])
	}
	return text, materialArithmetic.MatchString(text) &&
		len(materialNumber.FindAllString(text, -1)) >= 2 && materialBinaryOperator.MatchString(text)
}

type materialSourceLine struct {
	objects []string
	page    int
	block   string
	line    int
	text    string
	blocked bool
}
type materialQuestionGroup struct {
	number string
	lines  []materialSourceLine
	shared []materialSourceLine
}

// extractMaterialQuestions 依据显式题号归并段落；答案仅在同一完整页或正文范围内按唯一题号关联。
// 图表、公共材料和跨页依赖未闭合时不把局部算式拆成可发布题目。
func extractMaterialQuestions(blocks []MaterialBlock, subject, grade string) ([]MaterialCandidate, bool) {
	var out []MaterialCandidate
	complete := true
	for first := 0; first < len(blocks); {
		last := first + 1
		for last < len(blocks) && blocks[last].Page == blocks[first].Page {
			last++
		}
		items, closed := extractMaterialPage(blocks[first:last], subject, grade)
		out = append(out, items...)
		complete = complete && closed
		first = last
	}
	return out, complete
}

func materialNumberedLine(text string) (string, string, bool) {
	m := materialQuestionNumber.FindStringSubmatch(text)
	if len(m) != 5 {
		return "", text, false
	}
	for _, number := range m[1:4] {
		if number != "" {
			return number, strings.TrimSpace(m[4]), true
		}
	}
	return "", text, false
}

func materialAnswerHeading(heading string) bool {
	heading = strings.TrimSpace(strings.TrimRight(heading, ":："))
	switch strings.ToLower(heading) {
	case "答案", "参考答案", "answers", "answer key", "reference answers":
		return true
	default:
		return false
	}
}

func extractMaterialPage(blocks []MaterialBlock, subject, grade string) ([]MaterialCandidate, bool) {
	return extractMaterialClosedGroups(blocks, subject, grade, true)
}

// extractMaterialClosedGroups 不把页末当作题末；只有显式边界或完整来源末尾才闭合。
func extractMaterialClosedGroups(blocks []MaterialBlock, subject, grade string, sourceComplete bool) ([]MaterialCandidate, bool) {
	var groups, answers []materialQuestionGroup
	var current *materialQuestionGroup
	var shared []materialSourceLine
	inAnswers, complete := false, true
	flush := func() {
		if current != nil {
			if inAnswers {
				answers = append(answers, *current)
			} else {
				groups = append(groups, *current)
			}
			current = nil
		}
	}
	for _, block := range blocks {
		if block.Kind == "link_definition" {
			continue
		}
		for i, raw := range strings.Split(block.Text, "\n") {
			text := strings.TrimSpace(raw)
			if text == "" && (len(block.ObjectIDs) > 0 || block.Incomplete || block.Kind == "table") {
				text = "[Unresolved source object]"
			}
			if text == "" || strings.HasPrefix(text, "<!-- source_page_span=") {
				continue
			}
			if materialSeparator.MatchString(text) || !inAnswers && materialLayoutDecoration(text) {
				flush()
				continue
			}
			heading := strings.TrimSpace(strings.TrimLeft(text, "#"))
			if materialAnswerHeading(heading) {
				flush()
				inAnswers = true
				shared = nil
				continue
			}
			if strings.HasPrefix(text, "#") || block.Kind == "heading" {
				flush()
				inAnswers = false
				shared = nil
				if materialSharedHeading(heading) {
					shared = []materialSourceLine{{page: block.Page, block: block.ID, line: i + 1, text: heading, blocked: block.Incomplete, objects: block.ObjectIDs}}
				}
				// 公共材料不能仅因被写为标题便从事实中消失。
				if materialHasExternalDependency(heading) {
					complete = false
				}
				continue
			}
			line := materialSourceLine{page: block.Page, block: block.ID, line: i + 1, text: text, blocked: block.Kind == "table" || block.Kind == "formula" || block.Incomplete, objects: block.ObjectIDs}
			number, body, numbered := materialNumberedLine(text)
			if numbered {
				flush()
				line.text = body
				current = &materialQuestionGroup{number: number, lines: []materialSourceLine{line}, shared: append([]materialSourceLine(nil), shared...)}
				continue
			}
			if current != nil {
				current.lines = append(current.lines, line)
				continue
			}
			if !inAnswers && len(shared) > 0 {
				shared = append(shared, line)
				continue
			}
			if _, arithmetic := materialArithmeticStem(text); !inAnswers && arithmetic {
				if sourceComplete {
					groups = append(groups, materialQuestionGroup{lines: []materialSourceLine{line}})
				} else {
					complete = false
				}
			} else {
				complete = false
			}
		}
	}
	if sourceComplete {
		flush()
	} else if current != nil {
		complete = false
	}
	questionCounts := map[string]int{}
	answerGroups := map[string][]materialQuestionGroup{}
	for _, g := range groups {
		if g.number != "" {
			questionCounts[g.number]++
		}
	}
	for _, a := range answers {
		answerGroups[a.number] = append(answerGroups[a.number], a)
		if questionCounts[a.number] != 1 {
			complete = false
		}
	}
	var out []MaterialCandidate
	for _, g := range groups {
		var texts, sourceIDs, sharedTexts []string
		var visualObjects []string
		var sourceLocations, referenceLocations []MaterialSourceLocation
		blocked := false
		sharedBlocked := false
		sharedHardBlocked := false
		for _, line := range g.shared {
			for _, object := range line.objects {
				visualObjects = materialAppendUnique(visualObjects, object)
			}
			sharedTexts = append(sharedTexts, line.text)
			sourceIDs = materialAppendUnique(sourceIDs, line.block)
			sourceLocations = append(sourceLocations, MaterialSourceLocation{BlockID: line.block, Line: line.line})
			sharedBlocked = sharedBlocked || line.blocked || materialHasExternalDependency(line.text)
			sharedHardBlocked = sharedHardBlocked || line.blocked
		}
		for _, line := range g.lines {
			for _, object := range line.objects {
				visualObjects = materialAppendUnique(visualObjects, object)
			}
			texts = append(texts, line.text)
			sourceIDs = materialAppendUnique(sourceIDs, line.block)
			sourceLocations = append(sourceLocations, MaterialSourceLocation{BlockID: line.block, Line: line.line})
			blocked = blocked || line.blocked
		}
		stem := strings.TrimSpace(strings.Join(texts, "\n"))
		var visualPages []int
		if len(visualObjects) == 0 && materialDiagramDependency(strings.Join(sharedTexts, "\n")+"\n"+stem) {
			for _, line := range append(append([]materialSourceLine(nil), g.shared...), g.lines...) {
				if line.page > 0 && (len(visualPages) == 0 || visualPages[len(visualPages)-1] != line.page) {
					visualPages = append(visualPages, line.page)
				}
			}
		}
		if blocked || (materialHasExternalDependency(stem) && len(visualObjects) == 0 && len(visualPages) == 0) {
			complete = false
			continue
		}
		reference := ""
		for _, marker := range []string{"参考答案：", "答案：", "参考答案:", "答案:"} {
			if at := strings.Index(stem, marker); at >= 0 {
				reference, stem = strings.TrimSpace(stem[at+len(marker):]), strings.TrimSpace(stem[:at])
				break
			}
		}
		arithmeticStem, arithmetic := materialArithmeticStem(stem)
		if arithmetic {
			stem = arithmeticStem
			if strings.Count(stem, "=") > 1 {
				complete = false
				continue
			}
			if at := strings.Index(stem, "="); at >= 0 {
				if rhs := strings.TrimSpace(stem[at+1:]); rhs != "" {
					reference = rhs
				}
				stem = strings.TrimSpace(stem[:at]) + "="
			}
			stem = strings.TrimRight(stem, "?？")
			reference = strings.Trim(reference, "?？ ")
		} else if !((subject == "数学" || subject == "math") && g.number != "" && ((materialArabicDigit.MatchString(stem) && strings.ContainsAny(stem, "?？")) || len(visualObjects) > 0 || len(visualPages) > 0)) {
			complete = false
			continue
		}
		var referenceIDs []string
		if listed := answerGroups[g.number]; len(listed) > 0 {
			if len(listed) != 1 || questionCounts[g.number] != 1 {
				complete = false
				continue
			}
			var parts []string
			for _, line := range listed[0].lines {
				parts = append(parts, line.text)
				referenceIDs = materialAppendUnique(referenceIDs, line.block)
				referenceLocations = append(referenceLocations, MaterialSourceLocation{BlockID: line.block, Line: line.line})
				blocked = blocked || line.blocked
			}
			listedReference := strings.TrimSpace(strings.Join(parts, "\n"))
			if reference != "" && reference != listedReference {
				complete = false
				continue
			}
			reference = listedReference
			if blocked || reference == "" || materialHasExternalDependency(reference) {
				complete = false
				continue
			}
		}
		first := g.lines[0]
		var issues []string
		if sharedHardBlocked || (sharedBlocked && len(visualObjects) == 0 && len(visualPages) == 0) {
			complete = false
			issues = []string{"Shared material has unresolved source dependencies"}
		}
		if len(visualPages) > 0 && !materialPDFPagesExclusive(blocks, sourceLocations, visualPages) {
			issues = append(issues, "Source pages contain other questions or answers without reliable image regions")
			complete = false
		}
		out = append(out, MaterialCandidate{VisualPDFPages: visualPages, VisualObjectIDs: visualObjects, ID: fmt.Sprintf("%s:line:%d", first.block, first.line), BlockID: first.block, Line: first.line, Facts: k12.ProblemAssetFacts{Subject: "数学", Stem: stem, SharedMaterial: sharedTexts, AnswerContext: map[string]string{"grade_term": grade}}, ReferenceAnswer: reference, Issues: issues, SourceBlockIDs: sourceIDs, ReferenceBlockIDs: referenceIDs, QuestionNumber: g.number, SourceLocations: sourceLocations, ReferenceLocations: referenceLocations})
	}
	return out, complete
}

func materialAppendUnique(values []string, value string) []string {
	for _, old := range values {
		if old == value {
			return values
		}
	}
	return append(values, value)
}

func materialHasExternalDependency(stem string) bool {
	for _, ref := range []string{"如图", "下图", "上图", "表格", "下表", "上表", "图中", "根据材料", "上述", "上题", "下列", "选项", "![", "<img", "书后", "见答案", "同上", "文档视觉解析（OCR/VLM）"} {
		if strings.Contains(stem, ref) {
			return true
		}
	}
	return false
}

// materialSharedHeading 仅接受明确声明的公共范围，不从普通章节标题猜关联。
func materialSharedHeading(heading string) bool {
	for _, prefix := range []string{"公共材料", "公共题干", "共同条件", "Shared material", "Shared context"} {
		if strings.HasPrefix(heading, prefix) {
			return true
		}
	}
	return false
}

// materialDiagramDependency 只为明确依赖图形的题请求原页，不对普通算式重新识图。
func materialDiagramDependency(text string) bool {
	for _, marker := range []string{"如图", "下图", "上图", "图中", "图示", "左图", "右图", "diagram", "figure"} {
		if strings.Contains(strings.ToLower(text), marker) {
			return true
		}
	}
	return false
}

// materialPDFPagesExclusive 没有精确区域时，仅允许本题及其明确公共材料占有的页。
func materialPDFPagesExclusive(blocks []MaterialBlock, locations []MaterialSourceLocation, pages []int) bool {
	owned := map[string]map[int]bool{}
	for _, loc := range locations {
		if owned[loc.BlockID] == nil {
			owned[loc.BlockID] = map[int]bool{}
		}
		owned[loc.BlockID][loc.Line] = true
	}
	selected := map[int]bool{}
	for _, page := range pages {
		selected[page] = true
	}
	for _, b := range blocks {
		if !selected[b.Page] {
			continue
		}
		for i, raw := range strings.Split(b.Text, "\n") {
			text := strings.TrimSpace(raw)
			if text == "" || strings.HasPrefix(text, "<!-- source_page_span=") {
				continue
			}
			heading := strings.TrimSpace(strings.TrimLeft(text, "#"))
			if materialAnswerHeading(heading) || strings.Contains(text, "答案：") || strings.Contains(text, "答案:") {
				return false
			}
			if owned[b.ID][i+1] {
				continue
			}
			if strings.HasPrefix(text, "#") {
				continue
			}
			return false
		}
	}
	return true
}
