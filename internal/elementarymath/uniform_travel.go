package elementarymath

import (
	"math/big"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
)

type UniformTravelProblem struct {
	Hours, Distance, Unit string
}

// 主语与计时短语分开验证，修饰时长的词不能被当成人名的一部分。
var uniformTravelQuestion = regexp.MustCompile(`(?s)^([\p{Han}]+?)\s*(?:用时|用了|用|花了)?\s*([0-9零一二两三四五六七八九十].*?)\s*小时(?:走|行走|步行|行驶|骑行)了?\s*(.+?)\s*(千米|公里|米|km|m)\s*[，,]\s*(?:照这样的速度|按相同的速度|按同样的速度)\s*[，,]\s*(?:他|她|它)?每小时(?:走|行走|步行|行驶|骑行)多少\s*(千米|公里|米|km|m)\s*[？?]$`)
var travelActor = regexp.MustCompile(`^(?:小[\p{Han}]|[他她它我]|(?:[\p{Han}]{1,3}同学)|(?:一名|一位|一个)?(?:学生|同学|行人|运动员)|(?:一辆)?(?:汽车|自行车))$`)

func exactTravelActor(s string) bool {
	name := []rune(s)
	return s != "小于" && (travelActor.MatchString(s) || len(name) == 2 && name[0] == name[1])
}
var uniformTravelFractionLayout = regexp.MustCompile(`(^|[^0-9.])\(([0-9]+)\)\s*/\s*\(([0-9]+)\)`)
var mixedFractionBoundary = regexp.MustCompile(`([0-9])((?:\\\(|\\\[|\$)?\\(?:d|t)?frac\s*\{[0-9]+\}\s*\{[0-9]+\})`)
var travelDecimal = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
var travelFraction = regexp.MustCompile(`^(?:([0-9]+|[零一二两三四五六七八九十])(?:\s*又\s*|[\s\p{Zs}]+))?([0-9]+)\s*/\s*([0-9]+)$`)
var vulgarFraction = regexp.MustCompile(`[¼½¾⅐-⅞]`)
var travelWholeDigits = strings.NewReplacer("零", "0", "一", "1", "二", "2", "两", "2", "三", "3", "四", "4", "五", "5", "六", "6", "七", "7", "八", "8", "九", "9", "十", "10")

// SeparateMixedFraction 保留整数与紧随其后的 LaTeX 字面分数之间的边界，供计算副本使用。
func SeparateMixedFraction(source string) string {
	if !IsTravelRateSource(source) {
		return source
	}
	return mixedFractionBoundary.ReplaceAllString(source, "${1} ${2}")
}

// IsTravelRateSource 选择行程题的计算副本与答案版本，精算仍须逐项核实题面。
func IsTravelRateSource(problem string) bool {
	return strings.Contains(problem, "每小时") && (strings.Contains(problem, "照这样的速度") || strings.Contains(problem, "按相同的速度") || strings.Contains(problem, "按同样的速度"))
}

func exactTravelNumber(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if travelDecimal.MatchString(s) {
		return new(big.Rat).SetString(s)
	}
	parts := travelFraction.FindStringSubmatch(s)
	if parts == nil {
		return nil, false
	}
	numerator, _ := new(big.Int).SetString(parts[2], 10)
	denominator, _ := new(big.Int).SetString(parts[3], 10)
	if denominator.Sign() == 0 {
		return nil, false
	}
	if parts[1] != "" {
		if numerator.Cmp(denominator) >= 0 {
			return nil, false
		}
		whole, ok := new(big.Int).SetString(travelWholeDigits.Replace(parts[1]), 10)
		if !ok {
			return nil, false
		}
		numerator.Add(numerator, new(big.Int).Mul(whole, denominator))
	}
	return new(big.Rat).SetFrac(numerator, denominator), true
}

// ParseUniformTravelProblem 只接受自带用时、路程、匀速条件和所求单位的完整主问。
func ParseUniformTravelProblem(question string) (UniformTravelProblem, bool) {
	problem, _, ok := parseUniformTravel(question)
	return problem, ok
}

// 同一段题面提供两种事实，数值用于算速度，印刷时长用于核对原图按几份计数。
func parseUniformTravel(question string) (UniformTravelProblem, string, bool) {
	question, _ = mathtext.ProjectReadable(strings.TrimSpace(question))
	question = vulgarFraction.ReplaceAllStringFunc(question, func(f string) string {
		return " " + strings.ReplaceAll(norm.NFKC.String(f), "⁄", "/")
	})
	question = uniformTravelFractionLayout.ReplaceAllString(question, "${1}${2}/${3}")
	match := uniformTravelQuestion.FindStringSubmatch(question)
	if match == nil || !exactTravelActor(match[1]) || NormalizeUnit(match[4]) != NormalizeUnit(match[5]) {
		return UniformTravelProblem{}, "", false
	}
	hours, hoursOK := exactTravelNumber(match[2])
	distance, distanceOK := exactTravelNumber(match[3])
	if !hoursOK || !distanceOK || hours.Sign() <= 0 || distance.Sign() < 0 {
		return UniformTravelProblem{}, "", false
	}
	return UniformTravelProblem{Hours: hours.RatString(), Distance: distance.RatString(), Unit: match[5]}, strings.TrimSpace(match[2]), true
}
