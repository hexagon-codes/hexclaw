// Package skilladapter 把 K12 用例闭环包成平台通用 skill，注入 engine 工具面。
//
// 为什么要它：IM 群里家长发作业时走 engine 的通用 ReAct + 工具面。平台自带的 solve
// 只解题+验算，**不触发错题入库/学情信号**（那是 K12 领域副作用）。本包提供一个通用
// skill.Skill —— engine 只看见一个普通工具（守 AP-1，engine 不认识 K12）；LLM 调用它
// 即跑完整 K12 批改闭环（solve+批改+错题入库+学情），实例 scope 从 ctx 取（engine 已
// 把已路由 Agent 名 stamp 进 ctx，见 skill.RoutedAgentName），不信 LLM 传的 agent 参数。
package skilladapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon/observe/trace"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

// GradeSkill 是「批改作业并沉淀错题」的 K12 闭环工具。
type GradeSkill struct {
	deps usecase.Deps
}

// NewGradeSkill 建 K12 批改 skill（注入用例依赖）。
func NewGradeSkill(deps usecase.Deps) *GradeSkill {
	return &GradeSkill{deps: deps}
}

func (s *GradeSkill) Name() string      { return "k12_grade" }
func (s *GradeSkill) Match(string) bool { return false } // 只经 LLM 工具调用，不走关键词快路
func (s *GradeSkill) Description() string {
	return "解答或批改一道作业题，返回完整解法与讲解；任何当前助手都可直接调用。已关联学生档案时，真实错误作答自动入该学生错题本；独立解题或批改不创建学生记录。未作答时 student_answer 留空，绝不编造。"
}

func (s *GradeSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("k12_grade",
		"A specialized capability for primary-school homework grading and tutoring reports in the CURRENT assistant. Use it when the user requests grading or a teaching report; ordinary problem solving can use the generic solve tool. The default assistant discovers and combines skills without adopting a tutor's learner workflow. No separate tutor or learner profile is required for standalone use. If an answer was submitted, pass it as student_answer to GRADE it (independently solves & verifies, names the FIRST wrong step + error cause + a guiding hint). "+
			"If the problem is BLANK / UNANSWERED, LEAVE student_answer EMPTY — the tool will SOLVE it and return the full solution + answer + explanation (no grading, nothing logged). NEVER fabricate a student_answer. "+
			"An existing learner profile is used only for the current assistant. Only that learner's submitted mistakes are recorded; standalone use returns the answer or assessment without creating student records. Do NOT create or switch assistants, borrow another learner profile, or pass an agent id. "+
			"For an explicit request to resend the previous PDF, use action=resend_report without repeating the problem. It reads only a saved report in this conversation and never solves or renders again. "+
			"Use action=prepare_report only to retry a previously failed PDF preparation from this conversation. The verified report remains unchanged. report_message_id selects an exact persisted assistant message in this conversation; omit it for the latest report.",
		&llm.Schema{
			Type: "object",
			Properties: map[string]*llm.Schema{
				"action":            {Type: "string", Enum: []any{"grade", "prepare_report", "resend_report"}, Description: "Optional; defaults to grade. Report actions reuse trusted current-conversation history."},
				"report_message_id": {Type: "string", Description: "Optional persisted assistant message ID in the current conversation; only for report actions."},
				"problem":           {Type: "string", Description: "The problem statement."},
				"student_answer":    {Type: "string", Description: "The child's submitted answer or worked steps. LEAVE EMPTY for blank/unanswered problems — do NOT invent one; empty means 'solve & explain' instead of 'grade'."},
				"subject":           {Type: "string", Description: "Optional explicit subject: 数学/语文/英语/科学/信息科技; legacy 物理/化学 values remain accepted."},
				"grade":             {Type: "string", Description: "Optional. Effective grade term (e.g. 五年级上); left empty resolves from the child's profile."},
				"knowledge_points":  {Type: "array", Items: &llm.Schema{Type: "string"}, Description: "Optional knowledge points from recognition."},
				"source_session":    {Type: "string", Description: "Optional source session/chat id for mistake dedup."},
			},
		})
}

// Execute 跑完整 K12 批改闭环。实例 scope 取自 ctx（engine stamp 的已路由 Agent），
// 不信 LLM 的 agent 参数（同 authUserCtxKey 纪律）。
func (s *GradeSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	// BUG-20260710-H1：实例 scope 只认 ctx（engine stamp 的已路由 Agent），绝不回退
	// 采信 LLM 传的 agent 参数——否则幻觉参数可把批改记录/错题写进任意孩子的命名
	// 空间，击穿多孩隔离（同 authUserCtxKey 纪律）。测试用 skill.WithRoutedAgent 构造 ctx。
	invocation := skill.CurrentInvocation(ctx)
	agent := invocation.RoutedAgentName
	// 内置默认助手没有学生实体；可信会话仍提供完整执行身份。
	if agent == "" && invocation.AuthenticatedUserID != "" && invocation.SessionID != "" && invocation.SourceMessageID != "" && invocation.RequestID != "" {
		agent = "default"
		ctx = skill.WithRoutedAgent(ctx, agent)
	}
	if agent == "" {
		return nil, fmt.Errorf("k12_grade: current request identity is unavailable")
	}
	switch action := argStr(args, "action"); action {
	case "", "grade":
	case "prepare_report", "resend_report":
		return s.savedReportResult(ctx, agent, action, argStr(args, "report_message_id"))
	default:
		return nil, fmt.Errorf("k12_grade: unsupported action %q", action)
	}
	problem := argStr(args, "problem")
	if problem == "" {
		return nil, fmt.Errorf("k12_grade: problem 不可空")
	}

	grade := argStr(args, "grade")
	if grade == "" && s.deps.Profiles != nil {
		// grade 不信 LLM：优先从孩子档案取生效年级。
		if p, err := s.deps.GetProfile(ctx, agent); err == nil {
			grade = p.GradeTerm
		}
	}

	knowledgePoints := argStrSlice(args, "knowledge_points")
	trace.L(ctx).Info("K12 skill context resolved", "request_id", invocation.RequestID, "session_id", invocation.SessionID,
		"source_message_id", invocation.SourceMessageID, "resolved_agent", agent, "effective_grade", grade, "grade_from_profile", argStr(args, "grade") == "",
		"subject", argStr(args, "subject"), "has_student_answer", argStr(args, "student_answer") != "")
	res, err := s.deps.GradeHomeworkProblem(ctx, usecase.GradeRequest{
		AgentName:        agent,
		Subject:          argStr(args, "subject"),
		Grade:            grade,
		GradeFromProfile: argStr(args, "grade") == "",
		SourceSession:    argStr(args, "source_session"),
		Problem:          problem,
		StudentAnswer:    argStr(args, "student_answer"),
		KnowledgePoints:  knowledgePoints,
	})
	if err != nil {
		if errors.Is(err, k12storage.ErrTextSolveUnknown) {
			return nil, &skill.ExecutionError{Detail: skill.ToolExecutionDetail{Phase: "text_solve", Outcome: "unknown", Reason: "Query the original solve_run_id; do not start a replacement model request", Action: "post"}, Err: fmt.Errorf("k12_grade: %w", err)}
		}
		return nil, fmt.Errorf("k12_grade: %w", err)
	}

	if res.TeachingReport != nil && !res.OutOfScope {
		content, err := usecase.TeachingReportMarkdown(*res.TeachingReport)
		if err != nil {
			return nil, err
		}
		metadata := gradeMetadata(res, problem, knowledgePoints)
		if res.GradeReportRef != nil {
			return s.prepareReportResult(ctx, agent, res.Finalization, content, metadata, *res.GradeReportRef)
		}
		return s.prepareReportResult(ctx, agent, res.Finalization, content, metadata)
	}
	return &skill.Result{
		Content:  renderGradeContent(res),
		Metadata: gradeMetadata(res, problem, knowledgePoints),
	}, nil
}

// renderGradeContent 面向家长/LLM 的批改/解题文本（含入库提示条）。
func renderGradeContent(res usecase.GradeResult) string {
	if res.OutOfScope {
		return fmt.Sprintf("这道题涉及【%s】，超出孩子当前年级的进度——先确认是不是他的题，或按档案年级讲。", res.OutOfScopeKP)
	}
	// 空白/未作答题：解题分叉，给完整解法与讲解，不评对错、不提入库。
	if res.SolveOnly {
		if strings.TrimSpace(res.Solution) == "" {
			return "这道题孩子还没作答。可以先让他试着写一写，再拍给我；需要的话我也能直接讲解法。"
		}
		return fmt.Sprintf("这道题孩子还没作答，先把解法和答案讲清楚：\n%s\n给家长的引导：让孩子照着思路自己再做一遍。", res.Solution)
	}
	var b strings.Builder
	if strings.TrimSpace(res.Solution) != "" {
		fmt.Fprintf(&b, "Parent reference — answer and complete solution:\n%s\n\n", res.Solution)
	}
	// 判定统一 Verdict 五值（§4.5 布尔删除）：agree=答对。
	if res.Outcome.Verdict == usecase.VerdictAgree {
		b.WriteString("答对了。可以追问一句他的思路，防止蒙对。")
		return b.String()
	}
	fmt.Fprintf(&b, "第一个错步：%s\n错因：%s\n", res.Outcome.WrongStep, res.Outcome.ErrorCause)
	b.WriteString("Parent teaching method: use the complete reference above, explain the first incorrect step, and ask the child to explain and check that step.")
	if res.RecordCreated {
		fmt.Fprintf(&b, "\n（✓ 已记入错题本 · 知识点【%s】· 错因【%s】）", res.Outcome.KnowledgePoint, res.Outcome.ErrorCause)
	}
	return b.String()
}

func gradeMetadata(res usecase.GradeResult, problem string, knowledgePoints []string) map[string]string {
	m := map[string]string{
		// 判定统一 Verdict 五值（§4.5 布尔删除）：k12_correct 布尔键随之退役。
		"k12_verdict":        string(res.Outcome.Verdict),
		"k12_out_of_scope":   boolStr(res.OutOfScope),
		"k12_record_created": boolStr(res.RecordCreated),
		"badge":              res.Evidence.Badge(),
	}
	if res.RecordID != "" {
		m["k12_record_id"] = res.RecordID
	}
	if res.Finalization != nil {
		m["text_solve_run_id"] = res.Finalization.RunID
		m["text_solve_finalization_id"] = res.Finalization.FinalizationID
		m["text_solve_input_digest"] = res.Finalization.InputDigest
		if res.AnswerSource != nil {
			raw, _ := json.Marshal(res.AnswerSource)
			m["answer_source"] = string(raw)
		}
		refs, _ := json.Marshal([]textSolveReplyReference{{RunID: res.Finalization.RunID, FinalizationID: res.Finalization.FinalizationID, FinalizationDigest: res.Finalization.ResultDigest, InputDigest: res.Finalization.InputDigest, AnswerSource: res.AnswerSource, GradeReportRef: res.GradeReportRef}})
		m["text_solve_refs"] = string(refs)
	}
	// BUG-1：判错入库时额外产出前端可消费的结构化 record 元数据（engine 层作为领域中立
	// reply-safe 键透传到消息 metadata.record，前端 messageRecordChip 据 schema 渲染入库徽章）。
	// 领域字面量（错题本集合 id / 字段名）在此 K12 层落定，engine 不认识（守 AP-1）。
	if res.RecordCreated {
		if enc := encodeRecordMeta(res, problem, knowledgePoints); enc != "" {
			m["record"] = enc
		}
	}
	return m
}

// encodeRecordMeta 把入库错题编码成前端 RecordChip 契约（{collection,fields,status} JSON）。
// 知识点回退逻辑对齐 usecase.GradeHomeworkProblem：grader 未产知识点时取识题首个。
func encodeRecordMeta(res usecase.GradeResult, problem string, knowledgePoints []string) string {
	kp := res.Outcome.KnowledgePoint
	if kp == "" && len(knowledgePoints) > 0 {
		kp = knowledgePoints[0]
	}
	payload := struct {
		Collection string            `json:"collection"`
		Fields     map[string]string `json:"fields"`
		Status     string            `json:"status"`
	}{
		Collection: k12.CollectionMistakes,
		Fields: map[string]string{
			"question":        problem,
			"knowledge_point": kp,
			"error_cause":     res.Outcome.ErrorCause,
		},
		Status: k12.StatusNew,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func argStr(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argStrSlice(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
