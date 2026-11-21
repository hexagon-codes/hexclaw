package elementarymath

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
)

// TravelWorksheet 是由图示、四个印刷空和带括号的乘法等式共同确定的份数题。
type TravelWorksheet struct {
	UniformTravelProblem
	Parts, HourParts, Reciprocal, PartDistance, Rate string
}

var travelWorksheetNumber = regexp.MustCompile(`^[0-9]+[.、]\s*([小他她一])`)
var travelBoxFraction = regexp.MustCompile(`\\(?:d|t)?frac\s*\{\s*(□|\\square|\\Box|[0-9]+)\s*\}\s*\{\s*(□|\\square|\\Box|[0-9]+)\s*\}`)
var travelEmptyFractionText = regexp.MustCompile(`(?:\(\s*□\s*\)|□)\s*/\s*(?:\(\s*□\s*\)|□)`)
var travelDiagramCaption = regexp.MustCompile(`^(?:线段|示意)?图(?:示|中)?(?:文字|标注)?:`)
var travelDiagramMeasurement = regexp.MustCompile(`^([0-9]+(?:/[0-9]+)?)小时(?:行走|走|步行|行驶|骑行)了?(\?|[0-9]+)(km|m|米)`)
var figureConclusion = regexp.MustCompile(`^(?:(?:从|由|根据)(?:上图|图中|图示|线段图)(?:中)?(?:可以|可|能)?(?:看出|得出|得到|知道|知|见|得):?)?$`)
var travelPartCountBlanks = regexp.MustCompile(`^已知(?:\(\)|□)份(?:是|对应)([0-9]+)(km|米|m)[,。](?:可以)?先求(?:出)?(?:这样的)?(?:1|一(?:\(\)|□))份(?:是)?多少[,。](?:也就是|即)([0-9]+)÷([0-9]+)=([0-9]+)×([^;。]+)[;。]再求(?:出)?(?:这样的)?一?(?:\(\)|□)份(?:是)?多少[,。](?:也就是|即)([0-9]+)×([^×。]+)×([0-9]+)。?$`)
var travelWorksheetPunctuation = strings.NewReplacer("（", "(", "）", ")", "：", ":", "；", ";", "，", ",", "？", "?", "＝", "=", "“", "\"",
	"”", "\"", "这样的一1份", "这样的1份", "这样的一份", "这样的1份", "公里", "km", "千米", "km")

// TravelFractionBlankLayout 将两个空框组成的分式投影为一个作答槽，已有数值的分框保留。
func TravelFractionBlankLayout(source string) string {
	source = travelBoxFraction.ReplaceAllStringFunc(source, func(raw string) string {
		if !strings.ContainsAny(raw, "□") && !strings.Contains(raw, `\square`) && !strings.Contains(raw, `\Box`) {
			return raw
		}
		parts := travelBoxFraction.FindStringSubmatch(raw)
		return strings.NewReplacer(`\square`, "□", `\Box`, "□").Replace(parts[1] + "/" + parts[2])
	})
	return travelEmptyFractionText.ReplaceAllString(source, "()")
}

// TravelQuestionPrefix 只取带完整匀速条件和问号的印刷主问，附加算式另行核实。
func TravelQuestionPrefix(source string) (string, UniformTravelProblem, bool) {
	source = travelWorksheetNumber.ReplaceAllString(strings.TrimSpace(source), "$1")
	end := strings.IndexAny(source, "？?")
	if end < 0 {
		return "", UniformTravelProblem{}, false
	}
	main := source[:end] + "？"
	travel, ok := ParseUniformTravelProblem(main)
	return main, travel, ok
}

func ParseTravelWorksheet(source string) (TravelWorksheet, bool) {
	if !IsTravelRateSource(source) {
		return TravelWorksheet{}, false
	}
	// 分子、分母都是空框时要求填写一个分数；任一侧已有数值时继续保留。
	source = TravelFractionBlankLayout(source)
	source, _ = mathtext.ProjectReadable(source)
	end := strings.IndexAny(source, "？?")
	_, primary, ok := TravelQuestionPrefix(source)
	if !ok {
		return TravelWorksheet{}, false
	}
	time, _ := new(big.Rat).SetString(primary.Hours)
	distance, _ := new(big.Rat).SetString(primary.Distance)
	if !distance.IsInt() || distance.Sign() <= 0 || time.IsInt() || time.Num().Cmp(time.Denom()) >= 0 {
		return TravelWorksheet{}, false
	}
	p, q, d := time.Num().String(), time.Denom().String(), distance.RatString()
	part := new(big.Rat).Quo(distance, new(big.Rat).SetInt(time.Num()))
	rate := new(big.Rat).Quo(distance, time)
	unit := travelWorksheetPunctuation.Replace(primary.Unit)
	tail := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, travelWorksheetPunctuation.Replace(source[end:]))
	tail = strings.ReplaceAll(tail, "(□)", "()")
	_, tail, _ = strings.Cut(tail, "?")
	tail = travelDiagramCaption.ReplaceAllString(tail, "")
	// 以“已知……份”的实际问项分界。图后可以有承接词，也可以直接开始提问。
	start := strings.Index(tail, "已知")
	if start < 0 || !travelDiagramMatches(tail[:start], time.RatString(), q, d, unit) {
		return TravelWorksheet{}, false
	}
	blanks, tail, ok := strings.Cut(tail[start:], "由此推算出:")
	slots := travelPartCountBlanks.FindStringSubmatch(blanks)
	if !ok || slots == nil || slots[1] != d || slots[2] != unit || slots[3] != d || slots[4] != p || slots[5] != d || slots[7] != d || slots[9] != q {
		return TravelWorksheet{}, false
	}
	for _, factor := range []string{slots[6], slots[8]} {
		// 求的是除以 p 对应的完整乘数。若分框内已有印刷数字，须与倒数逐位相符。
		if factor != "()" && factor != "□" && factor != "□/"+p && factor != "1/□" {
			return TravelWorksheet{}, false
		}
	}
	equation := fmt.Sprintf("%s×1/%s×%s=%s×(1/%s×%s)", d, p, q, d, p, q)
	chain := fmt.Sprintf("%s÷%s=%s÷%s×%s=%s=%s×%s/%s", d, time.RatString(), d, p, q, equation, d, q, p)
	// 排版器可能给作为除数的整个分数加括号，数学关系不变。
	tail = strings.Replace(tail, d+"÷("+time.RatString()+")=", d+"÷"+time.RatString()+"=", 1)
	reason := fmt.Sprintf("(%s)。推算过程中\"%s\"的依据是()", unit, equation)
	tail = strings.TrimRight(tail, "。.")
	if tail != chain+reason && tail != chain+"="+rate.RatString()+reason {
		return TravelWorksheet{}, false
	}
	return TravelWorksheet{primary, p, q, new(big.Rat).Inv(new(big.Rat).SetInt(time.Num())).RatString(), part.RatString(), rate.RatString()}, true
}

func travelDiagramMatches(diagram, hours, hourParts, distance, unit string) bool {
	type measurement struct{ time, distance string }
	expected := map[measurement]bool{{"1", "?"}: true, {"1/" + hourParts, "?"}: true, {hours, distance}: true}
	for len(expected) != 0 {
		parts := travelDiagramMeasurement.FindStringSubmatch(diagram)
		if parts == nil || parts[3] != unit {
			return false
		}
		m := measurement{parts[1], parts[2]}
		if !expected[m] {
			return false
		}
		delete(expected, m)
		diagram = strings.TrimLeft(diagram[len(parts[0]):], ";。")
	}
	return figureConclusion.MatchString(diagram)
}

func (s TravelWorksheet) Answer() string {
	return fmt.Sprintf("每小时走%s%s。两步算式中的份数和乘数依次为 %s、%s、%s、%s。等式的依据是乘法结合律。", s.Rate, s.Unit, s.Parts, s.Reciprocal, s.HourParts, s.Reciprocal)
}

func (s TravelWorksheet) Worked() string {
	steps := []string{
		fmt.Sprintf("把1小时平均分成%[1]s份，每份是1/%[1]s小时。已知%s小时对应%s份，走了%s%s。", s.HourParts, s.Hours, s.Parts, s.Distance, s.Unit),
		fmt.Sprintf("每份走%s÷%s=%s%s。除以%s等于乘它的倒数%s。", s.Distance, s.Parts, s.PartDistance, s.Unit, s.Parts, s.Reciprocal),
		fmt.Sprintf("乘数要写成分数时，分子是1，分母是%[1]s。若题面已印出分母%[1]s，就只在分子格里填1。", s.Parts),
		fmt.Sprintf("1小时有%s份，每小时走%s×%s=%s%s。把两步合起来是%s×%s×%s。", s.HourParts, s.PartDistance, s.HourParts, s.Rate, s.Unit, s.Distance, s.Reciprocal, s.HourParts),
		fmt.Sprintf("乘法结合律允许先乘后两个因数。代回原题，%s×(%s)=%s%s。", s.Rate, s.Hours, s.Distance, s.Unit),
	}
	return strings.Join(steps, "\n") + "\n\n答案：" + s.Answer()
}
