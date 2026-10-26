package skill

import "strings"

// HasPersonaTag 判断技能是否显式声明为会话人设。
func HasPersonaTag(tags []string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), "persona") {
			return true
		}
	}
	return false
}

// IsPersona 只读取人设声明，不启用其他可选的技能筛选能力。
func IsPersona(s Skill) bool {
	declared, ok := s.(interface{ IsPersona() bool })
	return ok && declared.IsPersona()
}
