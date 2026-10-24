package engineadapter

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/skill"
)

// FreezeReportSolveTeachingContext 在原联合调用中增加双层结构，沿用同一教学Skill快照。
func (a *SolveAdapter) FreezeReportSolveTeachingContext(ctx context.Context, subject, grade string) (string, []skill.ContextReceipt, error) {
	contract, receipts, err := a.FreezeSolveTeachingContext(ctx, subject, grade)
	if err != nil {
		return "", nil, err
	}
	contract = strings.Replace(contract, "exactly seven fields:", "the seven existing fields and report_content:", 1)
	return contract + teachingReportJointContract, receipts, nil
}

const teachingReportJointContract = `
本次教学合同在原七项之外增加 report_content，同次生成并同次独立审核，不另行求解或润色。
原七项保留完整含义，使用本题必要的最少条目，不为凑数扩写；每条一句话，例子和追问只保留能检查本题关键理解的内容。
report_content只描述当前这一个求解目标，problem_id、problem_ids固定用局部标识current。服务端会绑定真实来源，禁止模型填写作答、评分、来源摘要、验证状态、origins、任意SVG或文件路径。
快速解答的answers直接给出全部所求量、单位和一套符合题面及教材的推荐完整写法；题目明确要求多解时须列全。其他有效写法及各自前提放入深度讲解的supplement块，不在快速解答堆放多段说明，也不得删掉这些前提或据推荐写法否定孩子的有效作答。steps只列必要算式，不混入段落解释。每个所求量可独立一项；text与latex只选一种，latex不含$定界符。短标题说明本题求什么。
数学答案、算式及含分数的关系式必须使用latex字段，分数写作\\frac{分子}{分母}，单位写作\\mathrm{TB}等；不要用text字段塞入5/2之类的纯文本算式。答案给所求结果，steps给必要列式，不重复一遍综合算式和代回检查；代回检查放入深度讲解。
sections为深度讲解，按本题实际卡点组织简短解释、必要关系、一般易错提醒和理解检查，不机械填满栏目，不重复答案或自述工具/模型。不把一般提醒说成该孩子真实错因，不要求家长重画。未提供可信图时diagram_ids为空，不声称已画图。方法注释只写实际采用的方法及对应block_ids，description用一句话说明本题怎么用，不堆术语。
report_content的精确JSON结构如下；可根据实际内容增减数组项，但items必须恰好包含current一项，不能漏掉原题独有答案和必要条件：
{"version":"teaching-report-content-v1","items":[{"problem_id":"current","short_title":"本题所求","answers":[{"id":"answer-1","label":"所求量","text":"完整直接答案","conditions":[]}],"steps":[{"id":"step-1","latex":"必要列式"}]}],"sections":[{"id":"section-1","title":"关键关系","problem_ids":["current"],"blocks":[{"id":"block-1","problem_ids":["current"],"text":"简洁讲解","purpose":""}],"diagram_ids":[],"method_notes":[{"name":"实际教学方法","description":"本题具体怎么用","block_ids":["block-1"]}]}]}
讲解块可用latex表示单独算式，或table:{headers:[{text或latex}],rows:[[{text或latex}]]}。purpose仅文字块可用supplement或pitfall，算式和表格留空串。全部答案、例子、讲法和检查必须与本候选完整解法一致，尚未独立验证时不得自称已验证。`

const teachingReportJointAudit = `
本候选包含report_content，审核fields必须在七个原字段之外增加且只增加report_content，共八项。独立检查其全部答案、单位、多解前提、必要算式、解释、例子、追问、理解检查和实际方法；不能因原解法正确就放过报告错误。缺字段、漏所求量、跨题内容或把学生未知状态编成事实均判该字段invalid。报告内容审核不替代原解法的独立执行证据。`
