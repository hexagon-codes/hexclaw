package k12

import (
	"io/fs"
	"strings"
	"testing"
)

// 最终解答保留逐题结果、冻结答案和回显边界。
// 渠道结构、公式及PDF投影由宿主处理，Skill不声称实际交付。
func TestHomeworkChecker_MarkdownStructureContract(t *testing.T) {
	raw, err := fs.ReadFile(BundledSkillsFS(), "skills/homework-checker.md")
	if err != nil {
		t.Fatalf("读取 homework-checker.md 失败: %v", err)
	}
	body := string(raw)

	mustContain := map[string]string{
		"最终解答的内容合同":            "缺最终解答内容合同",
		"内容逐题对应":               "缺逐题内容对应约束",
		"批改状态、原始作答、正确答案三项":     "缺固定三项结果内容合同",
		"source_answer":        "缺冻结答案来源",
		"答案逐字保持":               "缺冻结答案保真约束",
		"题号与首段同行":              "缺题号与正文关系",
		"具体渠道、公式投影、PDF 排版":     "缺宿主负责投影的边界",
		"Skill 不判断消息已发出或文件已打印": "缺实际交付回执边界",
		"===最终解答===":           "markdown 结构应锚定在「===最终解答===」之后的解答正文",
	}
	for sub, why := range mustContain {
		if !strings.Contains(body, sub) {
			t.Errorf("markdown 结构输出契约缺失「%s」：%s", sub, why)
		}
	}
}
