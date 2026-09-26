package k12storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// WeeklyCandidateTargetSource 只投影已持久的教学事实，课程关联由实际求解回执证明。
type WeeklyCandidateTargetSource struct {
	SourceRef, Subject, GradeTerm, KnowledgePoint, Question string
	SourceRevision                                          int
	GroundingResultJSON                                     string
}

// WeeklyTextbookPage 保留当前课程页的原始正文和确切来源，不代替学生评估。
type WeeklyTextbookPage struct {
	LogicalPage, PDFPage   int
	Content, ContentDigest string
	SourceRef              string
	EvidenceRefs           []string
}

// ListWeeklyTextbookPages 只读取当前有效绑定中已验证的课程页。
func (s *Store) ListWeeklyTextbookPages(ctx context.Context, requested TextbookScope, progress k12.CurriculumProgress) ([]WeeklyTextbookPage, error) {
	if progress.VerifiedPageFrom == nil || progress.VerifiedPageTo == nil {
		return nil, nil
	}
	scope, found, err := s.GetActiveTextbookGroundingScope(ctx, requested)
	if err != nil || !found {
		return nil, err
	}
	if scope.TextbookBindingID != progress.TextbookBindingID || scope.TextbookManifestID != progress.TextbookManifestID {
		return nil, nil
	}
	var pages []WeeklyTextbookPage
	for _, ref := range scope.PageRefs {
		if ref.LogicalPage < *progress.VerifiedPageFrom || ref.LogicalPage > *progress.VerifiedPageTo {
			continue
		}
		page := WeeklyTextbookPage{LogicalPage: ref.LogicalPage, PDFPage: ref.PDFPage}
		err := s.db.QueryRowContext(ctx, `SELECT p.content,p.content_digest
			FROM k12_textbook_catalog_jobs j JOIN kb_ingest_page_checkpoints p
			ON p.job_id=j.ingest_job_id AND p.source_digest=j.source_digest
			WHERE j.manifest_id=? AND j.owner_id=? AND j.document_id=?
			AND j.document_generation=? AND j.source_digest=? AND j.state='succeeded'
			AND p.page_number=?`, scope.TextbookManifestID, requested.OwnerID, scope.DocumentID,
			scope.DocumentGeneration, scope.SourceDigest, ref.PDFPage).Scan(&page.Content, &page.ContentDigest)
		if err != nil {
			return nil, fmt.Errorf("weekly textbook page unavailable: %w", err)
		}
		if strings.TrimSpace(page.Content) == "" || sha256Hex([]byte(page.Content)) != page.ContentDigest {
			return nil, fmt.Errorf("weekly textbook page content changed")
		}
		page.SourceRef = fmt.Sprintf("textbook:%s:%s:%s:%d:%d:%s", scope.TextbookBindingID,
			scope.TextbookManifestID, scope.DocumentID, scope.DocumentGeneration, ref.PDFPage, page.ContentDigest)
		page.EvidenceRefs = append([]string{page.SourceRef, fmt.Sprintf("logical_page:%d", ref.LogicalPage),
			"source_sha256:" + scope.SourceDigest}, ref.SegmentRefs...)
		pages = append(pages, page)
	}
	return pages, nil
}

func (s *Store) ListWeeklyCandidateTargetSources(ctx context.Context, owner, agent, grade string) ([]WeeklyCandidateTargetSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT record_id,subject,grade_term,knowledge_point,question,version
		FROM k12_mistakes WHERE agent_name=? AND grade_term=? AND subject='数学'
		AND status NOT IN ('mastered','archived') AND trim(knowledge_point) NOT IN ('','其他')
		ORDER BY COALESCE(due_at,9223372036854775807),created_at,record_id`, agent, grade)
	if err != nil {
		return nil, err
	}
	var out []WeeklyCandidateTargetSource
	for rows.Next() {
		var target WeeklyCandidateTargetSource
		if err := rows.Scan(&target.SourceRef, &target.Subject, &target.GradeTerm, &target.KnowledgePoint, &target.Question, &target.SourceRevision); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, target)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// 已发布题目的冻结学段关联真实评估；当前纠正替代原教学标签及求解回执。
	rows, err = s.db.QueryContext(ctx, `WITH current AS (
		SELECT g.*, (SELECT c.correction_json FROM k12_assessment_corrections c
			WHERE c.agent_name=g.agent_name AND c.job_id=g.job_id AND c.problem_id=g.problem_id
			AND c.input_revision=g.input_revision ORDER BY c.correction_revision DESC LIMIT 1) AS correction
		FROM k12_grading_assessment_items g WHERE g.agent_name=? AND g.current_disposition='current'
	), effective AS (
		SELECT g.*,COALESCE(json_extract(g.correction,'$.assessment.result_json'),g.result_json) AS teaching,
		COALESCE(json_extract(g.correction,'$.assessment.solve_invocation_id'),g.solve_invocation_id) AS effective_solve,
  COALESCE(json_extract(g.correction,'$.assessment.answer_source'),g.answer_source_json) AS effective_answer_source
		FROM current g
	) SELECT DISTINCT g.job_id,g.problem_id,g.input_revision,g.teaching,COALESCE(i.result_json,'')
	FROM effective g JOIN k12_problem_asset_publications p
	ON json_extract(p.verification_json,'$.agent_name')=g.agent_name
	JOIN k12_grading_item_invocations original ON original.agent_name=g.agent_name
	AND original.item_invocation_id=json_extract(p.verification_json,'$.invocation_id')
	AND original.job_id=g.job_id AND original.problem_id=g.problem_id AND original.input_revision=g.input_revision
	JOIN k12_problem_asset_versions v ON v.owner_id=p.owner_id AND v.asset_id=p.asset_id AND v.asset_version=p.asset_version
	LEFT JOIN k12_grading_item_invocations i ON i.agent_name=g.agent_name AND i.item_invocation_id=g.effective_solve
	WHERE v.owner_id=? AND json_extract(v.facts_json,'$.answer_context.grade_term')=?
	AND json_extract(g.teaching,'$.Recognized.Subject')='数学'
	AND json_array_length(g.teaching,'$.Recognized.KnowledgePoints')=1
 UNION SELECT DISTINCT g.job_id,g.problem_id,g.input_revision,g.teaching,COALESCE(i.result_json,'')
 FROM effective g JOIN k12_problem_asset_adoptions a
 ON a.adoption_id=json_extract(g.effective_answer_source,'$.adoption_id')
 AND a.job_id=g.job_id AND a.problem_id=g.problem_id AND a.input_revision=g.input_revision AND a.input_digest=g.input_digest
 JOIN k12_problem_asset_versions v ON v.owner_id=a.owner_id AND v.asset_id=a.asset_id AND v.asset_version=a.asset_version
 LEFT JOIN k12_grading_item_invocations i ON i.agent_name=g.agent_name AND i.item_invocation_id=g.effective_solve
 WHERE a.owner_id=? AND json_extract(v.facts_json,'$.answer_context.grade_term')=?
 AND json_extract(g.teaching,'$.Recognized.Subject')='数学'
 AND json_array_length(g.teaching,'$.Recognized.KnowledgePoints')=1
 ORDER BY 1,2`, agent, owner, grade, owner, grade)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, problemID, result string
		var target WeeklyCandidateTargetSource
		if err := rows.Scan(&jobID, &problemID, &target.SourceRevision, &result, &target.GroundingResultJSON); err != nil {
			return nil, err
		}
		var teaching struct {
			Recognized struct {
				Subject, Question string
				CanonicalMarkdown string `json:"canonical_markdown"`
				KnowledgePoints   []string
			}
		}
		if err := json.Unmarshal([]byte(result), &teaching); err != nil {
			return nil, err
		}
		target.SourceRef = fmt.Sprintf("assessment:%s:%s", jobID, problemID)
		target.Subject, target.GradeTerm = teaching.Recognized.Subject, grade
		target.KnowledgePoint = teaching.Recognized.KnowledgePoints[0]
		target.Question = teaching.Recognized.CanonicalMarkdown
		if target.Question == "" {
			target.Question = teaching.Recognized.Question
		}
		out = append(out, target)
	}
	return out, rows.Err()
}

// WeeklyPracticeProblemHashes 包含尚未移入练习集的周练，防止同孩子下一批重复。
func (s *Store) WeeklyPracticeProblemHashes(ctx context.Context, agent string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT json_extract(j.value,'$.subject'),json_extract(j.value,'$.prompt_markdown')
		FROM k12_weekly_practice_plans p,json_each(p.plan_json,'$.tracks') t,json_each(t.value,'$.items') j WHERE p.agent_name=?
		UNION SELECT json_extract(j.value,'$.subject'),json_extract(j.value,'$.prompt_markdown')
		FROM k12_weekly_arithmetic_batches b,json_each(b.items_json) j WHERE b.agent_name=?
  UNION SELECT json_extract(j.value,'$.subject'),json_extract(j.value,'$.prompt_markdown')
  FROM k12_weekly_practice_snapshots s,json_each(s.snapshot_json,'$.tracks') t,json_each(t.value,'$.items') j WHERE s.agent_name=?
  UNION SELECT i.subject,i.question_markdown FROM k12_practice_set_items i JOIN k12_practice_sets p ON p.record_id=i.set_record_id WHERE p.agent_name=?
  UNION SELECT p.subject,p.stem_markdown FROM k12_problems p JOIN k12_attempts a ON a.agent_name=p.agent_name AND a.problem_id=p.problem_id WHERE p.agent_name=? AND a.answer_state='present'
  UNION SELECT json_extract(result_json,'$.Recognized.Subject'),COALESCE(NULLIF(json_extract(result_json,'$.Recognized.canonical_markdown'),''),json_extract(result_json,'$.Recognized.Question')) FROM k12_grading_assessment_items WHERE agent_name=? AND json_extract(result_json,'$.Recognized.AnswerState')='present'`, agent, agent, agent, agent, agent, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var subject, question *string
		if err := rows.Scan(&subject, &question); err != nil {
			return nil, err
		}
		if subject != nil && question != nil {
			hash, _, err := k12.StablePracticeProblemHash(k12.PracticeCandidateProblem{Subject: *subject, QuestionMarkdown: *question})
			if err != nil {
				return nil, err
			}
			hashes = append(hashes, hash)
		}
	}
	return hashes, rows.Err()
}
