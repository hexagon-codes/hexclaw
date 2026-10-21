package k12

import (
	"io/fs"
	"strings"
	"testing"
)

// 内联回显只复述有证据的原题，并在同条消息给完整解答。
// 原图留白、档案资料及未可靠辨认片段分别保留，不引入阻塞式复述确认。
func TestHomeworkChecker_InlineEchoContract(t *testing.T) {
	raw, err := fs.ReadFile(BundledSkillsFS(), "skills/homework-checker.md")
	if err != nil {
		t.Fatalf("读取 homework-checker.md 失败: %v", err)
	}
	body := string(raw)

	// 必含：内联回显契约要素（回显抬头 + 同条消息 + 不确定字符护栏）。
	mustContain := map[string]string{
		"内联回显":   "缺内联回显方案声明",
		"我读到的题目": "缺回显抬头（先列识别到的题目让家长核对）",
		"同一条消息":  "缺「同一条消息内回显+解答」的单轮契约",
		"[?]":    "缺手写不确定字符 [?] 标注护栏",
		"待核对":    "缺 [?] 题在确认前只标待核对、不下批改结论的护栏",
	}
	for sub, why := range mustContain {
		if !strings.Contains(body, sub) {
			t.Errorf("回显护栏契约缺失「%s」：%s", sub, why)
		}
	}

	// 原图留白与档案称呼分别保留，不把资料改写为OCR事实。
	headerName := map[string]string{
		"原图表头和档案资料分开":      "缺姓名来源区分",
		"原图「姓名：___」保持识别为空": "缺原图留白保真",
		"按档案来源单独标识":        "缺档案称呼来源标识",
		"不能把资料补名写成 OCR 原文": "缺资料与OCR边界",
		"缺失称呼不编造":          "缺姓名缺失保护",
	}
	for sub, why := range headerName {
		if !strings.Contains(body, sub) {
			t.Errorf("作业表头姓名回显契约缺失「%s」：%s", sub, why)
		}
	}

	// 不得回退：阻塞式两轮门的措辞（识别完先不解题、等家长确认读得对后才进入）已弃。
	mustNotContain := map[string]string{
		"先不解题": "不得回退到「识别完先不解题」的阻塞式两轮门（IM 往返代价高）",
		"读得对":  "不得保留「家长确认『读得对』后才进入」的阻塞门措辞",
	}
	for sub, why := range mustNotContain {
		if strings.Contains(body, sub) {
			t.Errorf("回显护栏残留阻塞门措辞「%s」：%s", sub, why)
		}
	}
}
