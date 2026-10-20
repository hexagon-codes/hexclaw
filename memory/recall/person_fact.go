package recall

import "strings"

func isPersonSubject(subject string) bool {
	return strings.HasPrefix(strings.TrimSpace(subject), "人物:")
}

// NormalizePersonFact 规范人物属性；旧正文只解析明确的肯定当前事实。
func NormalizePersonFact(fact Entry) Entry {
	if !isPersonSubject(fact.Subject) {
		return fact
	}
	for _, marker := range []string{"如果", "假如", "计划", "准备", "将来", "明年", "下学期", "去年", "以前", "曾经", "过去", "不住", "不再", "不喜欢", "没有", "他说", "她说", "“", "\"", "\n"} {
		if strings.Contains(fact.Content, marker) {
			fact.Attribute, fact.Value = "", ""
			return fact
		}
	}
	attribute := canonicalPersonAttribute(fact.Attribute)
	if attribute != "" && strings.TrimSpace(fact.Value) != "" {
		fact.Attribute, fact.Value = attribute, strings.TrimSpace(fact.Value)
		return fact
	}
	fact.Attribute, fact.Value = "", ""
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(fact.Subject), "人物:"))
	text := strings.TrimSpace(fact.Content)
	if name == "" || !strings.HasPrefix(text, name) {
		return fact
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, name))
	for _, relation := range []struct{ prefix, attribute string }{
		{"现在住在", "current_residence"}, {"目前住在", "current_residence"},
		{"住在", "current_residence"}, {"现居", "current_residence"},
		{"现在读", "grade"}, {"就读", "grade"}, {"读", "grade"},
		{"喜欢", "interest"}, {"爱好是", "interest"},
	} {
		if !strings.HasPrefix(text, relation.prefix) {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(text, relation.prefix)), "。.!！ ")
		if value == "" || strings.ContainsAny(value, "，,；;、：:或和但（）()?") {
			return fact
		}
		if relation.attribute == "grade" && !strings.HasSuffix(value, "年级") {
			return fact
		}
		fact.Attribute, fact.Value = relation.attribute, value
		return fact
	}
	return fact
}

func canonicalPersonAttribute(attribute string) string {
	switch strings.TrimSpace(attribute) {
	case "grade", "年级", "在读年级", "就读年级", "当前年级":
		return "grade"
	case "current_residence", "居住地", "现居地", "当前居住地":
		return "current_residence"
	case "interest", "兴趣", "爱好":
		return "interest"
	default:
		return ""
	}
}

func personValuesConflict(old, fresh Entry) bool {
	a, b := strings.TrimSpace(old.Value), strings.TrimSpace(fresh.Value)
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) || strings.HasSuffix(a, b) || strings.HasSuffix(b, a) {
		return false
	}
	switch fresh.Attribute {
	case "grade":
		return true
	case "current_residence":
		return residenceGranularity(a) == residenceGranularity(b)
	default:
		return false
	}
}

func residenceGranularity(value string) string {
	for _, suffix := range []string{"街道", "小区", "省", "市", "区", "县", "镇", "村", "路", "号"} {
		if strings.Contains(value, suffix) {
			return suffix
		}
	}
	return "place"
}
