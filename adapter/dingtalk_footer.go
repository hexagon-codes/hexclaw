package adapter

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

// DingTalkReplyFooter 只用本次回执生成渠道尾注，规范正文和历史投递不受影响。
func DingTalkReplyFooter(reply *Reply, locale string, models ...ExecutionSummaryModel) string {
	return renderDingTalkReplyFooter(reply, locale, models, nil)
}

func renderDingTalkReplyFooter(reply *Reply, locale string, models []ExecutionSummaryModel, displayNames map[string]string) string {
	if reply == nil {
		return ""
	}
	labels := [6]string{"本次工具：", "本次技能：", "本次模型：", "部分成功", "结果未知", "未知"}
	if locale != "" && locale != "und" && !strings.HasPrefix(locale, "zh") {
		labels = [6]string{"Tools used:", "Skills used:", "Model used:", "partly successful", "outcome unknown", "unknown"}
		if strings.HasPrefix(locale, "ug") {
			labels = [6]string{"ئىشلىتىلگەن قوراللار:", "ئىشلىتىلگەن ماھارەتلەر:", "بۇ قېتىمقى مودېل:", "قىسمەن مۇۋەپپەقىيەتلىك", "نەتىجە نامەلۇم", "نامەلۇم"}
		}
	}
	type item struct {
		identity, name string
		outcomes       map[string]string
	}
	var tools []item
	var skills []string
	seenSkills := make(map[string]bool)
	addSkill := func(identity, name string) {
		if strings.TrimSpace(name) != "" && !seenSkills[identity] {
			seenSkills[identity] = true
			skills = append(skills, mathtext.EscapeMarkdownInline(name))
		}
	}
	for index, call := range reply.ToolCalls {
		identity, name := call.Name, call.Name
		kind, server := "tool", ""
		if call.Origin != nil {
			kind, server = call.Origin.Kind, call.Origin.ServerName
			if call.Origin.Name != "" {
				name = call.Origin.Name
			}
			identity = kind + "\x00" + server + "\x00" + name
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		base := toolBaseName(name)
		_, functionalTool := toolDisplayNames[base]
		if kind == "skill" && !functionalTool && base != "knowledge_search" {
			display := toolDisplayName(name)
			if base == "solve" {
				display = "解题"
			}
			if localized := displayNames[identity]; localized != "" {
				display = localized
			}
			addSkill(name, display)
			continue
		}
		display := toolDisplayName(name)
		switch base {
		case "solve":
			display = "解题"
		case "knowledge_search":
			display = "知识库检索"
		case "read_file", "read_text_file", "read_media_file", "read_multiple_files":
			display = "文件读取"
		case "list_directory", "directory_tree", "search_files":
			display = "文件查找"
		case "write_file", "edit_file", "move_file", "create_directory":
			display = "文件管理"
		}
		if localized := displayNames[identity]; localized != "" {
			display = localized
		}
		if kind == "mcp" && server != "" {
			display = server + " / " + display
		}
		position := -1
		for i := range tools {
			if tools[i].identity == identity {
				position = i
				break
			}
		}
		if position < 0 {
			tools = append(tools, item{identity: identity, name: display, outcomes: make(map[string]string)})
			position = len(tools) - 1
		}
		outcome := call.Status
		if call.Detail != nil && call.Detail.Outcome != "" {
			outcome = call.Detail.Outcome
		}
		// 仅相同调用ID的后续回执可覆盖前态，不把同参数独立请求猜成重试。
		action := "id:" + call.ID
		if call.ID == "" {
			action = "position:" + strconv.Itoa(index)
		}
		tools[position].outcomes[action] = outcome
	}
	var adopted []struct {
		Name         string            `json:"name"`
		DisplayName  string            `json:"display_name"`
		DisplayNames map[string]string `json:"display_names"`
		Status       string            `json:"status"`
	}
	if json.Unmarshal([]byte(reply.Metadata["teaching_context_receipts"]), &adopted) == nil {
		for _, receipt := range adopted {
			// 此回执只在讲解完成后按学科选择；普通loaded上下文不进入本栏目。
			if receipt.Status != "adopted" && receipt.Status != "loaded" {
				continue
			}
			name := receipt.DisplayName
			if localized := receipt.DisplayNames[locale]; localized != "" {
				name = localized
			}
			if name == "" {
				name = receipt.Name
			}
			addSkill(receipt.Name, name)
		}
	}
	var lines []string
	if len(tools) > 0 {
		var values []string
		for _, tool := range tools {
			var success, failed, unknown bool
			for _, outcome := range tool.outcomes {
				switch outcome {
				case "success", "succeeded":
					success = true
				case "error", "failed", "failure":
					failed = true
				default:
					unknown = true
				}
			}
			status := "🟢"
			switch {
			case unknown:
				status = "⚪ " + labels[4]
			case success && failed:
				status = "🟡 " + labels[3]
			case failed:
				status = "🔴"
			}
			values = append(values, mathtext.EscapeMarkdownInline(tool.name)+" "+status)
		}
		lines = append(lines, "**"+labels[0]+"** "+strings.Join(values, " · "))
	}
	if len(skills) > 0 {
		lines = append(lines, "**"+labels[1]+"** "+strings.Join(skills, " · "))
	}
	model, provider := reply.Metadata["model"], reply.Metadata["provider_display_name"]
	if provider == "" {
		provider = reply.Metadata["provider"]
	}
	if reply.Usage != nil {
		if model == "" {
			model = reply.Usage.Model
		}
		if provider == "" && model == reply.Usage.Model {
			provider = reply.Usage.Provider
		}
	}
	if len(models) == 0 {
		models = []ExecutionSummaryModel{{Provider: provider, ProviderInstanceID: reply.Metadata["provider_instance_id"], Model: model}}
	}
	var visibleModels []string
	seenModels := make(map[string]bool)
	for _, receipt := range models {
		model, provider = receipt.Model, receipt.ProviderDisplayName
		if provider == "" {
			provider = receipt.Provider
		}
		if strings.TrimSpace(model) == "" || strings.EqualFold(strings.TrimSpace(model), "auto") {
			model = labels[5]
		}
		identity := receipt.ProviderInstanceID + "\x00" + receipt.Provider + "\x00" + model
		if seenModels[identity] {
			continue
		}
		seenModels[identity] = true
		visible := mathtext.EscapeMarkdownInline(model)
		if strings.HasPrefix(locale, "ug") || strings.ContainsFunc(provider, func(r rune) bool {
			return unicode.Is(unicode.Arabic, r) || unicode.Is(unicode.Hebrew, r)
		}) {
			visible = "\u2066" + visible + "\u2069"
			if provider != "" {
				provider = "\u2068" + provider + "\u2069"
			}
		}
		visible = messagecontent.FormatModelIdentity(mathtext.EscapeMarkdownInline(provider), visible)
		visibleModels = append(visibleModels, visible)
	}
	lines = append(lines, "**"+labels[2]+"** "+strings.Join(visibleModels, "；"))
	return "---\n\n" + strings.Join(lines, "  \n")
}

// DingTalkExecutionSummaryFooter 将异步任务的同源执行回执接到统一尾注，不重算历史投递。
func DingTalkExecutionSummaryFooter(locale string, facts ExecutionSummary) string {
	summary := messagecontent.RenderExecutionSummary(locale, facts)
	if summary == "" {
		return ""
	}
	return "---\n\n" + summary
}
