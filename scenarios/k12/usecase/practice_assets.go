package usecase

import (
	"context"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// trySinglePracticeAsset 先按冻结目标选题；资产缺失或提交前失效时继续原 Generate+Solve。
// 只接管未开始模型调用的任务，不创建虚构的生成/验算回执。
func (d Deps) trySinglePracticeAsset(ctx context.Context, job k12.PracticeGenerationJob, request singlePracticeRequestSnapshot) (bool, error) {
	if job.Status != k12.PracticeGenerationQueued || job.Attempt != 0 || request.Difficulty != "same" {
		return false, nil
	}
	candidate, err := d.Records.FindPracticeProblemAsset(ctx, k12.PracticeAssetQuery{
		OwnerID: strings.TrimSpace(d.TextbookOwnerID), AgentName: job.AgentName, Subject: request.Subject,
		GradeTerm: request.Grade, KnowledgePoint: request.KnowledgePoint, OriginalQuestion: request.Question,
	})
	if errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// 当前资产发布资格只允许 numeric_exec；此处投影资产既有验证方式，不表示本任务再次验算。
	ready := k12.PracticeItem{
		ItemID: job.ResultItemIDs[0], SourceProblemID: request.SourceProblemID, SourceMistakeSummary: job.SourceSummary,
		Subject: request.Subject, AddedVia: k12.PracticeAddedViaSingleVariant, GenerationStatus: k12.PracticeItemGenerationReady,
		QuestionMarkdown: candidate.Version.Facts.Stem, ExpectedAnswerMarkdown: candidate.Version.Answer,
		VerificationStatus: k12.PracticeItemVerified, VerificationEvidence: string(EvidenceNumericExec),
		GenerationJobID: job.GenerationJobID, VariantIndex: 1, RequestedDifficulty: request.Difficulty,
		ActualDifficulty: request.Difficulty, AssetSource: &candidate.Source,
	}
	ready.NormalizedContentHash, _, err = k12.StablePracticeProblemHash(k12.PracticeCandidateProblem{
		Subject: ready.Subject, QuestionMarkdown: ready.QuestionMarkdown,
	})
	if err != nil {
		return false, err
	}
	_, _, err = d.commitSinglePracticeReadyItem(ctx, job, request.SourceSession, request.Grade, k12.PracticeSourceSingleVariant, ready)
	if errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
		return false, nil
	}
	return err == nil, err
}
