package render

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"strings"
	"unicode"
)

const TeachingDiagramRendererVersion = "teaching-svg-v3"

// TeachingDiagramInput 只承载用例已校验的图元，不包含题目、学生状态或求解逻辑。
type TeachingDiagramInput struct {
	Kind       string
	Title      string
	TotalLabel string
	AltText    string
	EqualParts int
	Segments   []TeachingDiagramSegment
	Endpoints  [2]string
	Travelers  [2]string
}

type TeachingDiagramSegment struct {
	Fraction    float64
	Label       string
	Highlighted bool
	Reverse     bool
}

type TeachingDiagramSVG struct {
	Content []byte
	Width   int
	Height  int
}

// RenderTeachingDiagramSVG 按统一几何生成实际图形；所有可见文本均来自同一份渲染输入。
func RenderTeachingDiagramSVG(input TeachingDiagramInput) (TeachingDiagramSVG, error) {
	if input.Kind != "fraction_bar" && input.Kind != "segment_bar" && input.Kind != "journey_line" {
		return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching diagram kind")
	}
	if len(input.Segments) == 0 || input.EqualParts < 0 || input.EqualParts > 24 {
		return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching diagram density")
	}
	var total float64
	for _, segment := range input.Segments {
		if math.IsNaN(segment.Fraction) || math.IsInf(segment.Fraction, 0) || segment.Fraction <= 0 {
			return TeachingDiagramSVG{}, fmt.Errorf("invalid teaching diagram geometry")
		}
		total += segment.Fraction
		if len([]rune(segment.Label)) > 26 {
			return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching diagram label length")
		}
	}
	if math.Abs(total-1) > 1e-12 {
		return TeachingDiagramSVG{}, fmt.Errorf("invalid teaching diagram total")
	}
	for _, label := range []string{input.Title, input.TotalLabel} {
		if teachingDiagramTextWidth(label) > 820 {
			return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching diagram label length")
		}
	}
	if input.Kind == "journey_line" {
		return renderTeachingJourneyLine(input)
	}
	escape := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	const width, height = 900, 230
	const left, right, top, barHeight = 40.0, 860.0, 88.0, 44.0
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-labelledby="title description"><title id="title">%s</title><desc id="description">%s</desc><rect width="900" height="230" fill="white"/><g font-family="PingFang SC, Noto Sans CJK SC, sans-serif" font-size="22" fill="#243746"><text x="40" y="34">%s</text>`, width, height, width, height, escape(input.Title), escape(input.AltText), escape(input.Title))
	x := left
	for i, segment := range input.Segments {
		w := (right - left) * segment.Fraction
		label := escape(segment.Label)
		if teachingDiagramTextWidth(segment.Label) > w-8 {
			// 窄段按词间空格分成两行，数量与单位仍保持在同一行。
			label = ""
			words := strings.Fields(segment.Label)
			for split := 1; split < len(words); split++ {
				first, second := strings.Join(words[:split], " "), strings.Join(words[split:], " ")
				if teachingDiagramTextWidth(first) <= w-8 && teachingDiagramTextWidth(second) <= w-8 {
					label = fmt.Sprintf(`<tspan x="%.6f" y="58">%s</tspan><tspan x="%.6f" y="82">%s</tspan>`, x+w/2, escape(first), x+w/2, escape(second))
					break
				}
			}
			if label == "" {
				return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching diagram label density")
			}
		}
		fill := "#f1f5f9"
		if segment.Highlighted {
			fill = "#c7def1"
		}
		fmt.Fprintf(&b, `<rect data-segment="%d" x="%.6f" y="88" width="%.6f" height="44" fill="%s" stroke="#243746" stroke-width="2"/><text x="%.6f" y="66" text-anchor="middle">%s</text>`, i, x, w, fill, x+w/2, label)
		if input.Kind == "journey_line" && segment.Highlighted {
			start, end := x+12, x+w-12
			if segment.Reverse {
				start, end = end, start
			}
			sign := 1.0
			if segment.Reverse {
				sign = -1
			}
			fmt.Fprintf(&b, `<path d="M %.6f 110 H %.6f M %.6f 103 L %.6f 110 L %.6f 117" fill="none" stroke="#315f7c" stroke-width="2"/>`, start, end, end-8*sign, end, end-8*sign)
		}
		x += w
	}
	for i := 1; i < input.EqualParts; i++ {
		x := left + (right-left)*float64(i)/float64(input.EqualParts)
		fmt.Fprintf(&b, `<path data-division="%d" d="M %.6f %.0f V %.0f" stroke="#64748b" stroke-width="1"/>`, i, x, top, top+barHeight)
	}
	fmt.Fprintf(&b, `<path d="M 40 149 V 160 H 860 V 149" fill="none" stroke="#243746" stroke-width="2"/><text x="450" y="194" text-anchor="middle">%s</text></g></svg>`, escape(input.TotalLabel))
	return TeachingDiagramSVG{Content: []byte(b.String()), Width: width, Height: height}, nil
}

// 行程使用课本常见的带刻度线段，位置、方向和括线共用同一份几何。
func renderTeachingJourneyLine(input TeachingDiagramInput) (TeachingDiagramSVG, error) {
	if len(input.Segments) != 3 || input.EqualParts < 1 {
		return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching journey geometry")
	}
	escape := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	for i, fallback := range [2]string{"起点", "终点"} {
		if input.Endpoints[i] == "" {
			input.Endpoints[i] = fallback
		}
		if len([]rune(input.Endpoints[i])) > 6 {
			return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching journey label length")
		}
	}
	for i, fallback := range [2]string{"第一人", "第二人"} {
		if input.Travelers[i] == "" {
			input.Travelers[i] = fallback
		}
		if len([]rune(input.Travelers[i])) > 6 {
			return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching journey label length")
		}
	}
	const left, length = 40.0, 820.0
	first := left + length*input.Segments[0].Fraction
	second := first + length*input.Segments[1].Fraction
	// 短端点保留原中心位置，长名称仅向画布内侧平移，避免裁切字形。
	startLabel := math.Max(left, 8+teachingDiagramTextWidth(input.Endpoints[0])/2)
	endLabel := math.Min(left+length, 900-8-teachingDiagramTextWidth(input.Endpoints[1])/2)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 900 270" width="900" height="270" role="img" aria-labelledby="title description"><title id="title">%s</title><desc id="description">%s</desc><rect width="900" height="270" fill="white"/><g font-family="PingFang SC, Noto Sans CJK SC, sans-serif" font-size="22" fill="#243746"><text x="40" y="30">全程平均分成%d份</text><path d="M 40 118 H 860" fill="none" stroke="#243746" stroke-width="2"/>`, escape(input.Title), escape(input.AltText), input.EqualParts)
	for i := 0; i <= input.EqualParts; i++ {
		x := left + length*float64(i)/float64(input.EqualParts)
		fmt.Fprintf(&b, `<path data-division="%d" d="M %.6f 110 V 126" stroke="#243746" stroke-width="2"/>`, i, x)
	}
	fmt.Fprintf(&b, `<text x="%.6f" y="101" text-anchor="middle">%s</text><text x="%.6f" y="101" text-anchor="middle">%s</text><text x="%.6f" y="101" text-anchor="middle">%s</text><text x="%.6f" y="101" text-anchor="middle">%s</text>`, startLabel, escape(input.Endpoints[0]), endLabel, escape(input.Endpoints[1]), first, escape(input.Travelers[0]), second, escape(input.Travelers[1]))
	fmt.Fprintf(&b, `<path d="M 40 71 H %.6f M %.6f 65 L %.6f 71 L %.6f 77 M 860 71 H %.6f M %.6f 65 L %.6f 71 L %.6f 77" fill="none" stroke="#243746" stroke-width="2"/>`, first-8, first-16, first-8, first-16, second+8, second+16, second+8, second+16)
	x := left
	for i, segment := range input.Segments {
		w := length * segment.Fraction
		parts := math.Round(segment.Fraction * float64(input.EqualParts))
		if math.Abs(parts-segment.Fraction*float64(input.EqualParts)) > 1e-9 || parts < 1 {
			return TeachingDiagramSVG{}, fmt.Errorf("invalid teaching journey division")
		}
		value := strings.TrimPrefix(segment.Label, []string{"第一人 ", "相距 ", "第二人 "}[i])
		if teachingDiagramTextWidth(value) > w-8 {
			return TeachingDiagramSVG{}, fmt.Errorf("unsupported teaching journey label density")
		}
		fmt.Fprintf(&b, `<path data-segment="%d" d="M %.6f 137 V 145 H %.6f V 137" fill="none" stroke="#243746" stroke-width="1.5"/><text x="%.6f" y="170" text-anchor="middle">%.0f份</text><text x="%.6f" y="195" text-anchor="middle">%s</text>`, i, x, x+w, x+w/2, parts, x+w/2, escape(value))
		x += w
	}
	fmt.Fprintf(&b, `<path d="M 40 216 V 224 H 860 V 216" fill="none" stroke="#243746" stroke-width="1.5"/><text x="450" y="254" text-anchor="middle">%s</text></g></svg>`, escape(input.TotalLabel))
	return TeachingDiagramSVG{Content: []byte(b.String()), Width: 900, Height: 270}, nil
}

// 保守预估仅用于模板排版；实际字体与PDF边界仍需对最终文件核验。
func teachingDiagramTextWidth(value string) float64 {
	var width float64
	for _, r := range value {
		if unicode.Is(unicode.Han, r) || r > 0xFF00 {
			width += 22
		} else {
			width += 13
		}
	}
	return width
}
