package engine

import (
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
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
	if cards, cardScope := verificationCardPoolScope(problem); cardScope {
		left, lok := parseVerificationCardFacts(computed, cards)
		right, rok := parseVerificationCardFacts(candidate, cards)
		if !lok || !rok {
			return false, false, true
		}
		expected := verificationCardPoolFacts(cards)
		return verificationCardFactsEqual(left, right) && verificationCardFactsEqual(left, expected), true, true
	}
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

var verificationCardPoolRe = regexp.MustCompile(`(?s)^(.*?)有([一二两三四五六七八九十0-9]+)张(?:数字)?卡片[:：]\s*(` + verificationRationalLiteral + `(?:\s*[、,，]\s*` + verificationRationalLiteral + `)+)\s*。\s*每次选出两张卡片[，,]只有它们上面的数互为倒数[，,]才能配成一组。\s*这些卡片能配成哪几组[？?]\s*剩下的卡片中[，,]哪张再添一张相同的卡片就能配对[？?]\s*哪张无论添什么数字卡片都不能配对[？?]\s*说明理由[。.]?$`)
var verificationCardLabeledRe = regexp.MustCompile(`^配对[:：](.+?)[；;]剩下[:：](.+?)[；;]添相同卡片可配对[:：](.+?)[；;]无论添什么都不能配对[:：](.+)$`)
var verificationCardPairRe = regexp.MustCompile(`^(` + verificationRationalLiteral + `)(?:和|与|↔)(` + verificationRationalLiteral + `)$`)
var verificationCardPairsRe = regexp.MustCompile(`^(?:原有卡片)?能配成([一二两三四五六七八九十0-9]+)组[:：](.+)$`)
var verificationCardSameRe = regexp.MustCompile(`^剩下(.+?)[，,]其中(` + verificationRationalLiteral + `)再添一张(` + verificationRationalLiteral + `)(?:就)?能配对$`)
var verificationCardImpossibleRe = regexp.MustCompile(`^(` + verificationRationalLiteral + `)无论添什么数字卡片都不能配对$`)
var verificationCardZeroReasonRe = regexp.MustCompile(`^0(?:与任何数相乘都得|与任何数相乘都等于|乘任何数都等于)0[，,]不等于1$`)

type verificationCardFacts struct {
	pairs                       [][2]int
	remaining, same, impossible []int
}

func verificationCardCount(text string) (int, bool) {
	if text == "两" {
		return 2, true
	}
	for i, char := range []rune("一二三四五六七八九十") {
		if text == string(char) {
			return i + 1, true
		}
	}
	value, err := strconv.Atoi(text)
	return value, err == nil && value > 0
}

// 卡片池只接纳完整的三个问项；卡片身份由原题的精确有理数确定。
func verificationCardPoolScope(problem string) ([]*big.Rat, bool) {
	if _, current, _, parsed := elementarymath.ParseMathSubproblem(problem); parsed {
		problem = current
	}
	match := verificationCardPoolRe.FindStringSubmatch(strings.TrimSpace(problem))
	if match == nil || strings.ContainsAny(match[1], "？?") || strings.Contains(match[1], "计算") || strings.Contains(match[1], "求") {
		return nil, false
	}
	count, ok := verificationCardCount(match[2])
	values := answerSetSep.Split(match[3], -1)
	if !ok || len(values) != count {
		return nil, false
	}
	var cards []*big.Rat
	for _, value := range values {
		rational, ok := new(big.Rat).SetString(value)
		if !ok || verificationCardIndex(cards, rational) >= 0 {
			return nil, false
		}
		cards = append(cards, rational)
	}
	return cards, true
}

func verificationCardIndex(cards []*big.Rat, value *big.Rat) int {
	for i, card := range cards {
		if card.Cmp(value) == 0 {
			return i
		}
	}
	return -1
}

func verificationCardIndices(text string, cards []*big.Rat) ([]int, bool) {
	if text == "无" {
		return nil, true
	}
	var result []int
	for _, value := range answerSetSep.Split(text, -1) {
		rational, ok := new(big.Rat).SetString(value)
		if !ok {
			return nil, false
		}
		index := verificationCardIndex(cards, rational)
		if index < 0 || slices.Contains(result, index) {
			return nil, false
		}
		result = append(result, index)
	}
	sort.Ints(result)
	return result, len(result) > 0
}

func verificationCardPairs(text string, cards []*big.Rat) ([][2]int, bool) {
	var pairs [][2]int
	for _, pair := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune("；;，,", r) }) {
		match := verificationCardPairRe.FindStringSubmatch(pair)
		if match == nil {
			return nil, false
		}
		left, lok := verificationCardIndices(match[1], cards)
		right, rok := verificationCardIndices(match[2], cards)
		if !lok || !rok {
			return nil, false
		}
		indices := [2]int{left[0], right[0]}
		if indices[0] > indices[1] {
			indices[0], indices[1] = indices[1], indices[0]
		}
		pairs = append(pairs, indices)
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i][0] < pairs[j][0] || pairs[i][0] == pairs[j][0] && pairs[i][1] < pairs[j][1]
	})
	return pairs, len(pairs) > 0
}

// 理由只能是对应配对的完整乘法等式，不能丢弃带有另一结论的尾句。
func verificationCardProductReasons(reason string, pairs [][2]int, cards []*big.Rat) bool {
	if reason == "" {
		return true
	}
	seen := make([]bool, len(pairs))
	for _, equation := range strings.FieldsFunc(reason, func(r rune) bool { return r == '，' || r == ',' }) {
		match := verificationMultiplicationEquationRe.FindStringSubmatch(equation)
		if match == nil {
			return false
		}
		a, aok := new(big.Rat).SetString(match[1])
		b, bok := new(big.Rat).SetString(match[2])
		value, vok := new(big.Rat).SetString(match[3])
		if !aok || !bok || !vok || value.Cmp(big.NewRat(1, 1)) != 0 || new(big.Rat).Mul(a, b).Cmp(value) != 0 {
			return false
		}
		indices := [2]int{verificationCardIndex(cards, a), verificationCardIndex(cards, b)}
		if indices[0] > indices[1] {
			indices[0], indices[1] = indices[1], indices[0]
		}
		index := slices.Index(pairs, indices)
		if index < 0 || seen[index] {
			return false
		}
		seen[index] = true
	}
	return !slices.Contains(seen, false)
}

func parseVerificationCardFacts(answer string, cards []*big.Rat) (verificationCardFacts, bool) {
	answer = strings.Join(strings.Fields(normalizeAnswer(answer)), "")
	answer = strings.TrimPrefix(strings.TrimPrefix(answer, "答案："), "答案:")
	facts := verificationCardFacts{}
	var fields []string
	if labeled := verificationCardLabeledRe.FindStringSubmatch(answer); labeled != nil {
		fields = labeled[1:]
	} else {
		clauses := strings.FieldsFunc(answer, func(r rune) bool { return r == '；' || r == ';' })
		if len(clauses) != 3 {
			return facts, false
		}
		main, reasons := make([]string, 3), make([]string, 3)
		for i, clause := range clauses {
			main[i], reasons[i], _ = strings.Cut(clause, "因为")
			main[i] = strings.TrimRight(main[i], "，,")
		}
		pair := verificationCardPairsRe.FindStringSubmatch(main[0])
		same := verificationCardSameRe.FindStringSubmatch(main[1])
		impossible := verificationCardImpossibleRe.FindStringSubmatch(main[2])
		if pair == nil || same == nil || impossible == nil {
			return facts, false
		}
		count, countOK := verificationCardCount(pair[1])
		pairs, pairsOK := verificationCardPairs(pair[2], cards)
		original, originalOK := verificationCardIndices(same[2], cards)
		added, addedOK := verificationCardIndices(same[3], cards)
		zero, zeroOK := new(big.Rat).SetString(impossible[1])
		if !countOK || !pairsOK || len(pairs) != count || !originalOK || !addedOK || original[0] != added[0] ||
			!zeroOK || zero.Sign() != 0 || !verificationCardProductReasons(reasons[0], pairs, cards) ||
			!verificationCardProductReasons(reasons[1], [][2]int{{original[0], original[0]}}, cards) ||
			(reasons[2] != "" && !verificationCardZeroReasonRe.MatchString(reasons[2])) {
			return facts, false
		}
		fields = []string{pair[2], same[1], same[2], impossible[1]}
	}
	var ok bool
	if facts.pairs, ok = verificationCardPairs(fields[0], cards); !ok {
		return facts, false
	}
	for i, target := range []*[]int{&facts.remaining, &facts.same, &facts.impossible} {
		if *target, ok = verificationCardIndices(fields[i+1], cards); !ok {
			return facts, false
		}
	}
	used := make([]bool, len(cards))
	for _, pair := range facts.pairs {
		if pair[0] == pair[1] || used[pair[0]] || used[pair[1]] {
			return facts, false
		}
		used[pair[0]], used[pair[1]] = true, true
	}
	for i := range cards {
		if used[i] == slices.Contains(facts.remaining, i) {
			return facts, false
		}
	}
	for _, category := range [][]int{facts.same, facts.impossible} {
		for _, index := range category {
			if !slices.Contains(facts.remaining, index) {
				return facts, false
			}
		}
	}
	return facts, true
}

// 精确有理数只核对关系投影的完整性；真实执行与过程审计仍由求解链分别要求。
func verificationCardPoolFacts(cards []*big.Rat) verificationCardFacts {
	var facts verificationCardFacts
	used := make([]bool, len(cards))
	for i, left := range cards {
		for j := i + 1; j < len(cards); j++ {
			if new(big.Rat).Mul(left, cards[j]).Cmp(big.NewRat(1, 1)) == 0 {
				facts.pairs = append(facts.pairs, [2]int{i, j})
				used[i], used[j] = true, true
			}
		}
	}
	for i, card := range cards {
		if used[i] {
			continue
		}
		facts.remaining = append(facts.remaining, i)
		if new(big.Rat).Mul(card, card).Cmp(big.NewRat(1, 1)) == 0 {
			facts.same = append(facts.same, i)
		}
		if card.Sign() == 0 {
			facts.impossible = append(facts.impossible, i)
		}
	}
	return facts
}

func verificationCardFactsEqual(left, right verificationCardFacts) bool {
	return slices.Equal(left.pairs, right.pairs) && slices.Equal(left.remaining, right.remaining) &&
		slices.Equal(left.same, right.same) && slices.Equal(left.impossible, right.impossible)
}
