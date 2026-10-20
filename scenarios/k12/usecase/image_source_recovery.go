package usecase

import (
	"context"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

var imageSolveActionPattern = regexp.MustCompile(`(?i)解题|解答|求解|\bsolve\b`)
var imageGradeActionPattern = regexp.MustCompile(`(?i)批改|批阅|检查作业|\bgrade\b|\bmark\b`)
var imageNegatedSolvePattern = regexp.MustCompile(`(?i)(不要|不用|无需|不必|别|do not|don't).{0,6}(解题|解答|求解|solve)`)
var imageNegatedGradePattern = regexp.MustCompile(`(?i)(不要|不用|无需|不必|别|do not|don't).{0,6}(批改|批阅|检查作业|grade|mark)`)

// 只冻结明确动作；冲突或未提供动作时保留原图片分类，不从笔迹反推用户目的。
func explicitImageHomeworkAction(message string) string {
	solve, grade := false, false
	for _, clause := range strings.FieldsFunc(message, func(r rune) bool { return strings.ContainsRune("，,。；;！!？?\n", r) }) {
		solve = solve || imageSolveActionPattern.MatchString(clause) && !imageNegatedSolvePattern.MatchString(clause)
		grade = grade || imageGradeActionPattern.MatchString(clause) && !imageNegatedGradePattern.MatchString(clause)
	}
	if solve && !grade {
		return "solve"
	}
	if grade && !solve {
		return "grade"
	}
	return ""
}

// 可选来源端口只返回已核验的派生材料，不覆盖 OCR 或学生作答。
type ImageSourceRecovery interface {
	RecoverImageSource(context.Context, GroundingSnapshot, k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error)
}

type ImageSourceLibraryLookup interface {
	FindImageSourceCandidate(context.Context, string, string, string, string, k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error)
}

func (o *GradingOrchestrator) recoverImageSourceMaterials(ctx context.Context, run *gradingRun, job GradingJobView) {
	if run.req.ImageScope == nil || o.deps.Records == nil {
		return
	}
	lookup, supported := o.deps.Grounding.(ImageSourceRecovery)
	freezer, canFreeze := o.deps.Grounding.(SnapshotGrounding)
	if !supported || !canFreeze {
		return
	}
	owner, err := resolveGradingGroundingTextbookOwner(ctx, o.deps, job)
	if err != nil {
		return
	}
	for i := range run.questions {
		source := run.questions[i].ImageSourceContext
		if source == nil || len(source.Recoveries) == 0 {
			continue
		}
		subject := imageSourceTextbookSubject(run.questions[i].Subject)
		if subject == "" {
			continue
		}
		scope, found, scopeErr := o.deps.Records.GetActiveTextbookGroundingScopeReadOnly(ctx, k12storage.TextbookScope{OwnerID: owner, AgentName: run.agentName, Subject: subject})
		if scopeErr != nil || !found {
			for n := range source.Recoveries {
				if source.Recoveries[n].State == "unresolved" {
					source.Recoveries[n].State = "no_verified_bound_textbook"
					if library, ok := o.deps.Grounding.(ImageSourceLibraryLookup); ok && source.Recoveries[n].Gap.Kind != "handwriting" {
						candidate, queryErr := library.FindImageSourceCandidate(ctx, run.agentName, run.questions[i].Subject, run.req.Grade, run.questions[i].RawTranscription, source.Recoveries[n].Gap)
						if queryErr == nil {
							source.Recoveries[n] = candidate
						}
					}
				}
			}
			continue
		}
		snapshot := groundingSnapshotFromScope(owner, run.agentName, scope)
		snapshot.Subject = run.questions[i].Subject
		snapshot, err = freezer.FreezeGroundingSnapshot(ctx, snapshot)
		if err != nil {
			continue
		}
		for n, recovery := range source.Recoveries {
			if recovery.State != "unresolved" {
				continue
			}
			result, lookupErr := lookup.RecoverImageSource(ctx, snapshot, recovery.Gap)
			if lookupErr != nil {
				source.Recoveries[n].State = "source_lookup_failed"
				continue
			}
			source.Recoveries[n] = result
			if result.State != "verified" && recovery.Gap.Kind != "handwriting" {
				if library, ok := o.deps.Grounding.(ImageSourceLibraryLookup); ok {
					candidate, queryErr := library.FindImageSourceCandidate(ctx, run.agentName, run.questions[i].Subject, run.req.Grade, run.questions[i].RawTranscription, recovery.Gap)
					if queryErr == nil {
						source.Recoveries[n] = candidate
					}
				}
			}
		}
	}
}

func imageSourceTextbookSubject(subject string) string {
	switch strings.TrimSpace(subject) {
	case "语文", "chinese":
		return "chinese"
	case "数学", "math":
		return "math"
	case "英语", "english":
		return "english"
	case "科学", "science":
		return "science"
	default:
		return ""
	}
}
