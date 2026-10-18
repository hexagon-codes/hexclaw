package adapter

import "github.com/hexagon-codes/hexclaw/messagecontent"

// ExecutionSummary 只投影本次结果可关联的执行事实，不承载调用参数或结果正文。
type ExecutionSummary = messagecontent.ExecutionSummary

// ExecutionSummaryStep 的 Key 使用稳定阶段语义，Status 来自实际执行回执。
type ExecutionSummaryStep = messagecontent.ExecutionSummaryStep

// ExecutionSummaryTool 保留作者名称与真实来源；多语言名称必须由来源显式提供。
type ExecutionSummaryTool = messagecontent.ExecutionSummaryTool

// ExecutionSummaryModel 保存实际调用的模型身份，不读取当前默认设置。
type ExecutionSummaryModel = messagecontent.ExecutionSummaryModel

// RenderExecutionSummary 生成紧凑 Markdown 尾注；缺少事实的栏目省略，模型始终在末尾。
// 只有明确已执行的终态进入投影，加载上下文和待执行计划不是调用事实。
func RenderExecutionSummary(locale string, facts ExecutionSummary) string {
	return messagecontent.RenderExecutionSummary(locale, facts)
}
