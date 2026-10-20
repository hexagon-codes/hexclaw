package usecase

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 可选来源端口只返回已核验的派生材料，不覆盖 OCR 或学生作答。
type ImageSourceRecovery interface {
	RecoverImageSource(context.Context, GroundingSnapshot, k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error)
}

type ImageSourceLibraryLookup interface {
	FindImageSourceCandidate(context.Context, string, string, k12.RecognitionImageSourceGapV1) (k12.RecognitionImageSourceRecoveryV1, error)
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
						candidate, queryErr := library.FindImageSourceCandidate(ctx, run.questions[i].Subject, run.req.Grade, source.Recoveries[n].Gap)
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
