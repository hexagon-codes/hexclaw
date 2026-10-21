package engine

import (
	"regexp"
	"strings"
)

type verificationPairingGroup struct {
	label       string
	left, right []answerQuantity
}

var verificationPairingGroupRe = regexp.MustCompile(`第[一二三四五六七八九十0-9]+组`)
var verificationTableDividerRe = regexp.MustCompile(`^:?-+:?$`)

// verificationPairingScope 以原题双列表格确定全部配对域，不从答案反推题目范围。
func verificationPairingScope(problem string) ([]verificationPairingGroup, bool) {
	if !strings.Contains(problem, "互为倒数") || (!strings.Contains(problem, "连线") && !strings.Contains(problem, "连起来")) {
		return nil, false
	}
	var groups []verificationPairingGroup
	var group *verificationPairingGroup
	table := false
	for _, line := range strings.Split(problem, "\n") {
		line = strings.TrimSpace(normalizeAnswer(line))
		if verificationPairingGroupRe.MatchString(line) && verificationPairingGroupRe.FindString(line) == line {
			groups = append(groups, verificationPairingGroup{label: line})
			group, table = &groups[len(groups)-1], false
			continue
		}
		if line == "" {
			continue
		}
		cells := strings.Split(line, "|")
		if group == nil || len(cells) != 4 || cells[0] != "" || cells[3] != "" {
			if table {
				table = false
			}
			continue
		}
		left, right := strings.TrimSpace(cells[1]), strings.TrimSpace(cells[2])
		if left == "左列" && right == "右列" {
			table = true
			continue
		}
		if verificationTableDividerRe.MatchString(left) && verificationTableDividerRe.MatchString(right) {
			continue
		}
		if !table {
			return nil, false
		}
		lq, lok := completeVerificationQuantity(left)
		rq, rok := completeVerificationQuantity(right)
		if !lok || !rok || pairingQuantityIndex(group.left, lq) >= 0 || pairingQuantityIndex(group.right, rq) >= 0 {
			return nil, false
		}
		group.left, group.right = append(group.left, lq), append(group.right, rq)
	}
	for i, g := range groups {
		if len(g.left) < 2 || len(g.left) != len(g.right) {
			return nil, false
		}
		for j := 0; j < i; j++ {
			if groups[j].label == g.label {
				return nil, false
			}
		}
	}
	return groups, len(groups) > 0
}

func pairingQuantityIndex(values []answerQuantity, value answerQuantity) int {
	for i, candidate := range values {
		if quantitiesEqual(candidate, value) {
			return i
		}
	}
	return -1
}

// parseVerificationPairing 要求每组左右数各使用一次，保留配对关系而非比较无序数字集。
func parseVerificationPairing(answer string, groups []verificationPairingGroup) ([][]int, bool) {
	answer = strings.TrimSpace(normalizeAnswer(answer))
	for _, prefix := range []string{"答案：", "答案:"} {
		answer = strings.TrimSpace(strings.TrimPrefix(answer, prefix))
	}
	indices := verificationPairingGroupRe.FindAllStringIndex(answer, -1)
	if len(indices) != len(groups) || indices[0][0] != 0 {
		return nil, false
	}
	result := make([][]int, len(groups))
	for i, index := range indices {
		groupIndex := -1
		for j, g := range groups {
			if g.label == answer[index[0]:index[1]] {
				groupIndex = j
			}
		}
		if groupIndex < 0 || result[groupIndex] != nil {
			return nil, false
		}
		end := len(answer)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		content := strings.TrimSpace(answer[index[1]:end])
		if !strings.HasPrefix(content, "：") && !strings.HasPrefix(content, ":") {
			return nil, false
		}
		content = strings.TrimSpace(strings.TrimLeft(content, "：:"))
		content = strings.TrimRight(content, "。；; \n")
		pairs := orderedAnswerSeparator.Split(content, -1)
		g := groups[groupIndex]
		if len(pairs) != len(g.left) {
			return nil, false
		}
		mapping := make([]int, len(g.left))
		seenLeft, seenRight := make([]bool, len(g.left)), make([]bool, len(g.right))
		for _, pair := range pairs {
			values := strings.Split(pair, "↔")
			if len(values) != 2 {
				return nil, false
			}
			left, lok := completeVerificationQuantity(strings.TrimSpace(values[0]))
			right, rok := completeVerificationQuantity(strings.TrimSpace(values[1]))
			if !lok || !rok {
				return nil, false
			}
			li, ri := pairingQuantityIndex(g.left, left), pairingQuantityIndex(g.right, right)
			if li < 0 || ri < 0 || seenLeft[li] || seenRight[ri] {
				return nil, false
			}
			seenLeft[li], seenRight[ri], mapping[li] = true, true, ri
		}
		result[groupIndex] = mapping
	}
	return result, true
}

func pairingVerificationAnswersEqual(problem, computed, candidate string) (equal, comparable, scoped bool) {
	groups, scoped := verificationPairingScope(problem)
	if !scoped {
		return false, false, false
	}
	left, lok := parseVerificationPairing(computed, groups)
	right, rok := parseVerificationPairing(candidate, groups)
	if !lok || !rok {
		return false, false, true
	}
	for i := range left {
		for j := range left[i] {
			if left[i][j] != right[i][j] {
				return false, true, true
			}
		}
	}
	return true, true, true
}
