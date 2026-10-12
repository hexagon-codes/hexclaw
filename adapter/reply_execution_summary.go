package adapter

import "strings"

// ExecutionSummary 只投影本次结果可关联的执行事实，不承载调用参数或结果正文。
type ExecutionSummary struct {
	Steps  []ExecutionSummaryStep  `json:"steps,omitempty"`
	Tools  []ExecutionSummaryTool  `json:"tools,omitempty"`
	Models []ExecutionSummaryModel `json:"models,omitempty"`
}

// ExecutionSummaryStep 的 Key 使用稳定阶段语义，Status 来自实际执行回执。
type ExecutionSummaryStep struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

// ExecutionSummaryTool 保留作者名称与真实来源；多语言名称必须由来源显式提供。
type ExecutionSummaryTool struct {
	Kind         string            `json:"kind"`
	Name         string            `json:"name"`
	DisplayName  string            `json:"display_name,omitempty"`
	DisplayNames map[string]string `json:"display_names,omitempty"`
	ServerName   string            `json:"server_name,omitempty"`
	Status       string            `json:"status"`
}

// ExecutionSummaryModel 保存实际调用的模型身份，不读取当前默认设置。
type ExecutionSummaryModel struct {
	Stage    string `json:"stage,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
}

type executionSummaryLabels struct {
	process, tools, models, tool, failed, unknown, partialFailed, partialUnknown string
}

var executionSummaryLocaleLabels = map[string]executionSummaryLabels{
	"zh-CN": {"处理摘要", "调用工具", "本次模型", "工具", "未成功", "结果未知", "部分未成功", "部分结果未知"},
	"en":    {"Processing summary", "Tools used", "Models used", "Tool", "failed", "outcome unknown", "partly failed", "partly unknown"},
	"ug-CN": {"بىر تەرەپ قىلىش خۇلاسىسى", "ئىشلىتىلگەن قوراللار", "بۇ قېتىمقى مودېل", "قورال", "مەغلۇپ", "نەتىجە نامەلۇم", "قىسمەن مەغلۇپ", "قىسمەن نامەلۇم"},
}

var executionSummaryStageLabels = map[string][3]string{
	"image_recognition":    {"图片识题", "Image recognition", "رەسىمدىكى سوئالنى تونۇش"},
	"numeric_verification": {"脚本验算", "Script verification", "كود ئارقىلىق تەكشۈرۈش"},
	"grading":              {"作业批改", "Homework grading", "تاپشۇرۇق تەكشۈرۈش"},
	"parent_guide":         {"家长指南", "Parent guide", "ئاتا-ئانىلار يېتەكچىسى"},
	"image_annotation":     {"原图批注", "Image annotation", "ئەسلى رەسىمگە ئىزاھلاش"},
	"answer_generation":    {"答案生成", "Answer generation", "جاۋاب ھاسىل قىلىش"},
	"question_solving":     {"题目求解", "Problem solving", "سوئال يېشىش"},
	"document_generation":  {"文件生成", "Document generation", "ھۆججەت ھاسىل قىلىش"},
}

// RenderExecutionSummary 生成紧凑 Markdown 尾注；缺少事实的栏目省略，模型始终在末尾。
// 只有明确已执行的终态进入投影，加载上下文和待执行计划不是调用事实。
func RenderExecutionSummary(locale string, facts ExecutionSummary) string {
	locale = executionSummaryLocale(locale)
	labels := executionSummaryLocaleLabels[locale]
	colon, separator, statusOpen, statusClose := "：", "；", "（", "）"
	if locale != "zh-CN" {
		colon, separator, statusOpen, statusClose = ": ", "; ", " (", ")"
	}
	lines := make([]string, 0, 3)
	steps := make([]executionSummaryItem, 0, len(facts.Steps))
	for _, step := range facts.Steps {
		name := executionSummaryStageName(locale, step.Key)
		if name != "" {
			steps = mergeExecutionSummaryItem(steps, step.Key, name, step.Status)
		}
	}
	if visible := renderExecutionSummaryItems(steps, labels, " → ", statusOpen, statusClose); visible != "" {
		lines = append(lines, "**"+labels.process+"**"+colon+visible)
	}
	toolItems := make([]executionSummaryItem, 0, len(facts.Tools))
	for _, tool := range facts.Tools {
		kind := strings.ToLower(strings.TrimSpace(tool.Kind))
		name := executionSummaryName(tool.DisplayNames[locale])
		if name == "" {
			name = executionSummaryName(tool.DisplayName)
		}
		if name == "" {
			name = executionSummaryName(tool.Name)
		}
		if name == "" {
			continue
		}
		var prefix string
		switch kind {
		case "skill":
			prefix = "Skill"
		case "mcp":
			prefix = "MCP"
			if server := executionSummaryName(tool.ServerName); server != "" {
				name = server + " / " + name
			}
		case "tool":
			prefix = labels.tool
		default:
			continue
		}
		identity := kind + "\x00" + strings.TrimSpace(tool.ServerName) + "\x00" + strings.TrimSpace(tool.Name)
		toolItems = mergeExecutionSummaryItem(toolItems, identity, prefix+" · "+name, tool.Status)
	}
	if visible := renderExecutionSummaryItems(toolItems, labels, separator, statusOpen, statusClose); visible != "" {
		lines = append(lines, "**"+labels.tools+"**"+colon+visible)
	}
	models := make([]executionSummaryModelItem, 0, len(facts.Models))
	for _, model := range facts.Models {
		name := executionSummaryName(model.Model)
		if name == "" || strings.EqualFold(strings.TrimSpace(model.Model), "auto") {
			continue
		}
		provider := executionSummaryName(model.Provider)
		identity := strings.TrimSpace(model.Provider) + "\x00" + strings.TrimSpace(model.Model)
		found := false
		stage := executionSummaryStageName(locale, model.Stage)
		for i := range models {
			if models[i].identity == identity {
				models[i].stages = appendExecutionSummaryUnique(models[i].stages, stage)
				found = true
				break
			}
		}
		if !found {
			models = append(models, executionSummaryModelItem{identity: identity, name: name, provider: provider,
				stages: appendExecutionSummaryUnique(nil, stage)})
		}
	}
	if len(models) > 0 {
		visible := make([]string, 0, len(models))
		for _, model := range models {
			name := model.name
			if model.provider != "" {
				name = model.provider + " / " + name
			}
			if len(models) > 1 && len(model.stages) > 0 {
				name = strings.Join(model.stages, " / ") + colon + name
			}
			visible = append(visible, name)
		}
		lines = append(lines, "**"+labels.models+"**"+colon+strings.Join(visible, separator))
	}
	return strings.Join(lines, "\n\n")
}

type executionSummaryItem struct {
	identity, name           string
	success, failed, unknown bool
}

type executionSummaryModelItem struct {
	identity, name, provider string
	stages                   []string
}

func mergeExecutionSummaryItem(items []executionSummaryItem, identity, name, status string) []executionSummaryItem {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "success" && status != "error" && status != "failed" && status != "unknown" {
		return items
	}
	index := -1
	for i := range items {
		if items[i].identity == identity {
			index = i
			break
		}
	}
	if index < 0 {
		items = append(items, executionSummaryItem{identity: identity, name: name})
		index = len(items) - 1
	}
	switch status {
	case "success":
		items[index].success = true
	case "error", "failed":
		items[index].failed = true
	case "unknown":
		items[index].unknown = true
	}
	return items
}

func renderExecutionSummaryItems(items []executionSummaryItem, labels executionSummaryLabels, separator, statusOpen, statusClose string) string {
	visible := make([]string, 0, len(items))
	for _, item := range items {
		name := item.name
		var status string
		if item.unknown {
			status = labels.unknown
			if item.success || item.failed {
				status = labels.partialUnknown
			}
		} else if item.failed {
			status = labels.failed
			if item.success {
				status = labels.partialFailed
			}
		}
		if status != "" {
			name += statusOpen + status + statusClose
		}
		visible = append(visible, name)
	}
	return strings.Join(visible, separator)
}

func executionSummaryLocale(locale string) string {
	locale = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	if locale == "zh" || strings.HasPrefix(locale, "zh-") {
		return "zh-CN"
	}
	if locale == "ug" || strings.HasPrefix(locale, "ug-") {
		return "ug-CN"
	}
	return "en"
}

func executionSummaryStageName(locale, key string) string {
	names, ok := executionSummaryStageLabels[strings.TrimSpace(key)]
	if !ok {
		return ""
	}
	switch locale {
	case "zh-CN":
		return names[0]
	case "ug-CN":
		return names[2]
	default:
		return names[1]
	}
}

func appendExecutionSummaryUnique(items []string, value string) []string {
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func executionSummaryName(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;").Replace(value)
}
