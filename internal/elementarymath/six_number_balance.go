// Package elementarymath 提供小学固定题型的纯语法解析，不求解或改写原始读数。
package elementarymath

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	sixNumberBalancePrefix = `^(?:[0-9]+[.．、])?在下列六个数[:：]([0-9]+)[、,，]([0-9]+)[、,，]([0-9]+)[、,，]([0-9]+)[、,，]([0-9]+)[、,，]([0-9]+)中[，,]?划去(?:一个)?数`
	sixNumberBalanceSuffix = `后[，,]?能使其中3个数的和(?:是|为)?另外2个数(?:的)?和的2倍[。.]?$`
)

var (
	sixNumberBalanceLegacyRe = regexp.MustCompile(sixNumberBalancePrefix + `[（(]?[）)]?` + sixNumberBalanceSuffix)
	// 来源语法只额外允许完整空括号后的一个逗号；无括号、非空括号不进入此分支。
	sixNumberBalanceSourceRe = regexp.MustCompile(sixNumberBalancePrefix + `(?:[（(]?[）)]?|[（(][）)][，,])` + sixNumberBalanceSuffix)
)

// ParseSixNumberBalanceLegacy 解析既有六数题型，保留数字原串和原顺序。
// 接受范围沿用原完整语法；多余条件、缺失条件或括号后的逗号均返回 false。
func ParseSixNumberBalanceLegacy(text string) ([6]string, bool) {
	return parseSixNumberBalance(text, sixNumberBalanceLegacyRe)
}

// SixNumberBalanceSourcesEquivalentV1 仅证明完整六数题干的固定条件和同序数字一致。
// 两份文本都须完整符合删一个、三数与二数分组、二倍关系；不比较答案或修改原文。
// 调用者负责限定来源模式及目标归属，无法解析的题干不构成等价证明。
func SixNumberBalanceSourcesEquivalentV1(left, right string) bool {
	leftNumbers, leftOK := parseSixNumberBalance(left, sixNumberBalanceSourceRe)
	rightNumbers, rightOK := parseSixNumberBalance(right, sixNumberBalanceSourceRe)
	return leftOK && rightOK && leftNumbers == rightNumbers
}

func parseSixNumberBalance(text string, pattern *regexp.Regexp) ([6]string, bool) {
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	match := pattern.FindStringSubmatch(compact)
	if len(match) != 7 {
		return [6]string{}, false
	}
	var numbers [6]string
	copy(numbers[:], match[1:])
	return numbers, true
}
