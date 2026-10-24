package usecase

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// restoreReviewPaperStatement 只从仍能证明当前错题题面的冻结来源恢复材料。
// 来源间存在不能解释的差异时保留原题，不用更新的识题结果替换历史学习事实。
func restoreReviewPaperStatement(item ReviewItem, sources []k12storage.ReviewPaperSource) string {
	if item.IsAccum() || !reviewPaperUsesPhotoSource(item.Fields) || item.Record == nil {
		return item.Title()
	}
	fallback := k12.MistakeQuestionStatement(item.Fields)
	var restored, identity string
	for _, source := range sources {
		q, valid := frozenReviewPaperQuestion(item, source)
		if !valid || !reviewPaperRecordMatchesQuestion(item, q) {
			continue
		}
		candidate := compactReviewPaperQuestionForSource(q, source)
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		key := reviewSourceTextKey(candidate)
		if identity != "" && key != identity {
			return fallback
		}
		if identity == "" {
			restored, identity = candidate, key
		}
	}
	if identity == "" {
		return fallback
	}
	return restored
}

// reviewPaperRecordMatchesQuestion 比较完整题面与已知执行包裹的完整恢复结果。
// 不用子题片段包含关系证明未被人工改题，新增条件或单位改变均不能借旧来源。
func reviewPaperRecordMatchesQuestion(item ReviewItem, q RecognizedQuestion) bool {
	if item.Record == nil || item.IsAccum() || !reviewPaperUsesPhotoSource(item.Fields) {
		return false
	}
	current := reviewSourceTextKey(k12.MistakeQuestionStatement(item.Fields))
	if current == "" {
		return false
	}
	modelProblem, _ := photoQuestionProblemTexts(q)
	legacySource := q.Question
	if q.ImageSourceContext != nil {
		for _, material := range q.ImageSourceContext.Materials {
			legacySource += "\n\n" + material.Text
		}
		for _, recovery := range q.ImageSourceContext.Recoveries {
			if recovery.State == "verified" && recovery.AdoptedText != "" {
				legacySource += "\n\n" + recovery.Gap.Before + recovery.AdoptedText + recovery.Gap.After
			}
		}
	}
	forms := []string{modelProblem, legacySource, compactReviewPaperQuestion(q)}
	if q.ImageSourceContext == nil || len(q.ImageSourceContext.Materials) == 0 && len(q.ImageSourceContext.Recoveries) == 0 {
		forms = append(forms, q.Question, q.CanonicalMarkdown,
			q.RawTranscription+"\n\n"+q.Question, q.RawTranscription+"\n\n"+q.CanonicalMarkdown)
	}
	for _, form := range forms {
		if strings.TrimSpace(form) == "" {
			continue
		}
		recovered := k12.MistakeQuestionStatement(k12.MistakeFields{Question: form, EntrySource: k12.MistakeEntryPhoto})
		if reviewSourceTextKey(recovered) == current {
			return reviewPaperFragmentsAlreadyAdopted(item.Fields.Question, reviewPaperQuestionFragments(q))
		}
	}
	return false
}

func compactReviewPaperQuestion(q RecognizedQuestion) string {
	return k12.CompactQuestionSourceFragments(reviewPaperQuestionFragments(q)...)
}

func compactReviewPaperQuestionForSource(q RecognizedQuestion, source k12storage.ReviewPaperSource) string {
	fragments := reviewPaperQuestionFragments(q)
	if source.ParentProblemID == "" || source.ParentProblemID != q.ParentProblemID ||
		source.ParentPageAssetID != q.PageAssetID || source.ParentKind != "compound_parent" || source.ParentCanonicalVersion != 1 {
		return k12.CompactQuestionSourceFragments(fragments...)
	}
	var parentPath []string
	if json.Unmarshal([]byte(source.ParentSourceNumberPathJSON), &parentPath) != nil || len(parentPath) == 0 || normalizedReviewNumberPath(parentPath) == "" {
		return k12.CompactQuestionSourceFragments(fragments...)
	}
	parentLine, _, _ := strings.Cut(source.ParentStemRaw, "\n")
	parentNumber := reviewMaterialParentNumber.FindStringSubmatchIndex(parentLine)
	if parentNumber == nil || normalizedReviewNumberPath([]string{parentLine[parentNumber[2]:parentNumber[3]]}) != normalizedReviewNumberPath(parentPath[len(parentPath)-1:]) {
		return k12.CompactQuestionSourceFragments(fragments...)
	}
	parentBody := reviewSourceTextKey(parentLine[parentNumber[1]:])
	// Raw当前问项保留原样；其余片段只有完整首行等于首版父题头时去除外层编号。
	for i := 1; i < len(fragments); i++ {
		line, _, _ := strings.Cut(fragments[i], "\n")
		prefix := reviewMaterialParentNumber.FindStringSubmatchIndex(line)
		if prefix != nil && normalizedReviewNumberPath([]string{line[prefix[2]:prefix[3]]}) == normalizedReviewNumberPath(parentPath[len(parentPath)-1:]) &&
			reviewSourceTextKey(line[prefix[1]:]) == parentBody {
			fragments[i] = fragments[i][prefix[1]:]
		}
	}
	return k12.CompactQuestionSourceFragments(fragments...)
}

func reviewPaperQuestionFragments(q RecognizedQuestion) []string {
	fragments := []string{q.RawTranscription, q.Question, q.CanonicalMarkdown}
	if q.ImageSourceContext != nil {
		for _, material := range q.ImageSourceContext.Materials {
			fragments = append(fragments, stripReviewMaterialParentNumber(q, material.Text))
		}
		for _, recovery := range q.ImageSourceContext.Recoveries {
			if recovery.State == "verified" && recovery.AdoptedText != "" {
				fragments = append(fragments, recovery.Gap.Before+recovery.AdoptedText+recovery.Gap.After)
			}
		}
	}
	return fragments
}

// 完整记录匹配后，再核每份待展示原文已经在当前采用题面中出现。
// 精确词边界包含只用于防止新增来源条件，不能独立代替前面的完整题面匹配。
func reviewPaperFragmentsAlreadyAdopted(question string, fragments []string) bool {
	shared, current, _, ok := elementarymath.ParsePartCountSlotProblemSources(question)
	if !ok {
		shared, current, _, ok = elementarymath.ParseFractionSlotProblemSources(question)
	}
	if !ok {
		shared, current, _, ok = elementarymath.ParseMathSubproblem(question)
	}
	if ok {
		question = current + "\n\n" + shared
	}
	adopted := "\x1f" + reviewSourceTextKey(question) + "\x1f"
	for _, fragment := range fragments {
		key := strings.TrimSuffix(reviewSourceTextKey(fragment), "\x1f.")
		if key != "" && !strings.Contains(adopted, "\x1f"+key+"\x1f") {
			return false
		}
	}
	return true
}

var reviewMaterialParentNumber = regexp.MustCompile(`^([0-9０-９]+)[.．、][ \t]*`)

// 只有完整印刷父子题号能证明材料开头的外层题号；括号子题号不在此处移除。
func stripReviewMaterialParentNumber(q RecognizedQuestion, source string) string {
	if len(q.SourceNumberPath) < 2 {
		return source
	}
	match := reviewMaterialParentNumber.FindStringSubmatchIndex(source)
	if match == nil || normalizedReviewNumberPath([]string{source[match[2]:match[3]]}) != normalizedReviewNumberPath(q.SourceNumberPath[:1]) {
		return source
	}
	next, _ := utf8.DecodeRuneInString(source[match[1]:])
	if unicode.IsDigit(next) {
		return source
	}
	return source[match[1]:]
}
