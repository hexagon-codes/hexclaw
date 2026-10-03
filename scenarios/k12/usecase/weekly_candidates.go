package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type WeeklyCandidateCheckpointRef = k12storage.WeeklyCandidateCheckpointRef

var errWeeklyLearningTargetUnavailable = errors.New("weekly learning target evidence unavailable")

// WeeklyPracticeTarget 冻结可核验教学目标，不从章节名称推导知识点。
type WeeklyPracticeTarget struct {
	SourceRef, Subject, GradeTerm, KnowledgePoint, Question string
	SourceRevision                                          int
	EvidenceRefs                                            []string
}

type weeklyCandidateCall struct {
	Status          string                      `json:"status"`
	Result          *SolveResult                `json:"result,omitempty"`
	Error           string                      `json:"error,omitempty"`
	InputDigest     string                      `json:"input_digest,omitempty"`
	PhysicalCalls   []weeklyPhysicalReceipt     `json:"physical_calls,omitempty"`
	PhysicalHistory []weeklyPhysicalReceipt     `json:"physical_history,omitempty"`
	Interpretations []weeklySolveInterpretation `json:"interpretations,omitempty"`
}

type weeklyCandidateStep struct {
	Target                int                          `json:"target"`
	Candidate             *WeeklyPracticeCandidate     `json:"candidate,omitempty"`
	Generate              weeklyCandidateCall          `json:"generate"`
	Solve                 weeklyCandidateCall          `json:"solve"`
	SolveRecoveryAttempts []weeklySolveRecoveryAttempt `json:"solve_recovery_attempts,omitempty"`
}

type weeklyCandidateGeneration struct {
	Route k12.GradingModelSnapshot `json:"route"`
	Items []weeklyCandidateStep    `json:"items"`
}

type weeklyPracticeCandidateSource struct{ deps *Deps }

func NewWeeklyPracticeCandidateSource(deps *Deps) WeeklyPracticeCandidateSource {
	return &weeklyPracticeCandidateSource{deps: deps}
}

type weeklyCandidateRequestFreezer interface {
	FreezeWeeklyPracticeCandidateRequest(context.Context, WeeklyPracticeCandidateRequest) (WeeklyPracticeCandidateRequest, error)
}

func (d Deps) prepareWeeklyCandidateCommand(ctx context.Context, request WeeklyPracticeCandidateRequest,
	plan k12.WeeklyPracticePlan, kind, key, digest string) (WeeklyPracticeCandidateRequest, error) {
	freezer, production := d.WeeklyCandidates.(weeklyCandidateRequestFreezer)
	if !production {
		return request, nil
	}
	ref := WeeklyCandidateCheckpointRef{AgentName: request.AgentName, Kind: kind, PlanID: plan.PlanID, Revision: plan.Revision, IdempotencyKey: key}
	if stored, err := d.Records.GetWeeklyCandidateCheckpoint(ctx, ref); err == nil {
		if err := json.Unmarshal([]byte(stored), &request); err != nil {
			return request, err
		}
	} else if !errors.Is(err, records.ErrNotFound) {
		return request, err
	} else if request.Generation == nil {
		var err error
		request, err = freezer.FreezeWeeklyPracticeCandidateRequest(ctx, request)
		if err != nil {
			return request, err
		}
	}
	request.Checkpoint = ref
	raw, err := json.Marshal(request)
	if err != nil {
		return request, err
	}
	stored, err := d.Records.PrepareWeeklyCandidateCommand(ctx, ref, digest, plan, string(raw), d.now())
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal([]byte(stored), &request); err != nil {
		return request, err
	}
	return request, nil
}

func (s *weeklyPracticeCandidateSource) FreezeWeeklyPracticeCandidateRequest(ctx context.Context, request WeeklyPracticeCandidateRequest) (WeeklyPracticeCandidateRequest, error) {
	profile, err := s.deps.GetProfileWithRevision(ctx, request.AgentName)
	if err != nil {
		return request, err
	}
	request.Targets = nil
	request.GradeTerm, request.Textbook, request.ProfileRevision = profile.GradeTerm, profile.SubjectTextbooks.Math, profile.Revision
	if request.Textbook == "" {
		request.Textbook = profile.TextbookEdition
	}
	if request.GradeTerm == "" {
		return request, fmt.Errorf("%w: learner grade unavailable", errWeeklyLearningTargetUnavailable)
	}
	sources, err := s.deps.Records.ListWeeklyCandidateTargetSources(ctx, s.deps.TextbookOwnerID, request.AgentName, request.GradeTerm)
	if err != nil {
		return request, err
	}
	targetProgress := request.Progress
	if request.PlanSection == k12.WeeklySectionTextbookConsolidation &&
		(targetProgress.VerifiedPageFrom == nil || targetProgress.VerifiedPageTo == nil) && targetProgress.TextbookManifestID != "" {
		catalog, catalogErr := s.deps.Records.GetTextbookManifestCatalog(ctx, k12storage.TextbookScope{
			OwnerID: s.deps.TextbookOwnerID, AgentName: request.AgentName, Subject: "math",
		}, targetProgress.TextbookManifestID)
		if catalogErr != nil {
			return request, catalogErr
		}
		for _, unit := range catalog.Units {
			if unit.UnitID != targetProgress.UnitID {
				continue
			}
			from, to := unit.PageFrom, unit.PageTo
			if targetProgress.LessonID != "" {
				from, to = 0, 0
				for _, lesson := range unit.Lessons {
					if lesson.LessonID == targetProgress.LessonID {
						from, to = lesson.PageFrom, lesson.PageTo
					}
				}
			}
			if from > 0 && to >= from {
				targetProgress.VerifiedPageFrom, targetProgress.VerifiedPageTo = &from, &to
			}
		}
	}
	seen := map[string]bool{}
	for _, source := range sources {
		point := strings.TrimSpace(source.KnowledgePoint)
		if source.Question == "" || point == "" || point == "其他" || seen[point] {
			continue
		}
		evidence := []string{source.SourceRef}
		switch request.PlanSection {
		case k12.WeeklySectionArithmeticWarmup:
			if !weeklyArithmeticExpression(source.Question) {
				continue
			}
		case k12.WeeklySectionTextbookConsolidation:
			_, grounding, _, decodeErr := decodeGroundedPhysicalPayload(source.GroundingResultJSON, nil)
			if decodeErr != nil || grounding == nil || !weeklyGroundingMatchesProgress(*grounding, targetProgress) {
				continue
			}
			for _, receipt := range grounding.Receipts {
				evidence = append(evidence, receipt.CitationDigest)
			}
		default:
			return request, fmt.Errorf("unsupported weekly candidate section")
		}
		seen[point] = true
		request.Targets = append(request.Targets, WeeklyPracticeTarget{SourceRef: source.SourceRef, Subject: source.Subject,
			GradeTerm: source.GradeTerm, KnowledgePoint: point, Question: source.Question,
			SourceRevision: source.SourceRevision, EvidenceRefs: evidence})
	}
	if len(request.Targets) == 0 {
		if request.PlanSection == k12.WeeklySectionTextbookConsolidation {
			pages, pageErr := s.deps.Records.ListWeeklyTextbookPages(ctx, k12storage.TextbookScope{
				OwnerID: s.deps.TextbookOwnerID, AgentName: request.AgentName, Subject: "math",
			}, targetProgress)
			if pageErr != nil {
				return request, pageErr
			}
			for _, page := range pages {
				point, body := weeklyTextbookLesson(page.Content, targetProgress.UnitTitle)
				if point == "" || body == "" || seen[point] {
					continue
				}
				seen[point] = true
				request.Targets = append(request.Targets, WeeklyPracticeTarget{SourceRef: page.SourceRef,
					Subject: "数学", GradeTerm: request.GradeTerm, KnowledgePoint: point, Question: body,
					SourceRevision: targetProgress.Revision, EvidenceRefs: page.EvidenceRefs})
			}
			request.Progress = targetProgress
		}
	}
	if len(request.Targets) == 0 {
		return request, errWeeklyLearningTargetUnavailable
	}
	request.Generation = &weeklyCandidateGeneration{}
	return request, nil
}

// weeklyTextbookLesson 从原页的显式课时标题取教学目标，不把章节或练习栏目猜成知识点。
func weeklyTextbookLesson(content, unitTitle string) (string, string) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		if title == "" || title == unitTitle || strings.Trim(title, "0123456789０１２３４５６７８９.、 ") == "" {
			continue
		}
		if strings.HasPrefix(title, "练习") {
			continue
		}
		switch title {
		case "做一做", "练一练", "练习", "练习题", "例题", "思考", "讨论", "拓展", "整理与复习":
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "## ") && !strings.HasPrefix(strings.TrimSpace(lines[end]), "# ") {
			end++
		}
		body := strings.TrimSpace(strings.Join(lines[i+1:end], "\n"))
		if body != "" {
			return title, body
		}
	}
	return "", ""
}

func weeklyGroundingMatchesProgress(grounding gradingStoredGrounding, progress k12.CurriculumProgress) bool {
	if progress.TextbookBindingID == "" || progress.TextbookManifestID == "" ||
		grounding.Snapshot.TextbookBindingID != progress.TextbookBindingID || grounding.Snapshot.TextbookManifestID != progress.TextbookManifestID {
		return false
	}
	// 目录段标识与检索 chunk 不同；课程对应关系由同一 manifest 的确切页范围证明。
	for _, receipt := range grounding.Receipts {
		if progress.VerifiedPageFrom != nil && progress.VerifiedPageTo != nil &&
			receipt.LogicalPage >= *progress.VerifiedPageFrom && receipt.LogicalPage <= *progress.VerifiedPageTo {
			return true
		}
	}
	return false
}

var weeklyArithmeticPattern = regexp.MustCompile(`^[0-9.()+*/−\-]+$`)

func weeklyArithmeticExpression(question string) bool {
	question = strings.NewReplacer(`\(`, "", `\)`, "", `\div`, "/", `\times`, "*", "÷", "/", "×", "*", "？", "?", "＝", "=").Replace(question)
	question = strings.Join(strings.Fields(question), "")
	// 指令前缀只影响算式资格判断，展示与独立验算仍使用原题面。
	question = strings.TrimPrefix(strings.TrimPrefix(question, "计算："), "计算:")
	if parts := strings.Split(question, "="); len(parts) == 2 && (parts[1] == "" || parts[1] == "?") {
		question = parts[0]
	} else if len(parts) != 1 {
		return false
	}
	return weeklyArithmeticPattern.MatchString(question) && strings.ContainsAny(question, "+-−*/")
}

func (s *weeklyPracticeCandidateSource) GenerateWeeklyPracticeCandidates(ctx context.Context, requested WeeklyPracticeCandidateRequest) ([]WeeklyPracticeCandidate, error) {
	d := s.deps
	raw, err := d.Records.GetWeeklyCandidateCheckpoint(ctx, requested.Checkpoint)
	if err != nil {
		return nil, err
	}
	var request WeeklyPracticeCandidateRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return nil, err
	}
	if request.AgentName != requested.AgentName || request.MaxItems != requested.MaxItems || len(request.Targets) == 0 || request.Generation == nil {
		return nil, fmt.Errorf("weekly frozen target unavailable")
	}
	request.Checkpoint = requested.Checkpoint
	for _, step := range request.Generation.Items {
		if len(step.SolveRecoveryAttempts) > 0 || len(step.Solve.Interpretations) > 0 {
			if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
				return nil, err
			}
			break
		}
	}
	// 冻结内容仍保留；尚待生成的教材命令不能使用已失效的文件代次。
	for _, target := range request.Targets {
		if !strings.HasPrefix(target.SourceRef, "textbook:") {
			continue
		}
		pages, err := d.Records.ListWeeklyTextbookPages(ctx, k12storage.TextbookScope{
			OwnerID: d.TextbookOwnerID, AgentName: request.AgentName, Subject: "math",
		}, request.Progress)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, page := range pages {
			matched = matched || page.SourceRef == target.SourceRef
		}
		if !matched {
			return nil, fmt.Errorf("weekly textbook source changed")
		}
	}
	saveCtx := ctx
	save := func() error {
		next, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if err := d.Records.SaveWeeklyCandidateCheckpoint(saveCtx, requested.Checkpoint, raw, string(next)); err != nil {
			return err
		}
		raw = string(next)
		return nil
	}
	excluded, err := d.Records.WeeklyPracticeProblemHashes(ctx, request.AgentName)
	if err != nil {
		return nil, err
	}
	for _, target := range request.Targets {
		hash, _, _ := k12.StablePracticeProblemHash(k12.PracticeCandidateProblem{Subject: target.Subject, QuestionMarkdown: target.Question})
		excluded = append(excluded, hash)
	}
	var output []WeeklyPracticeCandidate
	for index := 0; index < request.MaxItems; index++ {
		if len(request.Generation.Items) <= index {
			request.Generation.Items = append(request.Generation.Items, weeklyCandidateStep{Target: index % len(request.Targets)})
		}
		step := &request.Generation.Items[index]
		if step.Target < 0 || step.Target >= len(request.Targets) {
			return nil, fmt.Errorf("invalid weekly target checkpoint")
		}
		target := request.Targets[step.Target]
		if step.Candidate != nil && step.Candidate.AssetSource != nil {
			current, checkErr := d.Records.FindPracticeProblemAsset(ctx, k12.PracticeAssetQuery{OwnerID: d.TextbookOwnerID, AgentName: request.AgentName,
				Subject: target.Subject, GradeTerm: request.GradeTerm, KnowledgePoint: target.KnowledgePoint, OriginalQuestion: target.Question, ExcludedHashes: excluded})
			if checkErr != nil && !errors.Is(checkErr, k12storage.ErrProblemAssetUnavailable) {
				return nil, checkErr
			}
			if checkErr != nil || current.Source != *step.Candidate.AssetSource {
				step.Candidate = nil
			}
		}
		if step.Candidate == nil && step.Generate.Status == "" {
			asset, err := d.Records.FindPracticeProblemAsset(ctx, k12.PracticeAssetQuery{OwnerID: d.TextbookOwnerID, AgentName: request.AgentName,
				Subject: target.Subject, GradeTerm: request.GradeTerm, KnowledgePoint: target.KnowledgePoint,
				OriginalQuestion: target.Question, ExcludedHashes: excluded})
			if err == nil && (request.PlanSection != k12.WeeklySectionArithmeticWarmup || weeklyArithmeticExpression(asset.Version.Facts.Stem)) {
				step.Candidate = &WeeklyPracticeCandidate{SourceKind: "problem_asset", GenerationMethod: k12.WeeklyGenerationMethodAssetReuse,
					SourceRef: target.SourceRef, Subject: target.Subject, KnowledgePoint: target.KnowledgePoint, SourceQuestion: target.Question,
					PromptMarkdown: asset.Version.Facts.Stem, ExpectedAnswer: asset.Version.Answer, EvidenceRefs: append([]string(nil), target.EvidenceRefs...),
					EstimatedSeconds: 30, AssetSource: &asset.Source}
				if err := save(); err != nil {
					return nil, err
				}
			} else if err != nil && !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
				return nil, err
			}
		}
		if step.Candidate == nil {
			if d.PracticeVariant == nil || d.Solver == nil || d.PracticeGenerationRoute == nil {
				return nil, fmt.Errorf("weekly generation capability unavailable")
			}
			if request.Generation.Route.Route == "" {
				route, routeErr := d.PracticeGenerationRoute(ctx, k12.GradingModelSnapshot{})
				if routeErr != nil {
					return nil, routeErr
				}
				request.Generation.Route = k12.NormalizeGradingModelSnapshot(route)
				if request.Generation.Route.Provider == "" || request.Generation.Route.Model == "" || request.Generation.Route.Capability != "text" {
					return nil, fmt.Errorf("weekly generation route incomplete")
				}
				if err := save(); err != nil {
					return nil, err
				}
			}
			callCtx := k12.WithGradingModelSnapshot(ctx, request.Generation.Route)
			run := func(call *weeklyCandidateCall, invoke func() (SolveResult, error)) (SolveResult, error) {
				switch call.Status {
				case "succeeded":
					if weeklySuccessfulSolveResult(call) == nil {
						return SolveResult{}, fmt.Errorf("weekly successful response missing")
					}
					return *weeklySuccessfulSolveResult(call), nil
				case "sent", "outcome_unknown":
					return SolveResult{}, ErrModelInvocationRequiresReconciliation
				case "failed":
					return SolveResult{}, fmt.Errorf("weekly prior invocation failed: %s", call.Error)
				}
				call.Status = "sent"
				if err := save(); err != nil {
					return SolveResult{}, err
				}
				result, callErr := invoke()
				saveCtx = context.WithoutCancel(ctx)
				if callErr != nil {
					call.Status, call.Error = "failed", callErr.Error()
					if errors.Is(callErr, ErrModelInvocationRequiresReconciliation) || (!errors.Is(callErr, egress.ErrProviderNotSent) && sentProviderOutcomeUnknown(callErr, ctx.Err())) {
						call.Status = "outcome_unknown"
					}
				} else {
					call.Status, call.Result = "succeeded", &result
				}
				if err := save(); err != nil {
					return SolveResult{}, err
				}
				if call.Status == "outcome_unknown" {
					return SolveResult{}, ErrModelInvocationRequiresReconciliation
				}
				return result, callErr
			}
			generated, err := run(&step.Generate, func() (SolveResult, error) {
				prompt := fmt.Sprintf("Generate one same-level practice problem for the explicit knowledge point %s, grade %s, textbook %s. Source problem: %s. Do not repeat the source or earlier variants. Variant %d. Output ## 问题 / ## 解答 / ## 答案. Section: %s.", target.KnowledgePoint, request.GradeTerm, request.Textbook, target.Question, index+1, request.PlanSection)
				if strings.HasPrefix(target.SourceRef, "textbook:") {
					prompt += " The source is a verified textbook lesson page. Use only its taught methods and prerequisites; generate a new self-contained text problem that needs no diagram. Do not copy the examples or infer student mastery."
				}
				return d.PracticeVariant.GeneratePracticeVariant(callCtx, target.Subject, prompt, request.GradeTerm)
			})
			if err != nil {
				return nil, err
			}
			question, _, answer := SplitRetryPresentation(generated.Solution)
			if question == "" || answer == "" || (request.PlanSection == k12.WeeklySectionArithmeticWarmup && !weeklyArithmeticExpression(question)) {
				return nil, fmt.Errorf("weekly generated problem invalid")
			}
			solveCall := weeklyLatestSolve(step)
			inputDigest := d.weeklySolveInputDigest(callCtx, request, index, question)
			if solveCall.InputDigest != "" && solveCall.InputDigest != inputDigest {
				return nil, fmt.Errorf("weekly solve input changed")
			}
			validated, err := run(solveCall, func() (SolveResult, error) {
				solveCall.InputDigest = inputDigest
				executor := &weeklyPhysicalExecutor{call: solveCall, save: func() error { saveCtx = context.WithoutCancel(ctx); return save() }}
				solveCtx := WithGradingPhysicalCallExecutor(k12.WithMaterialPreparation(callCtx), executor)
				result, err := d.solveProblem(solveCtx, target.Subject, question, request.GradeTerm)
				if executor.err != nil {
					return SolveResult{}, executor.err
				}
				return result, err
			})
			if err != nil {
				return nil, err
			}
			_, _, expected := SplitRetryPresentation(validated.Solution)
			if len(step.SolveRecoveryAttempts) > 0 {
				if err := d.validateWeeklyRecoverySource(ctx, request); err != nil {
					return nil, err
				}
			}
			status, evidence, _ := classifyGeneratedPracticeItem(target.Subject, validated, strings.TrimSpace(expected) != "")
			if status != k12.PracticeItemVerified {
				return nil, fmt.Errorf("weekly generated answer verification insufficient")
			}
			sourceKind := "mistake_variant"
			if strings.HasPrefix(target.SourceRef, "assessment:") {
				sourceKind = "assessment_variant"
			} else if strings.HasPrefix(target.SourceRef, "textbook:") {
				sourceKind = "textbook_variant"
			}
			step.Candidate = &WeeklyPracticeCandidate{SourceKind: sourceKind, GenerationMethod: k12.WeeklyGenerationMethodAIVariant,
				SourceRef: fmt.Sprintf("%s:variant:%d", target.SourceRef, index+1), Subject: target.Subject, KnowledgePoint: target.KnowledgePoint, SourceQuestion: target.Question,
				PromptMarkdown: question, ExpectedAnswer: expected, EvidenceRefs: append(append([]string(nil), target.EvidenceRefs...), evidence), EstimatedSeconds: 30}
			if err := save(); err != nil {
				return nil, err
			}
		}
		hash, _, _ := k12.StablePracticeProblemHash(k12.PracticeCandidateProblem{Subject: step.Candidate.Subject, QuestionMarkdown: step.Candidate.PromptMarkdown})
		for _, prior := range excluded {
			if prior == hash {
				return nil, fmt.Errorf("weekly candidate duplicates practiced problem")
			}
		}
		excluded = append(excluded, hash)
		output = append(output, *step.Candidate)
	}
	return output, nil
}

// resetWeeklyKnownFailures 只允许显式重试已知失败；未知回执保持原状态。
func resetWeeklyKnownFailures(request *WeeklyPracticeCandidateRequest) {
	if request.Generation == nil {
		return
	}
	for i := range request.Generation.Items {
		step := &request.Generation.Items[i]
		if step.Generate.Status == "failed" {
			step.Generate = weeklyCandidateCall{}
		}
		if step.Solve.Status == "failed" {
			if len(step.Solve.PhysicalCalls) == 0 {
				step.Solve = weeklyCandidateCall{}
				continue
			}
			unknown := false
			for _, receipt := range step.Solve.PhysicalCalls {
				unknown = unknown || receipt.Status == "sent" || receipt.Status == "outcome_unknown"
			}
			if unknown {
				continue
			}
			successful := make([]weeklyPhysicalReceipt, 0, len(step.Solve.PhysicalCalls))
			for _, receipt := range step.Solve.PhysicalCalls {
				if receipt.Status == "succeeded" {
					successful = append(successful, receipt)
				} else {
					step.Solve.PhysicalHistory = append(step.Solve.PhysicalHistory, receipt)
				}
			}
			step.Solve.Status, step.Solve.Error = "", ""
			step.Solve.Result = nil
			step.Solve.PhysicalCalls = successful
		}
	}
}

func (d Deps) weeklyCandidateCheckpointJSON(ctx context.Context, request WeeklyPracticeCandidateRequest) string {
	if request.Checkpoint.Kind != "" {
		if raw, err := d.Records.GetWeeklyCandidateCheckpoint(ctx, request.Checkpoint); err == nil {
			return raw
		}
	}
	raw, _ := json.Marshal(request)
	return string(raw)
}
