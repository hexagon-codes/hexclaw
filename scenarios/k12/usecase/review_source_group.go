package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// ReviewSourceMemberRequest 明确选择审核过的输入回执，不按题目相似度挑选来源。
type ReviewSourceMemberRequest struct {
	RecordID         string `json:"record_id"`
	JobID            string `json:"job_id"`
	ProblemID        string `json:"problem_id"`
	AttemptID        string `json:"attempt_id"`
	ConfirmedVersion int    `json:"confirmed_version"`
	InputRevision    int    `json:"input_revision"`
	InputDigest      string `json:"input_digest"`
	ResultDigest     string `json:"result_digest"`
}

type ReviewSourceEvidenceRequest struct {
	Kind string `json:"kind"`
	Note string `json:"note"`
}

// RecordReviewSourceGroupRequest 只记录来源审核，不接收客户端编写的代表题面或答案。
type RecordReviewSourceGroupRequest struct {
	AgentName         string                      `json:"agent"`
	CanonicalRecordID string                      `json:"canonical_record_id"`
	Members           []ReviewSourceMemberRequest `json:"members"`
	Evidence          ReviewSourceEvidenceRequest `json:"evidence"`
}

func (d Deps) RecordReviewSourceGroup(ctx context.Context, request RecordReviewSourceGroupRequest) (k12.ReviewSourceGroup, bool, error) {
	if d.Records == nil {
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("review source storage is unavailable")
	}
	if strings.TrimSpace(request.AgentName) == "" || request.CanonicalRecordID == "" || len(request.Members) < 2 ||
		request.Evidence.Kind != k12.ReviewSourceEvidenceReviewed || strings.TrimSpace(request.Evidence.Note) == "" {
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("%w: agent, canonical record, reviewed evidence and at least two members are required", ErrInvalidInput)
	}
	ids, seen := make([]string, 0, len(request.Members)), make(map[string]bool)
	for _, member := range request.Members {
		if member.RecordID == "" || seen[member.RecordID] || member.JobID == "" || member.ProblemID == "" ||
			member.AttemptID == "" || member.ConfirmedVersion < 1 || member.InputRevision < 1 || member.InputDigest == "" || member.ResultDigest == "" {
			return k12.ReviewSourceGroup{}, false, fmt.Errorf("%w: every member requires one distinct record and a complete frozen input identity", ErrInvalidInput)
		}
		ids, seen[member.RecordID] = append(ids, member.RecordID), true
	}
	sources, err := d.Records.ListReviewPaperSources(ctx, request.AgentName, ids)
	if err != nil {
		return k12.ReviewSourceGroup{}, false, err
	}
	group := k12.ReviewSourceGroup{
		AgentName: request.AgentName, CanonicalRecordID: request.CanonicalRecordID, CreatedAt: d.now(),
		Evidence: k12.ReviewSourceGroupEvidence{Kind: request.Evidence.Kind, Note: request.Evidence.Note},
	}
	for _, member := range request.Members {
		record, err := d.Records.Get(ctx, member.RecordID)
		if err != nil {
			return k12.ReviewSourceGroup{}, false, err
		}
		if record.AgentName != request.AgentName || record.Collection != k12.CollectionMistakes {
			return k12.ReviewSourceGroup{}, false, records.ErrNotFound
		}
		var fields k12.MistakeFields
		if err := json.Unmarshal([]byte(record.Fields), &fields); err != nil {
			return k12.ReviewSourceGroup{}, false, fmt.Errorf("decode reviewed mistake: %w", err)
		}
		item := ReviewItem{Record: record, Fields: fields}
		var selected *k12storage.ReviewPaperSource
		for i := range sources {
			source := &sources[i]
			if source.RecordID == member.RecordID && source.JobID == member.JobID && source.ProblemID == member.ProblemID &&
				source.AttemptID == member.AttemptID && source.ConfirmedVersion == member.ConfirmedVersion &&
				source.InputRevision == member.InputRevision && source.InputDigest == member.InputDigest && source.ResultDigest == member.ResultDigest {
				selected = source
				break
			}
		}
		if selected == nil {
			return k12.ReviewSourceGroup{}, false, k12storage.ErrReviewSourceGroupStale
		}
		question, valid := reviewedSourceQuestion(item, *selected)
		if !valid || !reviewPaperRecordMatchesQuestion(item, question) {
			return k12.ReviewSourceGroup{}, false, k12storage.ErrReviewSourceGroupStale
		}
		var parentPath []string
		if selected.ParentProblemID == question.ParentProblemID && selected.ParentPageAssetID == question.PageAssetID && selected.ParentCanonicalVersion > 0 {
			_ = json.Unmarshal([]byte(selected.ParentSourceNumberPathJSON), &parentPath)
		}
		group.Members = append(group.Members, k12.ReviewSourceGroupMember{
			RecordID: member.RecordID, QuestionDigest: k12.ReviewSourceTextDigest(fields.Question),
			JobID: member.JobID, ProblemID: member.ProblemID, AttemptID: member.AttemptID,
			ConfirmedVersion: member.ConfirmedVersion, InputRevision: member.InputRevision,
			InputDigest: member.InputDigest, ResultDigest: member.ResultDigest, PageDigest: selected.PageDigest,
			SourceNumberPath: question.SourceNumberPath, ParentSourceNumberPath: parentPath,
		})
		if member.RecordID == request.CanonicalRecordID {
			group.CanonicalStatement = compactReviewPaperQuestionForSource(question, *selected)
		}
	}
	normalized, err := group.Normalized()
	if err != nil {
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return d.Records.PutReviewSourceGroup(ctx, normalized)
}

// 显式审核关联核对确切的已发布版本；不会把尚未发布的新输入借用旧回执。
func reviewedSourceQuestion(item ReviewItem, source k12storage.ReviewPaperSource) (RecognizedQuestion, bool) {
	if item.Record == nil || source.AgentName != item.Record.AgentName || source.RecordID != item.Record.RecordID ||
		source.InputRevision < 1 || source.PublishedRevision != source.InputRevision || source.ConfirmedVersion < 1 ||
		source.PageAssetID == "" || source.PageDigest == "" || source.ResultDigest != modelInvocationDigest([]byte(source.ResultJSON)) {
		return RecognizedQuestion{}, false
	}
	var frozen PhotoGradeItem
	if json.Unmarshal([]byte(source.ResultJSON), &frozen) != nil {
		return RecognizedQuestion{}, false
	}
	q := frozen.Recognized
	if q.ProblemID != source.ProblemID || q.AttemptID != source.AttemptID || q.InputDigest != source.InputDigest ||
		q.PageAssetID != source.PageAssetID || q.ConfirmedVersion != source.ConfirmedVersion || q.CanonicalVersion < 1 || q.RawTranscription == "" {
		return RecognizedQuestion{}, false
	}
	return q, true
}
