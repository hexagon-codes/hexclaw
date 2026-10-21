package engine

// AgentDefaultSkillSelector 由场景装配缩小默认正文集合；handled时保留所选完整正文。
// 显式挂载不经过此选择器，非本场景返回handled=false沿用既有行为。
type AgentDefaultSkillSelector func(text string, configured []string) (names []string, handled bool)

func (e *ReActEngine) SetAgentDefaultSkillSelector(selector AgentDefaultSkillSelector) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.agentDefaultSkillSelector = selector
}
