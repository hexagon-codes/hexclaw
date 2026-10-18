package elementarymath

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

const partCountSlotPrefix = "k12-part-count-slot-v1\nShared context:\n"
const partCountSlotCurrent = "\nCurrent original:\n"
const partCountSlotCanonical = "\nCurrent canonical:\n"
const partCountSlotTarget = "\nAnswer target:\n"
const partCountSlotInstruction = "Fill only the single printed blank immediately before 份 in the current original. The shared material and the remaining printed text are context, not additional questions to answer. Independently determine the number of parts for this blank. The complete final answer must be that one nonnegative quantity, with 份 optional because it is already printed. A fractional or decimal number of parts is allowed when supported by the problem. Do not answer the parent distance, elapsed time, or subsequent calculation. Do not require the student to rewrite printed text or units outside the blank."

var partCountSlotRe = regexp.MustCompile(`(?:（[ \t\p{Zs}]*）|\([ \t\p{Zs}]*\))[ \t\p{Zs}]*份`)
var partCountAnyBlankRe = regexp.MustCompile(`（[ \t\p{Zs}]*）|\([ \t\p{Zs}]*\)|□|\\square|\\phantom|_{2,}`)

const partCountCanonicalSquare = `(?:\\square|□|\$[ \t\p{Zs}]*(?:\\square|□)[ \t\p{Zs}]*\$)`

var partCountCanonicalBlankRe = regexp.MustCompile(`（[ \t\p{Zs}]*` + partCountCanonicalSquare + `[ \t\p{Zs}]*）|\([ \t\p{Zs}]*` + partCountCanonicalSquare + `[ \t\p{Zs}]*\)`)

// BuildPartCountSlotProblemFromSources 仅把当前题面唯一的份数空位作为作答目标。
// 原文和同源规范文本保留在既有请求内，不把公共材料的路程问题当作该空位。
func BuildPartCountSlotProblemFromSources(shared, current, canonical string) (string, bool) {
	if strings.TrimSpace(shared) == "" || !isPartCountSlotSource(current) {
		return "", false
	}
	currentSignature := partCountSourceSignature(current)
	// 规范文本可用括号内单方框表示原空位；只变换比较副本，空位数与位置仍须一致。
	canonicalComparison := partCountCanonicalBlankRe.ReplaceAllString(canonical, "（）")
	if currentSignature == "" || canonical != "" && (!isPartCountSlotSource(canonicalComparison) || currentSignature != partCountSourceSignature(canonicalComparison)) {
		return "", false
	}
	sharedJSON, _ := json.Marshal(shared)
	currentJSON, _ := json.Marshal(current)
	canonicalJSON, _ := json.Marshal(canonical)
	return partCountSlotPrefix + string(sharedJSON) + partCountSlotCurrent + string(currentJSON) + partCountSlotCanonical + string(canonicalJSON) + partCountSlotTarget + partCountSlotInstruction, true
}

// ParsePartCountSlotProblemSources 只接受完整版本合同，所有阶段读取同一份作答目标。
func ParsePartCountSlotProblemSources(problem string) (shared, current, canonical string, ok bool) {
	if !strings.HasPrefix(problem, partCountSlotPrefix) {
		return "", "", "", false
	}
	sharedJSON, rest, found := strings.Cut(strings.TrimPrefix(problem, partCountSlotPrefix), partCountSlotCurrent)
	if !found || json.Unmarshal([]byte(sharedJSON), &shared) != nil {
		return "", "", "", false
	}
	currentJSON, rest, found := strings.Cut(rest, partCountSlotCanonical)
	if !found || json.Unmarshal([]byte(currentJSON), &current) != nil {
		return "", "", "", false
	}
	canonicalJSON, target, found := strings.Cut(rest, partCountSlotTarget)
	if !found || target != partCountSlotInstruction || json.Unmarshal([]byte(canonicalJSON), &canonical) != nil {
		return "", "", "", false
	}
	_, valid := BuildPartCountSlotProblemFromSources(shared, current, canonical)
	return shared, current, canonical, valid
}

func isPartCountSlotSource(source string) bool {
	return len(partCountSlotRe.FindAllStringIndex(source, -1)) == 1 && len(partCountAnyBlankRe.FindAllStringIndex(source, -1)) == 1
}

func partCountSourceSignature(source string) string {
	source = fractionSlotTeXNumbers.ReplaceAllString(source, "$1/$2")
	source = strings.NewReplacer("$", "", `\(`, "", `\)`, "", `\[`, "", `\]`, "", "（", "(", "）", ")").Replace(source)
	if fractionSlotSeparatedNumbers.MatchString(source) {
		return ""
	}
	source = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, source)
	// 句末句号或省略号只表示印刷续接，内部标点及缺失正文不能因此被忽略。
	return strings.TrimRight(source, "。…")
}
