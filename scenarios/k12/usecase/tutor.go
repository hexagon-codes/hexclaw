package usecase

import (
	"context"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 辅导编排始终面向家长提供完整参考；渐进提示只描述家长对孩子的讲题方法。
// 情绪信号补充安抚建议，不阻断家长获得经 Solver 验算链取得的可信解。

// TutorStage 保留历史请求阶段值；新响应统一提供完整家长参考。
type TutorStage int

const (
	StageHint1 TutorStage = 1 // 兼容历史方向提示请求
	StageHint2 TutorStage = 2 // 兼容历史方法提示请求
	StageFull  TutorStage = 3 // 完整讲解：分步讲解 + 关键步标注 + 变式题
)

// TutorTurnRequest 一轮辅导输入信号（确定性驱动阶段推进 + 情绪守门）。
type TutorTurnRequest struct {
	AgentName     string     // 归属实例（T2.4：阶段三讲解完成时据此 + 题目定位错题推进 explained）
	PriorStage    TutorStage // 历史请求阶段，不再作为答案可见性门槛
	ParentMessage string     // 家长本轮消息（检测升级/情绪/孩子作答意图）
	StudentAnswer string     // 家长转述的孩子作答（非空 → 至少进阶段二批改）
}

// TutorDirective 一轮辅导的编排结论（喂给上游注入 LLM 提示）。
type TutorDirective struct {
	Stage      TutorStage `json:"stage"`
	Comfort    bool       `json:"comfort"`     // 情绪信号命中：补充安抚建议
	EmotionCue string     `json:"emotion_cue"` // 命中的情绪信号词（可空）
	Escalated  bool       `json:"escalated"`   // 本轮较上轮是否升级
	PromptHint string     `json:"prompt_hint"` // 分阶段（或安抚）行为指令，注入系统提示
}

// 情绪信号用于生成家长可采用的安抚建议。
var emotionCues = []string{
	"哭了", "哭", "生气", "发火", "发脾气", "闹脾气", "烦了", "不耐烦", "急了",
	"不想学", "崩溃", "摔", "闹", "急哭", "委屈", "沮丧",
}

// PlanTutorTurn 自动提供完整家长参考，不要求逐轮解锁答案。
func PlanTutorTurn(req TutorTurnRequest) TutorDirective {
	directive := TutorDirective{
		Stage:      StageFull,
		Escalated:  req.PriorStage != 0 && req.PriorStage < StageFull,
		PromptHint: "向家长提供正确答案、完整解法和讲题方法：分步骤写清楚、标注关键步。讲法包括 1~2 条方向性提示、关键方法或第一步怎么起，以及孩子卡住时怎样拆小步骤；给出家长可以直接使用的追问和检查方法。需要巩固时，沿既有验算链提供一道同知识点的变式题。按当前年级讲解，清晰题目自动解答并验算；只澄清无法辨认或相互矛盾的原题事实，不编造学生作答。内容简洁明确，不输出禁答提示或教学原则声明。",
	}
	if strings.TrimSpace(req.StudentAnswer) != "" {
		directive.PromptHint += " 家长报了孩子的作答：先独立解出正确答案再逐步对比；孩子对了就肯定并追问一句思路，防止蒙对；错了就指出第一个出错的步骤和错因，给出完整订正参考，以及家长可用的引导话术。"
	}
	if cue := firstHit(req.ParentMessage, emotionCues); cue != "" {
		directive.Comfort = true
		directive.EmotionCue = cue
		directive.PromptHint += " 孩子情绪不好时，给家长具体的安抚建议：共情孩子的感受，肯定已经付出的努力，把题目拆成更小的步骤、降低难度，等孩子平静下来再讲。"
	}
	return directive
}

// firstHit 返回 s 中命中的第一个关键词（按 cues 顺序）；无命中返回空串。
func firstHit(s string, cues []string) string {
	for _, c := range cues {
		if strings.Contains(s, c) {
			return c
		}
	}
	return ""
}

// TutorTurnResult 辅导一轮的产物：家长讲题指令与验算过的完整解。
type TutorTurnResult struct {
	Directive TutorDirective
	Solution  string        // 经 Solver 验算链的完整解
	Evidence  SolveEvidence // 验算证据
}

// TutorTurn 在有题目时自动调用 Solver 取得完整解，不以提示阶段或情绪作为门槛。
func (d Deps) TutorTurn(ctx context.Context, req TutorTurnRequest, problem, grade string) (TutorTurnResult, error) {
	if err := validateGradeInput(grade); err != nil {
		return TutorTurnResult{}, err
	}
	dir := PlanTutorTurn(req)
	res := TutorTurnResult{Directive: dir}
	if strings.TrimSpace(problem) != "" && d.Solver != nil {
		subject, mistake := d.tutorQuestionContext(ctx, req, problem)
		input := GradeRequest{AgentName: req.AgentName, Grade: grade, Problem: problem, Subject: subject, SubjectUnresolved: subject == ""}
		solution, evidence, err := d.solveTutorQuestion(ctx, input)
		if err != nil {
			return res, err
		}
		res.Solution, res.Evidence = solution, evidence
		// T2.4：完整讲解完成 → 若该题在错题本(new) 自动推进 explained（PRD §5.3.1）。
		// best-effort：推进失败不影响辅导产物（讲解已给出）。
		if mistake != nil && solution != "" && evidence.Verdict != VerdictOutOfScope {
			_ = d.Records.UpdateStatus(ctx, mistake.RecordID, k12.StatusExplained, mistake.DueAt, mistake.Version)
		}
	}
	return res, nil
}

// 明示的学科和可精确解析的算式可作为学科事实；仅出现在题目故事里的课程名称不足以定科。
var tutorDeclaredSubject = regexp.MustCompile(`(?:^|[：:，,。；;！？!?\n]|这道|一道|这是|本题是|科目为|学科为|讲解|辅导|年级[上下]?册?)(数学|英语|语文|科学|信息科技|物理|化学)(?:题|课|作业|练习|试卷|[：:，,。；;！？!?\n]|$)`)

func tutorQuestionSubject(parentMessage, problem string) (string, bool) {
	subject := ""
	for _, source := range []string{parentMessage, problem} {
		for _, match := range tutorDeclaredSubject.FindAllStringSubmatch(strings.TrimSpace(source), -1) {
			if subject != "" && subject != match[1] {
				return "", false
			}
			subject = match[1]
		}
	}
	if subject != "" {
		return subject, true
	}
	if _, ok := elementarymath.ParseUniformTravelProblem(problem); ok || parentGuidePureArithmetic(problem) {
		return "数学", true
	}
	return "", true
}

// 用同一孩子、同一题面的记录确定学科和原版本。跨学科同名题只能按已确定的学科匹配。
func (d Deps) tutorQuestionContext(ctx context.Context, req TutorTurnRequest, problem string) (string, *records.AgentRecord) {
	subject, consistent := tutorQuestionSubject(req.ParentMessage, problem)
	if !consistent || d.Records == nil || req.AgentName == "" {
		return subject, nil
	}
	rows, err := d.Records.ListByScope(ctx, req.AgentName, k12.CollectionMistakes, "")
	if err != nil {
		return subject, nil
	}
	want := k12.NormalizeQuestion(problem)
	matches := make(map[string]*records.AgentRecord)
	for _, row := range rows {
		fields, err := k12.ParseMistakeFields(row.Fields)
		if err != nil || row.Status == k12.StatusArchived || k12.NormalizeQuestion(fields.Question) != want {
			continue
		}
		course, err := normalizeSubject(fields.Subject)
		if err != nil {
			continue
		}
		if _, found := matches[course]; !found {
			matches[course] = nil
		}
		if row.Status == k12.StatusNew && matches[course] == nil {
			matches[course] = row
		}
	}
	if subject == "" {
		for course := range matches {
			if course == "" {
				continue
			}
			if subject != "" {
				return "", nil
			}
			subject = course
		}
	}
	if target, found := matches[subject]; found {
		return subject, target
	}
	if len(matches) == 1 {
		return subject, matches[""]
	}
	return subject, nil
}

func (d Deps) solveTutorQuestion(ctx context.Context, input GradeRequest) (string, SolveEvidence, error) {
	if input.AgentName != "" && d.Records != nil && !HasGradingPhysicalCallExecutor(ctx) {
		if origin, ok := d.textSolveOrigin(ctx, input); ok {
			// 辅导回合直接交付同一持久解答和验算证据，打印正文由其独立交付入口投影。
			solved, err := d.solveTextProblem(ctx, origin, input)
			return solved.Solution, solved.Evidence, err
		}
	}
	solved, err := d.solveHomeworkSubject(ctx, input, input.Problem)
	return solved.Solution, solved.Evidence, err
}
