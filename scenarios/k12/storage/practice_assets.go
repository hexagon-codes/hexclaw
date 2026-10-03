package k12storage

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// FindPracticeProblemAsset 只用已持久化的有效批改知识点匹配教学目标，不补猜缺失标签。
func (s *Store) FindPracticeProblemAsset(ctx context.Context, query k12.PracticeAssetQuery) (k12.PracticeAssetCandidate, error) {
	return findPracticeProblemAsset(ctx, s.db, query, "")
}

func findPracticeProblemAsset(ctx context.Context, db dbHandle, query k12.PracticeAssetQuery, assetID string) (k12.PracticeAssetCandidate, error) {
	if query.OwnerID == "" || query.AgentName == "" || query.Subject == "" || query.GradeTerm == "" ||
		query.KnowledgePoint == "" || query.KnowledgePoint == "其他" || (query.OriginalReview && query.OriginalQuestion == "") {
		return k12.PracticeAssetCandidate{}, ErrProblemAssetUnavailable
	}
	rows, err := db.QueryContext(ctx, `WITH eligible AS (
		SELECT v.*, COALESCE((SELECT json_extract(c.correction_json,'$.assessment.result_json')
			FROM k12_assessment_corrections c WHERE c.agent_name=g.agent_name AND c.job_id=g.job_id
			AND c.problem_id=g.problem_id AND c.input_revision=g.input_revision
			ORDER BY c.correction_revision DESC LIMIT 1),g.result_json) AS teaching_json
		FROM k12_problem_assets a JOIN k12_problem_asset_versions v
		ON v.owner_id=a.owner_id AND v.asset_id=a.asset_id AND v.asset_version=a.current_version
		JOIN k12_problem_asset_publications p ON p.owner_id=v.owner_id AND p.asset_id=v.asset_id AND p.asset_version=v.asset_version
		JOIN k12_grading_item_invocations i ON i.agent_name=json_extract(p.verification_json,'$.agent_name')
		AND i.item_invocation_id=json_extract(p.verification_json,'$.invocation_id')
		JOIN k12_grading_assessment_items g ON g.agent_name=i.agent_name AND g.job_id=i.job_id
		AND g.problem_id=i.problem_id AND g.input_revision=i.input_revision AND g.current_disposition='current'
		WHERE a.owner_id=? AND a.status='active' AND a.revision=v.published_revision
		AND (?='' OR a.asset_id=?) AND json_extract(v.facts_json,'$.subject')=?
		AND json_extract(v.facts_json,'$.answer_context.grade_term')=?
	) SELECT DISTINCT owner_id,asset_id,asset_version,published_revision,facts_json,facts_digest,answer,answer_result_json,created_at
	FROM eligible WHERE json_array_length(teaching_json,'$.Recognized.KnowledgePoints')=1
	AND json_extract(teaching_json,'$.Recognized.KnowledgePoints[0]')=?
	ORDER BY created_at,asset_id`, query.OwnerID, assetID, assetID, query.Subject, query.GradeTerm, query.KnowledgePoint)
	if err != nil {
		return k12.PracticeAssetCandidate{}, err
	}
	var versions []k12.ProblemAssetVersion
	for rows.Next() {
		var v k12.ProblemAssetVersion
		var facts string
		if err := rows.Scan(&v.OwnerID, &v.AssetID, &v.Version, &v.Revision, &facts, &v.FactsDigest, &v.Answer, &v.AnswerResultJSON, &v.CreatedAt); err != nil {
			rows.Close()
			return k12.PracticeAssetCandidate{}, err
		}
		if err := json.Unmarshal([]byte(facts), &v.Facts); err != nil {
			rows.Close()
			return k12.PracticeAssetCandidate{}, err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return k12.PracticeAssetCandidate{}, err
	}
	if len(versions) == 0 {
		return k12.PracticeAssetCandidate{}, ErrProblemAssetUnavailable
	}
	excluded := map[string]bool{}
	for _, hash := range query.ExcludedHashes {
		excluded[hash] = true
	}
	if !query.OriginalReview {
		excluded[practiceQuestionHash(query.Subject, query.OriginalQuestion)] = true
		history, err := db.QueryContext(ctx, `SELECT i.subject,i.question_markdown FROM k12_practice_set_items i
			JOIN k12_practice_sets s ON s.record_id=i.set_record_id WHERE s.agent_name=?
			UNION SELECT p.subject,p.stem_markdown FROM k12_problems p JOIN k12_attempts a
			ON a.agent_name=p.agent_name AND a.problem_id=p.problem_id WHERE p.agent_name=? AND a.answer_state='present'
			UNION SELECT COALESCE(json_extract(result_json,'$.Recognized.Subject'),''),
			COALESCE(NULLIF(json_extract(result_json,'$.Recognized.canonical_markdown'),''),json_extract(result_json,'$.Recognized.Question'),'')
			FROM k12_grading_assessment_items WHERE agent_name=? AND json_extract(result_json,'$.Recognized.AnswerState')='present'`,
			query.AgentName, query.AgentName, query.AgentName)
		if err != nil {
			return k12.PracticeAssetCandidate{}, err
		}
		for history.Next() {
			var subject, question string
			if err := history.Scan(&subject, &question); err != nil {
				history.Close()
				return k12.PracticeAssetCandidate{}, err
			}
			excluded[practiceQuestionHash(subject, question)] = true
		}
		err = history.Err()
		history.Close()
		if err != nil {
			return k12.PracticeAssetCandidate{}, err
		}
	}
	for _, v := range versions {
		// 当前练习入口只承载完整纯文本独立题，含材料或图表的资产交回原生成链。
		if len(v.Facts.SharedMaterial)+len(v.Facts.Options)+len(v.Facts.VisualFacts)+len(v.Facts.Objects) > 0 {
			continue
		}
		hash := practiceQuestionHash(v.Facts.Subject, v.Facts.Stem)
		if excluded[hash] || (query.OriginalReview && hash != practiceQuestionHash(query.Subject, query.OriginalQuestion)) {
			continue
		}
		return k12.PracticeAssetCandidate{Version: v, Source: k12.PracticeAssetSource{
			OwnerID: v.OwnerID, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision,
			FactsDigest: v.FactsDigest, GradeTerm: query.GradeTerm, KnowledgePoint: query.KnowledgePoint,
			OriginalReview: query.OriginalReview,
		}}, nil
	}
	return k12.PracticeAssetCandidate{}, ErrProblemAssetUnavailable
}

func practiceQuestionHash(subject, question string) string {
	hash, _, _ := k12.StablePracticeProblemHash(k12.PracticeCandidateProblem{
		Subject: strings.TrimSpace(subject), QuestionMarkdown: strings.TrimSpace(question),
	})
	return hash
}

// validatePracticeAssetItems 在入篮写事务内再次核对资格与已练记录；幂等已提交结果不经过此门。
func validatePracticeAssetItems(ctx context.Context, db dbHandle, recFields string, job k12.PracticeGenerationJob) error {
	fields, err := k12.ParsePracticeSetFields(recFields)
	if err != nil {
		return err
	}
	for _, item := range fields.Items {
		if item.GenerationJobID != job.GenerationJobID || item.AssetSource == nil {
			continue
		}
		var request struct {
			Question, Subject, Grade string
			KnowledgePoint           string `json:"knowledge_point"`
		}
		if err := json.Unmarshal([]byte(job.RequestSnapshot), &request); err != nil {
			return err
		}
		source := item.AssetSource
		if job.Scope != "single" {
			// 组卷沿既有来源题身份复核教学目标；来源变化不能把旧选择带入新目标。
			if err := db.QueryRowContext(ctx, `SELECT subject,question,knowledge_point FROM k12_mistakes
				WHERE record_id=? AND agent_name=?`, item.SourceProblemID, job.AgentName).
				Scan(&request.Subject, &request.Question, &request.KnowledgePoint); err != nil {
				return err
			}
			if source.OriginalReview || job.Difficulty != "same" {
				return ErrProblemAssetUnavailable
			}
		}
		if source.GradeTerm != request.Grade || source.KnowledgePoint != strings.TrimSpace(request.KnowledgePoint) ||
			strings.TrimSpace(item.Subject) != strings.TrimSpace(request.Subject) {
			return ErrProblemAssetUnavailable
		}
		if err := validatePracticeAssetProblem(ctx, db, job.AgentName, request.Question, k12.PracticeCandidateProblem{
			Subject: item.Subject, QuestionMarkdown: item.QuestionMarkdown,
			ExpectedAnswerMarkdown: item.ExpectedAnswerMarkdown, AssetSource: source,
		}); err != nil {
			return err
		}
	}
	return nil
}

// validatePracticeAssetProblem 复用同一选题资格，在各入口自己的写事务中校验冻结来源。
func validatePracticeAssetProblem(ctx context.Context, db dbHandle, agentName, original string, problem k12.PracticeCandidateProblem) error {
	source := problem.AssetSource
	if source == nil {
		return nil
	}
	candidate, err := findPracticeProblemAsset(ctx, db, k12.PracticeAssetQuery{
		OwnerID: source.OwnerID, AgentName: agentName, Subject: problem.Subject, GradeTerm: source.GradeTerm,
		KnowledgePoint: source.KnowledgePoint, OriginalReview: source.OriginalReview, OriginalQuestion: original,
	}, source.AssetID)
	if err != nil {
		return err
	}
	expected := k12.NormalizePracticeCandidateProblem(k12.PracticeCandidateProblem{
		Subject: candidate.Version.Facts.Subject, QuestionMarkdown: candidate.Version.Facts.Stem,
		ExpectedAnswerMarkdown: candidate.Version.Answer,
	})
	problem = k12.NormalizePracticeCandidateProblem(problem)
	if candidate.Source != *source || expected.QuestionMarkdown != problem.QuestionMarkdown ||
		expected.ExpectedAnswerMarkdown != problem.ExpectedAnswerMarkdown || len(problem.Options)+len(problem.ResourceDigests) != 0 {
		return ErrProblemAssetUnavailable
	}
	return nil
}

func validatePracticeCandidateAsset(ctx context.Context, db dbHandle, agentName, selectionID string, problem k12.PracticeCandidateProblem) error {
	if problem.AssetSource == nil {
		return nil
	}
	var subject, question, point, grade string
	if err := db.QueryRowContext(ctx, `SELECT m.subject,m.question,m.knowledge_point,s.grade
		FROM k12_practice_candidate_selections s JOIN k12_mistakes m
		ON m.record_id=s.source_mistake_id AND m.agent_name=s.agent_name
		WHERE s.agent_name=? AND s.selection_id=?`, agentName, selectionID).
		Scan(&subject, &question, &point, &grade); err != nil {
		return err
	}
	source := problem.AssetSource
	if source.OriginalReview || source.GradeTerm != grade || source.KnowledgePoint != strings.TrimSpace(point) ||
		problem.Subject != strings.TrimSpace(subject) {
		return ErrProblemAssetUnavailable
	}
	return validatePracticeAssetProblem(ctx, db, agentName, question, problem)
}
