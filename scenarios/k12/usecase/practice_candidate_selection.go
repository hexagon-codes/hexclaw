package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

const practiceCandidateBatchSize = 3

type PracticeCandidateSelectionRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Grade          string `json:"grade,omitempty"`
	Textbook       string `json:"textbook,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	SourceSession  string `json:"source_session,omitempty"`
}

type PracticeCandidateBatchRequest struct {
	Revision       int    `json:"revision"`
	IdempotencyKey string `json:"idempotency_key"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
}

func (d Deps) OpenPracticeCandidateSelection(
	ctx context.Context,
	agentName, sourceMistakeID string,
	req PracticeCandidateSelectionRequest,
) (k12.PracticeCandidateSelection, error) {
	if d.Records == nil {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"usecase: 未配置 K12 记录存储",
		)
	}
	agentName = strings.TrimSpace(agentName)
	sourceMistakeID = strings.TrimSpace(sourceMistakeID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if agentName == "" || sourceMistakeID == "" || req.IdempotencyKey == "" {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"%w: agent/source_mistake/idempotency_key 必填", ErrInvalidInput,
		)
	}
	source, fields, err := d.singlePracticeSource(ctx, agentName, sourceMistakeID)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	normalized, _, _, err := normalizeSinglePracticeRequest(
		ctx, d, source, fields, SinglePracticeGenerationRequest{
			IdempotencyKey: req.IdempotencyKey,
			Grade:          req.Grade,
			Textbook:       req.Textbook,
			Difficulty:     "same",
			Provider:       req.Provider,
			Model:          req.Model,
			SourceSession:  req.SourceSession,
		},
	)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	if normalized.SourceSession == "" {
		normalized.SourceSession = source.SourceSession
	}

	if prior, getErr := d.Records.GetPracticeCandidateSelectionByIdempotencyKey(
		ctx, agentName, req.IdempotencyKey,
	); getErr == nil {
		if prior.SourceMistakeID != sourceMistakeID ||
			prior.Grade != normalized.Grade ||
			prior.Textbook != normalized.Textbook ||
			prior.SourceSessionID != normalized.SourceSession {
			return k12.PracticeCandidateSelection{}, fmt.Errorf(
				"%w: idempotency_key 已绑定其他候选题请求", ErrInvalidInput,
			)
		}
		return d.ensureInitialPracticeCandidateBatch(
			ctx, prior, req.IdempotencyKey,
		)
	} else if !errors.Is(getErr, records.ErrNotFound) {
		return k12.PracticeCandidateSelection{}, getErr
	}
	if d.PracticeGenerationRoute == nil {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"usecase: 未配置逐题出题路由解析",
		)
	}
	route, err := d.PracticeGenerationRoute(ctx, k12.GradingModelSnapshot{
		Provider: normalized.Provider,
		Model:    normalized.Model,
	})
	if err != nil {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"usecase: 冻结候选题模型路由: %w", err,
		)
	}
	route = k12.NormalizeGradingModelSnapshot(route)
	if route.Provider == "" || route.Model == "" ||
		route.Route != route.Provider+"/"+route.Model ||
		(route.Capability != "" && route.Capability != "text") {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"usecase: 候选题冻结路由必须是完整 text provider/model 快照",
		)
	}
	routeRaw, err := json.Marshal(route)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	selection, _, err := d.Records.OpenPracticeCandidateSelection(
		ctx, k12storage.PracticeCandidateOpenInput{
			AgentName:         agentName,
			SourceMistakeID:   sourceMistakeID,
			IdempotencyKey:    req.IdempotencyKey,
			Grade:             normalized.Grade,
			Textbook:          normalized.Textbook,
			SourceSession:     normalized.SourceSession,
			RouteSnapshotJSON: string(routeRaw),
		},
	)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	return d.ensureInitialPracticeCandidateBatch(
		ctx, selection, req.IdempotencyKey,
	)
}

func (d Deps) ensureInitialPracticeCandidateBatch(
	ctx context.Context,
	selection k12.PracticeCandidateSelection,
	openIdempotencyKey string,
) (k12.PracticeCandidateSelection, error) {
	if selection.State != k12.PracticeCandidateSelectionOpen {
		return selection, nil
	}
	for _, candidate := range selection.Candidates {
		if candidate.CandidateKind == k12.PracticeCandidateVariant {
			return selection, nil
		}
	}
	return d.GeneratePracticeCandidateBatch(
		ctx, selection.AgentName, selection.SelectionID,
		PracticeCandidateBatchRequest{
			Revision:       selection.Revision,
			IdempotencyKey: openIdempotencyKey + ":initial-batch",
		},
	)
}

func (d Deps) GeneratePracticeCandidateBatch(
	ctx context.Context,
	agentName, selectionID string,
	req PracticeCandidateBatchRequest,
) (k12.PracticeCandidateSelection, error) {
	if d.Records == nil {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"usecase: 未配置 K12 记录存储",
		)
	}
	agentName = strings.TrimSpace(agentName)
	selectionID = strings.TrimSpace(selectionID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	if agentName == "" || selectionID == "" || req.IdempotencyKey == "" ||
		req.Revision < 1 || (req.Provider == "") != (req.Model == "") {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"%w: agent/selection/revision/idempotency_key 非法", ErrInvalidInput,
		)
	}
	selection, err := d.Records.GetPracticeCandidateSelection(
		ctx, agentName, selectionID,
	)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	if selection.State != k12.PracticeCandidateSelectionOpen {
		return k12.PracticeCandidateSelection{}, records.ErrIllegalTransition
	}
	var route k12.GradingModelSnapshot
	if err := json.Unmarshal([]byte(selection.RouteSnapshotJSON), &route); err != nil {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"%w: 候选题冻结路由快照损坏",
			ErrModelInvocationRequiresReconciliation,
		)
	}
	route = k12.NormalizeGradingModelSnapshot(route)
	if route.Provider == "" || route.Model == "" ||
		route.Route != route.Provider+"/"+route.Model {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"%w: 候选题冻结路由快照不完整",
			ErrModelInvocationRequiresReconciliation,
		)
	}
	if req.Provider != "" &&
		(req.Provider != route.Provider || req.Model != route.Model) {
		return k12.PracticeCandidateSelection{}, fmt.Errorf(
			"%w: 已接受候选题只能使用冻结模型路由", ErrInvalidInput,
		)
	}
	reserved, _, err := d.Records.ReservePracticeCandidateBatch(
		ctx, agentName, selectionID, req.Revision,
		req.IdempotencyKey, practiceCandidateBatchSize,
	)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	_, fields, err := d.singlePracticeSource(
		ctx, agentName, selection.SourceMistakeID,
	)
	if err != nil {
		return k12.PracticeCandidateSelection{}, err
	}
	modelCtx := k12.WithGradingModelSnapshot(ctx, route)
	// 候选尚未入篮也占用本次选择的题目身份，换一批不能反复返回同一资产。
	excludedHashes := make([]string, 0, len(selection.Candidates)+len(reserved))
	for _, prior := range append(append([]k12.PracticeCandidate(nil), selection.Candidates...), reserved...) {
		if prior.NormalizedContentHash != "" {
			excludedHashes = append(excludedHashes, prior.NormalizedContentHash)
		}
	}
	for _, candidate := range reserved {
		if candidate.State != k12.PracticeCandidateGenerating {
			continue
		}
		asset, assetErr := d.Records.FindPracticeProblemAsset(ctx, k12.PracticeAssetQuery{
			OwnerID: strings.TrimSpace(d.TextbookOwnerID), AgentName: agentName, Subject: strings.TrimSpace(fields.Subject),
			GradeTerm: selection.Grade, KnowledgePoint: strings.TrimSpace(fields.KnowledgePoint),
			OriginalQuestion: fields.Question, ExcludedHashes: excludedHashes,
		})
		if assetErr == nil {
			completed, completeErr := d.Records.CompletePracticeCandidate(ctx, agentName, candidate.CandidateID, k12.PracticeCandidateProblem{
				Subject: fields.Subject, QuestionMarkdown: asset.Version.Facts.Stem,
				ExpectedAnswerMarkdown: asset.Version.Answer, AssetSource: &asset.Source,
			}, "")
			if completeErr == nil {
				excludedHashes = append(excludedHashes, completed.NormalizedContentHash)
				continue
			}
			if !errors.Is(completeErr, k12storage.ErrProblemAssetUnavailable) {
				return k12.PracticeCandidateSelection{}, completeErr
			}
		} else if !errors.Is(assetErr, k12storage.ErrProblemAssetUnavailable) {
			return k12.PracticeCandidateSelection{}, assetErr
		}
		if d.PracticeVariant == nil || d.Solver == nil {
			_, _ = d.Records.CompletePracticeCandidate(
				context.WithoutCancel(ctx), agentName, candidate.CandidateID,
				k12.PracticeCandidateProblem{},
				"候选题生成或独立验算能力未配置",
			)
			continue
		}
		request := singlePracticeRequestSnapshot{
			SourceMistakeID: selection.SourceMistakeID,
			SourceProblemID: selection.SourceMistakeID,
			Question:        strings.TrimSpace(fields.Question),
			KnowledgePoint:  strings.TrimSpace(fields.KnowledgePoint),
			Subject:         strings.TrimSpace(fields.Subject),
			Grade:           selection.Grade,
			Textbook:        selection.Textbook,
			Difficulty:      "same",
			Provider:        route.Provider,
			Model:           route.Model,
			SourceSession:   selection.SourceSessionID,
		}
		prompt := singlePracticePrompt(request) + fmt.Sprintf(
			"\n这是第 %d 批第 %d 题；题干必须与本批其他题不同。",
			candidate.BatchOrdinal, candidate.CandidateOrdinal,
		)
		generated, generateErr := d.PracticeVariant.GeneratePracticeVariant(
			modelCtx, request.Subject, prompt, request.Grade,
		)
		if generateErr != nil {
			_, _ = d.Records.CompletePracticeCandidate(
				context.WithoutCancel(ctx), agentName, candidate.CandidateID,
				k12.PracticeCandidateProblem{}, "模型生成失败："+generateErr.Error(),
			)
			continue
		}
		question, _, expected := SplitRetryPresentation(generated.Solution)
		if strings.TrimSpace(question) == "" || strings.TrimSpace(expected) == "" {
			_, _ = d.Records.CompletePracticeCandidate(
				context.WithoutCancel(ctx), agentName, candidate.CandidateID,
				k12.PracticeCandidateProblem{},
				"模型结果缺少可分离的题目或答案",
			)
			continue
		}
		validated, validateErr := d.solveProblem(
			modelCtx, request.Subject, question, request.Grade,
		)
		if validateErr != nil {
			_, _ = d.Records.CompletePracticeCandidate(
				context.WithoutCancel(ctx), agentName, candidate.CandidateID,
				k12.PracticeCandidateProblem{},
				"独立验算失败："+validateErr.Error(),
			)
			continue
		}
		_, _, validatedExpected := SplitRetryPresentation(validated.Solution)
		if normalizedCandidateAnswer(validatedExpected) == "" ||
			normalizedCandidateAnswer(validatedExpected) !=
				normalizedCandidateAnswer(expected) {
			_, _ = d.Records.CompletePracticeCandidate(
				context.WithoutCancel(ctx), agentName, candidate.CandidateID,
				k12.PracticeCandidateProblem{},
				"生成答案与独立验算结果不一致",
			)
			continue
		}
		completed, completeErr := d.Records.CompletePracticeCandidate(
			context.WithoutCancel(ctx), agentName, candidate.CandidateID,
			k12.PracticeCandidateProblem{
				Subject:                request.Subject,
				QuestionMarkdown:       question,
				ExpectedAnswerMarkdown: expected,
			},
			"",
		)
		if completeErr != nil {
			return k12.PracticeCandidateSelection{}, completeErr
		}
		excludedHashes = append(excludedHashes, completed.NormalizedContentHash)
	}
	return d.Records.GetPracticeCandidateSelection(ctx, agentName, selectionID)
}

func normalizedCandidateAnswer(value string) string {
	replacer := strings.NewReplacer(
		" ", "", "\n", "", "\t", "", "\r", "",
		"*", "", "`", "", "，", ",", "。", "",
	)
	return strings.ToLower(replacer.Replace(strings.TrimSpace(value)))
}

func (d Deps) CommitPracticeCandidateSelection(
	ctx context.Context,
	in k12storage.PracticeCandidateCommitInput,
) (k12storage.PracticeCandidateCommitReceipt, error) {
	if d.Records == nil {
		return k12storage.PracticeCandidateCommitReceipt{}, fmt.Errorf(
			"usecase: 未配置 K12 记录存储",
		)
	}
	return d.Records.CommitPracticeCandidateSelection(ctx, in)
}
