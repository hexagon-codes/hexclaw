package elementarymath

import (
	"regexp"
	"sort"
	"strings"
)

type unitDefinition struct {
	name          string
	aliases       []string
	caseSensitive bool
}

type unitGroup struct {
	category string
	units    []unitDefinition
}

// 小学单位目录按量的类别维护；教材情境中的扩展单位同样保留独立语义。
// 目录只定义同一单位的写法，不定义单位进率，也不推断题目中省略的单位。
var elementaryUnitGroups = []unitGroup{
	{"length", []unitDefinition{
		{"毫米", []string{"mm"}, false}, {"厘米", []string{"cm"}, false},
		{"分米", []string{"dm"}, false}, {"米", []string{"m"}, false}, {"千米", []string{"km", "公里"}, false},
	}},
	{"area", []unitDefinition{
		{"平方毫米", []string{"mm²", "mm2", "mm^2"}, false}, {"平方厘米", []string{"cm²", "cm2", "cm^2"}, false},
		{"平方分米", []string{"dm²", "dm2", "dm^2"}, false}, {"平方米", []string{"m²", "m2", "m^2"}, false},
		{"平方千米", []string{"km²", "km2", "km^2"}, false}, {"公顷", []string{"ha"}, false},
	}},
	{"volume", []unitDefinition{
		{"立方毫米", []string{"mm³", "mm3", "mm^3"}, false}, {"立方厘米", []string{"cm³", "cm3", "cm^3"}, false},
		{"立方分米", []string{"dm³", "dm3", "dm^3"}, false}, {"立方米", []string{"m³", "m3", "m^3"}, false},
		{"升", []string{"L", "l", "公升"}, false}, {"毫升", []string{"mL", "ml"}, false},
	}},
	{"mass", []unitDefinition{
		{"克", []string{"g"}, false}, {"千克", []string{"kg", "公斤"}, false},
		{"吨", []string{"t"}, false}, {"毫克", []string{"mg"}, false},
	}},
	{"time", []unitDefinition{
		{"秒", []string{"s", "秒钟"}, false}, {"分钟", []string{"min"}, false},
		{"小时", []string{"h", "时"}, false}, {"天", []string{"d", "日"}, false},
		{"周", []string{"星期"}, false}, {"月", nil, false}, {"年", nil, false}, {"世纪", nil, false},
	}},
	{"money", []unitDefinition{
		{"元", []string{"圆", "人民币元"}, false}, {"角", nil, false}, {"分", nil, false},
		{"万元", nil, false}, {"亿元", nil, false},
	}},
	{"angle", []unitDefinition{{"角度", []string{"°"}, false}, {"度", nil, false}}},
	{"temperature", []unitDefinition{{"摄氏度", []string{"℃", "°C"}, false}}},
	{"energy", []unitDefinition{
		{"千瓦时", []string{"kWh", "kW·h", "千瓦·时", "度电"}, false},
		{"瓦", []string{"W"}, false}, {"千瓦", []string{"kW"}, false},
	}},
	{"data", []unitDefinition{
		{"字节", []string{"B"}, true}, {"千字节", []string{"kB", "KB"}, true},
		{"兆字节", []string{"MB"}, true}, {"吉字节", []string{"GB"}, true}, {"太字节", []string{"TB"}, true},
		{"比特", []string{"b", "bit"}, true}, {"千比特", []string{"kb", "kbit"}, true},
		{"兆比特", []string{"Mb", "Mbit"}, true}, {"吉比特", []string{"Gb", "Gbit"}, true}, {"太比特", []string{"Tb", "Tbit"}, true},
	}},
	{"count", []unitDefinition{
		{"张纸", nil, false}, {"张", nil, false}, {"本", nil, false}, {"册", nil, false}, {"页", nil, false},
		{"段", nil, false}, {"人", nil, false}, {"份", nil, false}, {"个", nil, false}, {"只", nil, false},
		{"条", nil, false}, {"件", nil, false}, {"台", nil, false}, {"套", nil, false}, {"双", nil, false},
		{"对", nil, false}, {"支", nil, false}, {"枝", nil, false}, {"根", nil, false}, {"棵", nil, false},
		{"朵", nil, false}, {"辆", nil, false}, {"架", nil, false}, {"艘", nil, false}, {"座", nil, false},
		{"栋", nil, false}, {"间", nil, false}, {"户", nil, false}, {"家", nil, false}, {"箱", nil, false},
		{"盒", nil, false}, {"瓶", nil, false}, {"杯", nil, false}, {"袋", nil, false}, {"包", nil, false},
		{"桶", nil, false}, {"盆", nil, false}, {"捆", nil, false}, {"卷", nil, false}, {"堆", nil, false},
		{"批", nil, false}, {"组", nil, false}, {"队", nil, false}, {"班", nil, false}, {"排", nil, false},
		{"位", nil, false}, {"名", nil, false}, {"节", nil, false}, {"课时", nil, false}, {"次", nil, false},
		{"遍", nil, false}, {"圈", nil, false}, {"轮", nil, false}, {"级", nil, false}, {"层", nil, false},
		{"块", nil, false}, {"片", nil, false}, {"粒", nil, false}, {"颗", nil, false}, {"枚", nil, false},
		{"题", nil, false}, {"步", nil, false}, {"趟", nil, false}, {"场", nil, false},
	}},
}

var unitCanonical, unitPattern = buildUnitCatalog()

func buildUnitCatalog() (map[string]string, string) {
	canonical := make(map[string]string)
	var patterns []string
	for _, group := range elementaryUnitGroups {
		for _, unit := range group.units {
			for _, alias := range append([]string{unit.name}, unit.aliases...) {
				canonical[alias] = unit.name
				pattern := regexp.QuoteMeta(alias)
				if unit.caseSensitive {
					pattern = "(?-i:" + pattern + ")"
				} else {
					canonical[strings.ToLower(alias)] = unit.name
				}
				patterns = append(patterns, pattern)
			}
		}
	}
	sort.Slice(patterns, func(i, j int) bool {
		if len(patterns[i]) == len(patterns[j]) {
			return patterns[i] < patterns[j]
		}
		return len(patterns[i]) > len(patterns[j])
	})
	atom := "(?:" + strings.Join(patterns, "|") + ")"
	return canonical, atom + `(?:\s*[/／每]\s*` + atom + `)?`
}

// UnitPattern 返回不含捕获组的单位表达式，可组合成完整数值或列式解析器。
// 它包含速度、单价等单层比率单位；数据容量符号保留大小写区别。
func UnitPattern() string { return unitPattern }

// NormalizeUnit 归一单位别名；未知单位返回空串，不执行单位换算。
// “分”“度”“块”等多义词保留原义，不自动映射为分钟、角度或人民币。
func NormalizeUnit(unit string) string {
	unit = strings.TrimSpace(unit)
	unit = strings.NewReplacer("／", "/", "每", "/").Replace(unit)
	if parts := strings.Split(unit, "/"); len(parts) == 2 {
		numerator, denominator := NormalizeUnit(parts[0]), NormalizeUnit(parts[1])
		if numerator != "" && denominator != "" {
			return numerator + "/" + denominator
		}
		return ""
	}
	if canonical, ok := unitCanonical[unit]; ok {
		return canonical
	}
	return unitCanonical[strings.ToLower(unit)]
}
