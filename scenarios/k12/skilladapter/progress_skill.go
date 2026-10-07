package skilladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

// ProgressSkill 只将本轮家长明确的单元修正映射到真实教材目录。
type ProgressSkill struct{ deps usecase.Deps }

func NewProgressSkill(deps usecase.Deps) *ProgressSkill { return &ProgressSkill{deps: deps} }
func (*ProgressSkill) Name() string                     { return "k12_progress" }
func (*ProgressSkill) Match(string) bool                { return false }
func (*ProgressSkill) Description() string {
	return "Update the routed child's mathematics curriculum progress only from an explicit parent correction in the current original user text."
}
func (*ProgressSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("k12_progress",
		"Apply an explicit parent statement that the teacher is teaching unit N, the child is currently studying unit N, or the curriculum progress should be changed to unit N. The routed child, original user statement and real textbook catalog are resolved automatically. Do not call for homework text, quoted examples, hypothetical statements or review questions. This only records curriculum progress, never mastery or completion. Do not provide an agent id or a confirmation flag.",
		&llm.Schema{Type: "object"})
}

func (s *ProgressSkill) Execute(ctx context.Context, _ map[string]any) (*skill.Result, error) {
	agent := skill.RoutedAgentName(ctx)
	if agent == "" || skill.SystemDispatchSource(ctx) != "" {
		return nil, fmt.Errorf("k12_progress: an interactive routed child is required")
	}
	ordinal, ok := explicitProgressUnit(skill.OriginalUserText(ctx))
	if !ok {
		return nil, fmt.Errorf("k12_progress: an explicit current parent progress correction is required")
	}
	catalog, err := s.deps.GetProgressCurriculumCatalog(ctx, s.deps.TextbookOwnerID, agent)
	if err != nil {
		return nil, fmt.Errorf("k12_progress: %w", err)
	}
	unit, ok := uniqueProgressUnit(catalog.Units, ordinal)
	if !ok {
		return nil, fmt.Errorf("k12_progress: the stated unit does not uniquely match an available textbook catalog")
	}
	current, revision, err := s.deps.GetCurriculumProgressState(ctx, agent, "math")
	if err != nil {
		return nil, fmt.Errorf("k12_progress: %w", err)
	}
	selection := k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: catalog.TextbookManifestID,
		Volume: catalog.Volume, UnitID: unit.UnitID, EvidenceSource: "parent_confirmed"}
	// 同单元的明确修正不清空既有选填项，跨单元则不迁移旧课时或页码。
	if current != nil && current.TextbookManifestID == catalog.TextbookManifestID && current.UnitID == unit.UnitID {
		selection.LessonID, selection.PageFrom, selection.PageTo = current.LessonID, current.RequestedPageFrom, current.RequestedPageTo
	}
	progress, err := s.deps.SetConfirmedCurriculumProgress(ctx, usecase.ConfirmedCurriculumProgressRequest{
		OwnerID: s.deps.TextbookOwnerID, AgentName: agent, ExpectedProgressRevision: revision,
		Selection: selection,
	})
	if err != nil {
		return nil, fmt.Errorf("k12_progress: %w", err)
	}
	if progress == nil {
		return nil, fmt.Errorf("k12_progress: curriculum progress was not saved")
	}
	content, err := json.Marshal(struct {
		Subject, Unit, Source string
		Revision              int
	}{progress.Subject, progress.UnitTitle, progress.EvidenceSource, progress.Revision})
	if err != nil {
		return nil, err
	}
	return &skill.Result{Content: string(content), Metadata: map[string]string{
		"k12_progress_source": progress.EvidenceSource, "k12_progress_revision": strconv.Itoa(progress.Revision),
	}}, nil
}

var progressQuotedText = regexp.MustCompile("(?s)```.*?```|“[^”]*”|「[^」]*」|『[^』]*』|\"[^\"]*\"|`[^`]*`")
var progressCorrectionUnit = regexp.MustCompile(`(?:进度\s*(?:改到|改为|改成|调整为|调整到)|改到|改为|改成|老师\s*(?:正在)?(?:讲到|教到)(?:了)?|(?:学校|现在|目前)?\s*(?:正在学(?:到)?|学到))\s*(?:数学\s*)?第\s*([0-9一二三四五六七八九十两]+)\s*单元`)
var progressCatalogOrdinal = regexp.MustCompile(`^\s*(?:第\s*)?([0-9一二三四五六七八九十两]+)\s*(?:单元|[.、])`)
var progressDataSection = regexp.MustCompile(`(?m)^\s*(?:题干|题目|复习题|练习题|假设|假如|假定)\s*[:：]`)

// 修正仅来自独立的家长陈述；引用、题干、假设和复习语句不是进度事实。
func explicitProgressUnit(text string) (int, bool) {
	text = progressQuotedText.ReplaceAllString(text, " ")
	if progressDataSection.MatchString(text) {
		return 0, false
	}
	selected := 0
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("，,。；;\n", r)
	}) {
		blocked := false
		for _, marker := range []string{"如果", "假设", "假如", "假定", "可能", "估计", "预计", "题干", "题目", "例题", "练习", "复习", "吗", "是否", "？", "?", "不是", "没有"} {
			blocked = blocked || strings.Contains(clause, marker)
		}
		if blocked {
			continue
		}
		for _, match := range progressCorrectionUnit.FindAllStringSubmatch(clause, -1) {
			ordinal, ok := progressUnitNumber(match[1])
			if !ok || (selected != 0 && selected != ordinal) {
				return 0, false
			}
			selected = ordinal
		}
	}
	return selected, selected > 0
}

func progressUnitNumber(text string) (int, bool) {
	if n, err := strconv.Atoi(text); err == nil {
		return n, n > 0
	}
	digits := map[rune]int{'一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	runes := []rune(text)
	if len(runes) == 1 {
		if runes[0] == '十' {
			return 10, true
		}
		n, ok := digits[runes[0]]
		return n, ok
	}
	parts := strings.Split(text, "十")
	if len(parts) != 2 {
		return 0, false
	}
	tens, ones := 1, 0
	if parts[0] != "" {
		if len([]rune(parts[0])) != 1 {
			return 0, false
		}
		var ok bool
		tens, ok = digits[[]rune(parts[0])[0]]
		if !ok {
			return 0, false
		}
	}
	if parts[1] != "" {
		if len([]rune(parts[1])) != 1 {
			return 0, false
		}
		var ok bool
		ones, ok = digits[[]rune(parts[1])[0]]
		if !ok {
			return 0, false
		}
	}
	return tens*10 + ones, true
}

func uniqueProgressUnit(units []k12.CurriculumCatalogUnit, ordinal int) (k12.CurriculumCatalogUnit, bool) {
	var selected k12.CurriculumCatalogUnit
	matches, numbered := 0, false
	for _, unit := range units {
		match := progressCatalogOrdinal.FindStringSubmatch(unit.Title)
		if len(match) != 2 {
			continue
		}
		numbered = true
		n, ok := progressUnitNumber(match[1])
		if ok && n == ordinal {
			selected, matches = unit, matches+1
		}
	}
	if numbered {
		return selected, matches == 1 && selected.UnitID != ""
	}
	// 没有印刷编号时，已核验目录的单元顺序是唯一位置依据。
	if ordinal < 1 || ordinal > len(units) || units[ordinal-1].UnitID == "" {
		return selected, false
	}
	return units[ordinal-1], true
}
