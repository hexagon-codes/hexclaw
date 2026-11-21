package elementarymath

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
)

// TravelWorksheet 保存匀速份数题的主问，以及实际读到的连续印刷问项。
type TravelWorksheet struct {
	UniformTravelProblem
	Parts, HourParts, Reciprocal, PartDistance, Rate string
	FactorSlots                                      [2]FractionSlot
	BlankCount                                       int
	DerivationPrinted, ReasonRequested               bool
}

// FractionSlot 表示原纸上实际留给学生书写的部分。
type FractionSlot uint8

const (
	WholeFraction FractionSlot = iota
	NumeratorOnly
	DenominatorOnly
	UncertainFractionSlot
)

var travelWorksheetNumber = regexp.MustCompile(`^[0-9]+[.、]\s*([\p{Han}])`)
var travelBoxFraction = regexp.MustCompile(`\\(?:d|t)?frac\s*\{\s*(□|\\square|\\Box|[0-9]+)\s*\}\s*\{\s*(□|\\square|\\Box|[0-9]+)\s*\}`)
var travelEmptyFractionText = regexp.MustCompile(`(?:\(\s*□\s*\)|□)\s*/\s*(?:\(\s*□\s*\)|□)`)
var travelDiagramCaption = regexp.MustCompile(`^(?:线段|示意)?图(?:示|中)?(?:文字|标注)?:`)
var travelDiagramMeasurement = regexp.MustCompile(`^([0-9]+(?:/[0-9]+)?)小时(?:行走|走|步行|行驶|骑行)了?(\?|[0-9]+)(km|m|米)`)
var figureConclusion = regexp.MustCompile(`^(?:(?:从|由|根据)(?:上图|图中|图示|线段图)(?:中)?(?:可以|可|能)?(?:看出|得出|得到|知道|知|见|得):?)?$`)
var travelKnownParts = regexp.MustCompile(`^已知(?:\(\)|□)份(?:是|对应)([0-9]+)(km|米|m)`)
var travelOnePart = regexp.MustCompile(`^[,。](?:可以)?先求(?:出)?(?:这样的)?(?:1|一(?:\(\)|□))份(?:是)?多少`)

const travelFractionSlot = `(□/[0-9]+|[0-9]+/□|\(\)|□)`

var travelPartEquation = regexp.MustCompile(`^[,。](?:也就是|即)([0-9]+)÷([0-9]+)=([0-9]+)×` + travelFractionSlot)
var travelHourParts = regexp.MustCompile(`^[;。]再求(?:出)?(?:这样的)?一?(?:\(\)|□)份(?:是)?多少`)
var travelHourEquation = regexp.MustCompile(`^[,。](?:也就是|即)([0-9]+)×` + travelFractionSlot + `×([0-9]+)$`)
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
	if end < 0 {
		return TravelWorksheet{}, false
	}
	main := travelWorksheetNumber.ReplaceAllString(strings.TrimSpace(source[:end]), "$1") + "？"
	primary, literal, ok := parseUniformTravel(main)
	if !ok {
		return TravelWorksheet{}, false
	}
	time, _ := new(big.Rat).SetString(primary.Hours)
	distance, _ := new(big.Rat).SetString(primary.Distance)
	written := travelFraction.FindStringSubmatch(literal)
	if !distance.IsInt() || distance.Sign() <= 0 || written == nil || written[1] != "" || time.Cmp(big.NewRat(1, 1)) >= 0 {
		return TravelWorksheet{}, false
	}
	p, q, d := written[2], written[3], distance.RatString()
	printedTime := p + "/" + q
	parts, _ := new(big.Rat).SetString(p)
	part := new(big.Rat).Quo(distance, parts)
	rate := new(big.Rat).Quo(distance, time)
	unit := travelWorksheetPunctuation.Replace(primary.Unit)
	tail := compactTravelWorksheet(source[end:])
	_, tail, _ = strings.Cut(tail, "?")
	// 以“已知……份”的实际问项分界。图后可以有承接词，也可以直接开始提问。
	start := strings.Index(tail, "已知")
	if start < 0 || !travelDiagramMatches(tail[:start], printedTime, q, d, unit) {
		return TravelWorksheet{}, false
	}
	blanks, tail, hasDerivation := strings.Cut(tail[start:], "由此推算出:")
	sheet := TravelWorksheet{UniformTravelProblem: primary, Parts: p, HourParts: q, Reciprocal: new(big.Rat).Inv(parts).RatString(), PartDistance: part.RatString(), Rate: rate.RatString(), DerivationPrinted: hasDerivation}
	if !sheet.readPrintedBlanks(blanks, unit) || hasDerivation && sheet.BlankCount != 4 {
		return TravelWorksheet{}, false
	}
	if !hasDerivation {
		return sheet, true
	}
	equation := fmt.Sprintf("%s×1/%s×%s=%s×(1/%s×%s)", d, p, q, d, p, q)
	chain := fmt.Sprintf("%s÷%s=%s÷%s×%s=%s=%s×%s/%s", d, printedTime, d, p, q, equation, d, q, p)
	// 排版器可能给作为除数的整个分数加括号，数学关系不变。
	tail = strings.Replace(tail, d+"÷("+printedTime+")=", d+"÷"+printedTime+"=", 1)
	reason := fmt.Sprintf("。推算过程中\"%s\"的依据是()", equation)
	tail = strings.TrimRight(tail, "。.")
	if strings.HasSuffix(tail, "的依据是□") {
		tail = strings.TrimSuffix(tail, "□") + "()"
	}
	tail, withReason := strings.CutSuffix(tail, reason)
	unitSuffix := "(" + unit + ")"
	if tail != chain+unitSuffix && tail != chain+"="+rate.RatString()+unitSuffix {
		return TravelWorksheet{}, false
	}
	sheet.ReasonRequested = withReason
	return sheet, true
}

func compactTravelWorksheet(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, travelWorksheetPunctuation.Replace(text))
	return strings.ReplaceAll(text, "(□)", "()")
}

// CorroboratedTravelWorksheet 取各份独立读数的共同问项。转写较长的一份只提供前缀比对基准，
// 不能增加短读数所未确认的问项；被选为展示原文的那份也参加核对。
func CorroboratedTravelWorksheet(source string, readings []string) (TravelWorksheet, string, bool) {
	texts := append([]string{source}, readings...)
	var sheet TravelWorksheet
	var reference string
	for _, text := range texts {
		if parsed, ok := ParseTravelWorksheet(text); ok && parsed.BlankCount > sheet.BlankCount {
			sheet, reference = parsed, text
		}
	}
	if reference == "" {
		return TravelWorksheet{}, "", false
	}
	confirmed := reference
	for _, text := range texts {
		other, excerpt, ok := travelWorksheetReading(reference, text)
		if !ok {
			return TravelWorksheet{}, "", false
		}
		if other.BlankCount < sheet.BlankCount || text == source && other.BlankCount == sheet.BlankCount {
			confirmed = excerpt
		}
		if sheet, ok = sheet.Corroborate(other); !ok {
			return TravelWorksheet{}, "", false
		}
	}
	return sheet, confirmed, true
}

// 允许复读在下一问的句中结束。只有余下文字确实是参考原文的前缀，
// 才能截到上一完整分句。任何新增条件、操作数或运算符差异仍交给原来的分歧处理。
func travelWorksheetReading(reference, reading string) (TravelWorksheet, string, bool) {
	if sheet, ok := ParseTravelWorksheet(reading); ok {
		return sheet, reading, true
	}
	tail := func(s string) string {
		_, s, found := strings.Cut(s, "已知")
		if !found {
			return ""
		}
		s, _ = mathtext.ProjectReadable(TravelFractionBlankLayout(s))
		return compactTravelWorksheet(s)
	}
	observed, complete := tail(reading), tail(reference)
	if observed == "" || len(observed) >= len(complete) || !strings.HasPrefix(complete, observed) {
		return TravelWorksheet{}, "", false
	}
	// 四个空最多分作两次份数和两次算式。只枚举问项间已有的句界，保留原文作为求解输入。
	start := strings.Index(reading, "已知")
	for end := len(reading); end > start; {
		cut := strings.LastIndexAny(reading[start:end], "；;。，,")
		if cut < 0 {
			break
		}
		end = start + cut
		if sheet, ok := ParseTravelWorksheet(reading[:end]); ok {
			return sheet, strings.TrimSpace(reading[:end]), true
		}
	}
	return TravelWorksheet{}, "", false
}

// 只在完整的印刷分句边界接受截短的读数。现存字符与算式有冲突时不能按截短处理。
func (s *TravelWorksheet) readPrintedBlanks(source, unit string) bool {
	tail := strings.TrimRight(source, ";。,")
	consume := func(pattern *regexp.Regexp) []string {
		fields := pattern.FindStringSubmatch(tail)
		if fields != nil {
			tail = tail[len(fields[0]):]
		}
		return fields
	}
	known := consume(travelKnownParts)
	if known == nil || known[1] != s.Distance || known[2] != unit {
		return false
	}
	s.BlankCount = 1
	if tail == "" {
		return true
	}
	if consume(travelOnePart) == nil {
		return false
	}
	if tail == "" {
		return true
	}
	first := consume(travelPartEquation)
	if first == nil || first[1] != s.Distance || first[2] != s.Parts || first[3] != s.Distance || !s.readFactor(0, first[4]) {
		return false
	}
	s.BlankCount = 2
	if tail == "" {
		return true
	}
	if consume(travelHourParts) == nil {
		return false
	}
	s.BlankCount = 3
	if tail == "" {
		return true
	}
	last := consume(travelHourEquation)
	if last == nil || last[1] != s.Distance || last[3] != s.HourParts || !s.readFactor(1, last[2]) {
		return false
	}
	s.BlankCount = 4
	return true
}

func (s *TravelWorksheet) readFactor(index int, printed string) bool {
	switch printed {
	case "()", "□":
		s.FactorSlots[index] = WholeFraction
	case "□/" + s.Parts:
		s.FactorSlots[index] = NumeratorOnly
	case "1/□":
		s.FactorSlots[index] = DenominatorOnly
	default:
		return false
	}
	return true
}

func travelDiagramMatches(diagram, hours, hourParts, distance, unit string) bool {
	type measurement struct{ time, distance string }
	diagram, wrapped := strings.CutPrefix(diagram, "(")
	diagram = travelDiagramCaption.ReplaceAllString(diagram, "")
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
	if wrapped {
		var closed bool
		diagram, closed = strings.CutPrefix(diagram, ")")
		if !closed {
			return false
		}
	}
	return figureConclusion.MatchString(diagram)
}

// 证据只属于本次已核实的原题；同一上下文里的其它文字或图片不能继承它。
type worksheetEvidenceKey struct{}
type worksheetEvidence struct {
	source string
	sheet  TravelWorksheet
}

func WithWorksheetEvidence(ctx context.Context, source string, sheet TravelWorksheet) context.Context {
	return context.WithValue(ctx, worksheetEvidenceKey{}, worksheetEvidence{source, sheet})
}

func TravelWorksheetFor(ctx context.Context, source string) (TravelWorksheet, bool) {
	if evidence, ok := ctx.Value(worksheetEvidenceKey{}).(worksheetEvidence); ok && evidence.source == source {
		return evidence.sheet, true
	}
	return ParseTravelWorksheet(source)
}

// Corroborate 合并同一题的独立读数。数学条件必须相同，预印位置有分歧时保留该格的不确定性。
func (s TravelWorksheet) Corroborate(other TravelWorksheet) (TravelWorksheet, bool) {
	slots := other.FactorSlots
	other.FactorSlots = s.FactorSlots
	s.BlankCount = min(s.BlankCount, other.BlankCount)
	other.BlankCount = s.BlankCount
	s.ReasonRequested = s.ReasonRequested && other.ReasonRequested
	other.ReasonRequested = s.ReasonRequested
	s.DerivationPrinted = s.DerivationPrinted && other.DerivationPrinted
	other.DerivationPrinted = s.DerivationPrinted
	if s != other {
		return TravelWorksheet{}, false
	}
	for i, slot := range slots {
		if 2*i+2 > s.BlankCount {
			s.FactorSlots[i] = WholeFraction
		} else if s.FactorSlots[i] != slot {
			s.FactorSlots[i] = UncertainFractionSlot
		}
	}
	return s, true
}

func (s TravelWorksheet) WrittenValues() [4]string {
	values := [4]string{s.Parts, s.Reciprocal, s.HourParts, s.Reciprocal}
	for i, slot := range s.FactorSlots {
		switch slot {
		case NumeratorOnly:
			values[2*i+1] = "1"
		case DenominatorOnly:
			values[2*i+1] = s.Parts
		}
	}
	return values
}

func (s TravelWorksheet) Answer() string {
	values := s.WrittenValues()
	for i, slot := range s.FactorSlots {
		if slot == UncertainFractionSlot {
			values[2*i+1] = fmt.Sprintf("%s（整格写分数，只空分子填1，只空分母填%s）", s.Reciprocal, s.Parts)
		}
	}
	labels := [...]string{"", "第一个空填写", "前两个空依次填写", "前三个空依次填写", "按题面四个空依次填写"}
	answer := fmt.Sprintf("每小时走%s%s。%s %s。", s.Rate, s.Unit, labels[s.BlankCount], strings.Join(values[:s.BlankCount], "、"))
	if s.ReasonRequested {
		answer += "等式的依据是乘法结合律。"
	}
	return answer
}

func (s TravelWorksheet) Worked() string {
	steps := []string{
		fmt.Sprintf("把1小时平均分成%[1]s份，每份是1/%[1]s小时。已知%s小时对应%s份，走了%s%s。", s.HourParts, s.Parts+"/"+s.HourParts, s.Parts, s.Distance, s.Unit),
		fmt.Sprintf("每份走%s÷%s=%s%s。除以%s等于乘它的倒数%s。", s.Distance, s.Parts, s.PartDistance, s.Unit, s.Parts, s.Reciprocal),
		fmt.Sprintf("乘数要写成分数时，分子是1，分母是%[1]s。分母已印就填分子1，分子已印就填分母%[1]s。", s.Parts),
		fmt.Sprintf("1小时有%s份，每小时走%s×%s=%s%s。把两步合起来是%s×%s×%s。", s.HourParts, s.PartDistance, s.HourParts, s.Rate, s.Unit, s.Distance, s.Reciprocal, s.HourParts),
	}
	check := fmt.Sprintf("代回原题，%s×(%s)=%s%s。", s.Rate, s.Hours, s.Distance, s.Unit)
	if s.ReasonRequested {
		check = "乘法结合律允许先乘后两个因数。" + check
	}
	steps = append(steps, check)
	return strings.Join(steps, "\n") + "\n\n答案：" + s.Answer()
}
