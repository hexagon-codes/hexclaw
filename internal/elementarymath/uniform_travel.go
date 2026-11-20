package elementarymath

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

type UniformTravelProblem struct {
	Hours, Distance, Unit string
}

var uniformTravelQuestion = regexp.MustCompile(`^([\p{Han}]{1,16}?)([0-9]+(?:[./][0-9]+)?)小时(?:走|行走|步行|行驶|骑行)了?([0-9]+(?:[./][0-9]+)?)(千米|公里|米|km|m)[，,](?:照这样的速度|按相同的速度|按同样的速度)[，,](?:他|她|它)?每小时(?:走|行走|步行|行驶|骑行)多少(千米|公里|米|km|m)[？?]$`)
var uniformTravelFractionLayout = regexp.MustCompile(`\(([0-9]+)\)\s*/\s*\(([0-9]+)\)`)
var qualifiedTravelDuration = regexp.MustCompile(`至少|至多|最少|最多|约|近|不足|超过|多于|少于|高于|低于|大概|大致|差不多|可能|估计`)

// ParseUniformTravelProblem 只接受自带用时、路程、匀速条件和所求单位的完整主问。
func ParseUniformTravelProblem(question string) (UniformTravelProblem, bool) {
	question = fractionSlotTeXNumbers.ReplaceAllString(question, "$1/$2")
	question = strings.NewReplacer(`\(`, "", `\)`, "", `$`, "").Replace(strings.TrimSpace(question))
	question = uniformTravelFractionLayout.ReplaceAllString(question, "$1/$2")
	if fractionSlotSeparatedNumbers.MatchString(question) {
		return UniformTravelProblem{}, false
	}
	question = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, question)
	match := uniformTravelQuestion.FindStringSubmatch(question)
	if match == nil || strings.ContainsAny(match[1], "不没未非") || qualifiedTravelDuration.MatchString(match[1]) || NormalizeUnit(match[4]) != NormalizeUnit(match[5]) {
		return UniformTravelProblem{}, false
	}
	hours, hoursOK := new(big.Rat).SetString(match[2])
	distance, distanceOK := new(big.Rat).SetString(match[3])
	if !hoursOK || !distanceOK || hours.Sign() <= 0 || distance.Sign() < 0 {
		return UniformTravelProblem{}, false
	}
	return UniformTravelProblem{Hours: match[2], Distance: match[3], Unit: match[5]}, true
}
