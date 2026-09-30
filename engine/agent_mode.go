// agent_mode.go 提供通用的 Agent 策略模式（领域无关；场景包按需注入领域特性词）。
//
// 设计选择：HexClaw.ReActEngine 已是完整自研工具循环引擎（tool cache / budget /
// compactor / streaming / reasoning / multi-provider），不接入 hexagon Agent（那会
// 丢失所有企业级能力）。本模块通过**系统提示差异化**在 ReActEngine 上叠加模式：
//
//   - react          — 默认 ReAct，工具 + 推理交替（现有行为）
//   - plan-execute   — 先规划再执行（数学题 / 多步任务）
//   - reflection     — 答案产出后自我审查（判题 / 作文批改）
//   - tot            — Tree-of-Thought：列多条路径再择优（数学多解 / 开放题）
//   - self-reflect   — 强自查：每个关键步骤后立即审视（语文阅读 / 论证）
//   - mem-augmented  — 检索个性化档案优先，再作答（个性化助手）
//   - debate         — 双视角对辩后裁决（争议性判题 / 作文打分）
//   - auto           — 按消息启发式路由到上面具体模式
//
// 设计取舍：lightweight prompt-overlay 而非 hexagon agent factory。这样能在保留
// ReActEngine 全部能力（tool / streaming / budget / reasoning）的同时提供
// 7 种通用策略。重型模式（多分支并行 / 多 agent 实例）需 v0.5.x 后扩展。
package engine

import (
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/hexagon-codes/hexclaw/skill"
)

// AgentMode Agent 策略模式。
type AgentMode string

const (
	ModeReAct        AgentMode = "react"
	ModePlanExecute  AgentMode = "plan-execute"
	ModeReflection   AgentMode = "reflection"
	ModeToT          AgentMode = "tot"
	ModeSelfReflect  AgentMode = "self-reflect"
	ModeMemAugmented AgentMode = "mem-augmented"
	ModeDebate       AgentMode = "debate"
	ModeAuto         AgentMode = "auto"
)

// IsValid 检查是否合法枚举值。
func (m AgentMode) IsValid() bool {
	switch m {
	case ModeReAct, ModePlanExecute, ModeReflection,
		ModeToT, ModeSelfReflect, ModeMemAugmented, ModeDebate,
		ModeAuto:
		return true
	}
	return false
}

// DecodeMode 从 raw 字符串解码，未知或空返回 ModeAuto。
func DecodeMode(raw string) AgentMode {
	m := AgentMode(strings.TrimSpace(strings.ToLower(raw)))
	if m.IsValid() {
		return m
	}
	return ModeAuto
}

// modePromptPrefix 返回每种模式附加到 system prompt 的指令。
// 注意："react" 和 "auto" 在被路由解析后返回 "" —— 即不附加任何额外 prompt，保持默认行为。
func modePromptPrefix(m AgentMode) string {
	switch m {
	case ModePlanExecute:
		return `Plan and complete the steps the task needs, using tools when necessary. Lead the final response with the result, then include the steps needed to understand or use it. Show a brief plan only when a complex task needs progress updates or the user asks for a plan; do not impose a fixed number of steps or numbered process headings.
`
	case ModeReflection:
		return `Before answering, check key conditions, calculations and reasoning. Give the checked result and the evidence needed to understand it, without displaying an internal self-check checklist or a "check passed" statement. Correct any errors you find and clearly state any uncertainty that still affects the conclusion.
`
	case ModeToT:
		return `Compare suitable approaches before giving a reliable result and the necessary steps. When the user asks for multiple solutions, explain each solution fully and identify meaningful differences. For ordinary questions, do not require two displayed solutions, a comparison section or a statement that both approaches agree.
`
	case ModeSelfReflect:
		return `Examine assumptions and key reasoning, correct any issues you find, and answer directly. Keep the derivation needed to understand the answer and identify unresolved gaps. Do not impose sections for initial thoughts, reflection or revision, or add a "reflection passed" statement.
`
	case ModeMemAugmented:
		return `Use relevant profile and historical information from the provided memory and retrieved context to answer naturally within the current user, course and task scope. Do not impose profile-recall or personalization sections or repeat the profile verbatim. Explain missing profile information only when it materially affects the current result; never infer facts from missing information.
`
	case ModeDebate:
		return `For a disputed question, lead with the conclusion and its key evidence. When the user explicitly asks for a debate or multiple perspectives, explain the relevant positions and meaningful differences fully. If the issue cannot be resolved, say that no conclusion is established and identify the missing information. Do not impose sections for an affirmative side, an opposing side, a verdict and a final answer.
`
	default:
		return ""
	}
}

// modeKeywordMatcher 场景包注入的"某 mode 的领域特性词"匹配器（清债 P5）。
//
// engine 不再硬编码 K12 领域词（错题/复习/备考/我孩子…），改由场景包（scenarios/k12）通过
// ModeFeatureRegistry 提供，engine 在**原路由位置**消费（保持 AutoRoute 优先级不变）。
// nil 时无场景包特性，仅走内置通用关键词（AP-1：删掉场景包后平台仍是干净通用路由）。
//
// BUG-20260710-H2：原子存取——注入发生在 composition root 装配期，而本地预热
// goroutine（StartLocalWarmup→ResolveMode→packMatches）可能并发读；裸包级变量
// 构成 data race（go test -race 可检出）。
var modeKeywordMatcher atomic.Pointer[func(mode AgentMode, text string) bool]

// SetModeKeywordMatcher 由 composition root 注入（适配场景包的 ModeFeatureRegistry.MatchesMode）。
// 传 nil 清除注入（测试还原用）。
func SetModeKeywordMatcher(m func(mode AgentMode, text string) bool) {
	if m == nil {
		modeKeywordMatcher.Store(nil)
		return
	}
	modeKeywordMatcher.Store(&m)
}

// packMatches 查询场景包是否为 mode 提供了命中 s 的特性词。
func packMatches(mode AgentMode, s string) bool {
	m := modeKeywordMatcher.Load()
	return m != nil && (*m)(mode, s)
}

// AutoRoute 对用户消息做启发式分类，返回推荐的 AgentMode。
//
// 分类规则（优先级从上到下；领域特性词由场景包补充）：
//  1. 双答案争议 / 打分类 → ModeDebate（双视角辩论）
//  2. 多解探索（"几种方法"/"还有别的解法"） → ModeToT
//  3. 个性化档案（"我之前"/"我孩子"/"以前错过") → ModeMemAugmented
//  4. 数学 / 多步计算 / 规划类 → ModePlanExecute（先列步骤再算）
//  5. 判题 / 对错争议 → ModeReflection（自我审查答案）
//  6. 兜底 → ModeReAct
//
// 返回值为具体模式（不返回 ModeAuto）。
func AutoRoute(userText string) AgentMode {
	text := strings.ToLower(userText)
	if containsDebateFeatures(text) {
		return ModeDebate
	}
	if containsToTFeatures(text) {
		return ModeToT
	}
	if containsMemoryFeatures(text) {
		return ModeMemAugmented
	}
	if containsMathFeatures(text) || containsPlanFeatures(text) {
		return ModePlanExecute
	}
	if containsJudgeFeatures(text) {
		return ModeReflection
	}
	return ModeReAct
}

var mathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\d+\s*[\+\-\*\/×÷=]\s*\d+`),
	regexp.MustCompile(`\b(函数|方程|几何|求解|多少|等于|算式|计算)\b`),
	regexp.MustCompile(`\$[^$]+\$`), // LaTeX 公式
}

func containsMathFeatures(s string) bool {
	for _, p := range mathPatterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

func containsPlanFeatures(s string) bool {
	// 通用规划词（engine 内置，领域无关）。K12 的"复习/备考"由场景包提供（清债 P5）。
	keywords := []string{"计划", "安排", "一周", "一个月", "时间表", "plan", "schedule"}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return packMatches(ModePlanExecute, s)
}

func containsJudgeFeatures(s string) bool {
	keywords := []string{"对不对", "对吗", "答案是", "正确吗", "哪个对", "判断下", "批改", "review", "correct"}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// containsDebateFeatures 双答案争议 / 双视角打分类
func containsDebateFeatures(s string) bool {
	keywords := []string{"两个答案", "都说", "争议", "有人说", "到底哪个", "评分", "打分", "评分理由", "辩一辩"}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// containsToTFeatures 多解探索类
func containsToTFeatures(s string) bool {
	keywords := []string{"几种方法", "几种解法", "多种思路", "另一种解法", "还有别的", "其他思路", "多个角度"}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// containsMemoryFeatures 个性化档案类
func containsMemoryFeatures(s string) bool {
	// 通用个性化词（engine 内置）。K12 的"我孩子/错题本/以前错过/之前那道"由场景包提供（清债 P5）。
	keywords := []string{"我之前", "上次", "我家"}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return packMatches(ModeMemAugmented, s)
}

// ResolveMode 将 meta 里的 agent_mode 解析为最终具体模式。
// mode="auto" 会基于 userText 启发式路由；显式指定则直接返回。
//
// 注意：这是无 Skill 上下文版本，不能利用 Skill.PreferredMode 覆盖路由。
// 调用方若有 Skill registry，请改用 ResolveModeWithSkillHint。
func ResolveMode(rawMode, userText string) AgentMode {
	m := DecodeMode(rawMode)
	if m == ModeAuto {
		return AutoRoute(userText)
	}
	return m
}

// ResolveModeWithSkillHint 在 ResolveMode 基础上让"Skill 声明的 PreferredMode"
// 优先级最高（B2 DoD 第二条）。
//
// 路由优先级（从高到低）：
//  1. 显式指定的非 auto rawMode（用户在 Settings 选了具体模式）
//  2. Top-1 召回 Skill 的 frontmatter `preferred_mode`（YAML 声明）
//  3. AutoRoute 启发式分类
//
// registry==nil 或 query=="" 时退化到 ResolveMode。
func ResolveModeWithSkillHint(rawMode, userText string, registry *skill.DefaultRegistry) AgentMode {
	m := DecodeMode(rawMode)
	if m != ModeAuto {
		return m
	}
	if registry != nil && strings.TrimSpace(userText) != "" {
		// 用 Top-1 召回；不附加 Activation（避免循环依赖：Activation.Mode 还没决定）
		if hits := registry.SelectByQuery(userText, 1); len(hits) > 0 {
			if pm := skill.PreferredMode(hits[0]); pm != "" {
				if mode := AgentMode(pm); mode.IsValid() && mode != ModeAuto {
					return mode
				}
			}
		}
	}
	return AutoRoute(userText)
}
