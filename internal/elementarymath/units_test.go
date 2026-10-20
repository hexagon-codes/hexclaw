package elementarymath

import (
	"regexp"
	"testing"
)

// 单位与写法来自小学度量和应用题场景；同单位别名可等同，不暗中进行跨单位换算。
func TestElementaryUnitAliases(t *testing.T) {
	pattern := regexp.MustCompile(`(?i)^(?:` + UnitPattern() + `)$`)
	for _, tc := range []struct {
		canonical string
		aliases   []string
	}{
		{"毫米", []string{"毫米", "mm"}}, {"厘米", []string{"厘米", "cm"}},
		{"分米", []string{"分米", "dm"}}, {"米", []string{"米", "m"}}, {"千米", []string{"千米", "公里", "km"}},
		{"平方毫米", []string{"平方毫米", "mm²", "mm2", "mm^2"}},
		{"平方厘米", []string{"平方厘米", "cm²", "cm2", "cm^2"}},
		{"平方分米", []string{"平方分米", "dm²", "dm2", "dm^2"}},
		{"平方米", []string{"平方米", "m²", "m2", "m^2"}},
		{"平方千米", []string{"平方千米", "km²", "km2", "km^2"}}, {"公顷", []string{"公顷", "ha"}},
		{"立方毫米", []string{"立方毫米", "mm³", "mm3", "mm^3"}},
		{"立方厘米", []string{"立方厘米", "cm³", "cm3", "cm^3"}},
		{"立方分米", []string{"立方分米", "dm³", "dm3", "dm^3"}},
		{"立方米", []string{"立方米", "m³", "m3", "m^3"}},
		{"升", []string{"升", "公升", "L", "l"}}, {"毫升", []string{"毫升", "mL", "ml"}},
		{"克", []string{"克", "g"}}, {"千克", []string{"千克", "公斤", "kg", "KG"}},
		{"吨", []string{"吨", "t"}}, {"毫克", []string{"毫克", "mg"}},
		{"秒", []string{"秒", "秒钟", "s"}}, {"分钟", []string{"分钟", "min"}},
		{"小时", []string{"小时", "时", "h"}}, {"天", []string{"天", "日", "d"}},
		{"周", []string{"周", "星期"}}, {"月", []string{"月"}}, {"年", []string{"年"}}, {"世纪", []string{"世纪"}},
		{"元", []string{"元", "圆", "人民币元"}}, {"角", []string{"角"}}, {"分", []string{"分"}},
		{"万元", []string{"万元"}}, {"亿元", []string{"亿元"}},
		{"角度", []string{"°"}}, {"度", []string{"度"}}, {"摄氏度", []string{"摄氏度", "℃", "°C"}},
		{"千瓦时", []string{"千瓦时", "kWh", "kW·h", "千瓦·时", "度电"}},
		{"瓦", []string{"瓦", "W"}}, {"千瓦", []string{"千瓦", "kW"}},
		{"字节", []string{"字节", "B"}}, {"千字节", []string{"千字节", "KB", "kB"}},
		{"兆字节", []string{"兆字节", "MB"}}, {"吉字节", []string{"吉字节", "GB"}}, {"太字节", []string{"太字节", "TB"}},
		{"比特", []string{"比特", "bit", "b"}}, {"千比特", []string{"kb", "kbit"}},
		{"兆比特", []string{"Mb", "Mbit"}}, {"吉比特", []string{"Gb", "Gbit"}}, {"太比特", []string{"Tb", "Tbit"}},
		{"米/秒", []string{"m/s", "米/秒", "米每秒", "米／秒"}},
		{"千米/小时", []string{"km/h", "千米/时", "公里/小时"}},
		{"元/千克", []string{"元/kg", "元/千克", "元每公斤"}},
		{"米/分钟", []string{"m/min", "米/分钟"}}, {"元/个", []string{"元/个"}},
	} {
		for _, alias := range tc.aliases {
			t.Run(alias, func(t *testing.T) {
				if !pattern.MatchString(alias) || NormalizeUnit(alias) != tc.canonical {
					t.Fatalf("unit %q normalized to %q, want %q", alias, NormalizeUnit(alias), tc.canonical)
				}
			})
		}
	}
	for _, unit := range []string{"张纸", "张", "本", "册", "页", "段", "人", "份", "个", "只", "条", "件", "台", "套", "双", "对", "支", "枝", "根", "棵", "朵", "辆", "架", "艘", "座", "栋", "间", "户", "家", "箱", "盒", "瓶", "杯", "袋", "包", "桶", "盆", "捆", "卷", "堆", "批", "组", "队", "班", "排", "位", "名", "节", "课时", "次", "遍", "圈", "轮", "级", "层", "块", "片", "粒", "颗", "枚", "题", "步", "趟", "场"} {
		if !pattern.MatchString(unit) || NormalizeUnit(unit) != unit {
			t.Fatalf("count unit %q lost its original meaning", unit)
		}
	}
}

func TestElementaryUnitsKeepDistinctMeanings(t *testing.T) {
	for _, pair := range [][2]string{{"m", "cm"}, {"m²", "m³"}, {"元", "角"}, {"分", "min"}, {"度", "°"}, {"块", "元"}, {"TB", "Tb"}, {"B", "b"}, {"MB", "Mb"}, {"L", "mL"}, {"m/s", "m/min"}} {
		if NormalizeUnit(pair[0]) == NormalizeUnit(pair[1]) {
			t.Fatalf("different units became aliases: %v", pair)
		}
	}
	for _, unit := range []string{"unknown", "tb", "mb", "米/未知", "米/秒/秒", "", "kg of sugar"} {
		if NormalizeUnit(unit) != "" {
			t.Fatalf("unknown or incomplete unit acquired a meaning: %q", unit)
		}
	}
}
