package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func normalizePracticeAnswerContext(value string) (string, error) {
	value = strings.TrimSpace(value)
	switch value {
	case "", k12.LearningEvidenceUnknown:
		return "", nil
	case k12.LearningEvidencePrompted, k12.LearningEvidenceIndependent:
		return value, nil
	default:
		return "", fmt.Errorf("%w: unsupported answer_context", ErrInvalidInput)
	}
}

func practiceReturnContextMatches(ret k12.PracticeReturnAsset, input PracticeReturnInput) bool {
	requested, err := normalizePracticeAnswerContext(input.AnswerContext)
	if err != nil {
		return false
	}
	actual := ""
	for _, event := range ret.LearningEvidence {
		if event.Kind == k12.LearningEvidencePrompted || event.Kind == k12.LearningEvidenceIndependent {
			actual = event.Kind
		}
	}
	return actual == requested
}

// practiceReturnLearningEvidenceBasis 在任何来源复习写入前冻结本次可观察的时间依据。
func (d Deps) practiceReturnLearningEvidenceBasis(ctx context.Context, view PracticeSetView, ret k12.PracticeReturnAsset, itemIDs []string) (map[string]int64, error) {
	selected := make(map[string]bool, len(itemIDs))
	for _, id := range itemIDs {
		selected[id] = true
	}
	basis := make(map[string]int64)
	for _, item := range view.Fields.Items {
		if !selected[item.ItemID] || item.SourceProblemID == "" {
			continue
		}
		if _, seen := basis[item.SourceProblemID]; seen {
			continue
		}
		basis[item.SourceProblemID] = 0
		source, err := d.Records.Get(ctx, item.SourceProblemID)
		if errors.Is(err, records.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if source == nil || source.AgentName != view.Record.AgentName || source.Collection != k12.CollectionMistakes {
			continue
		}
		fields, err := k12.ParseMistakeFields(source.Fields)
		if err != nil {
			return nil, err
		}
		if fields.LastRetriedAt <= ret.ReturnedAt {
			basis[item.SourceProblemID] = fields.LastRetriedAt
		}
		// 旧批次仅复用同一回传、同一来源已有的可追溯依据，不借用其他题的条件。
		for _, prior := range view.Fields.Items {
			if prior.SourceProblemID != item.SourceProblemID {
				continue
			}
			for _, event := range prior.LearningEvidence {
				if event.Kind == k12.LearningEvidenceDelayed && event.Source == "system_timestamps" &&
					event.EventID == ret.ReturnID && event.BasisID == item.SourceProblemID &&
					event.OccurredAt == ret.ReturnedAt && event.BasisAt > 0 &&
					ret.ReturnedAt-event.BasisAt >= MasteryGapInterval {
					basis[item.SourceProblemID] = event.BasisAt
				}
			}
		}
	}
	return basis, nil
}

func (d Deps) freezeLegacyPracticeReturnEvidenceBasis(ctx context.Context, view *PracticeSetView, results []PracticeGradeResult) error {
	needed := make(map[string]bool)
	for _, result := range results {
		if result.ReturnID != "" {
			needed[result.ReturnID] = true
		}
	}
	for i := range view.Fields.ReturnAssets {
		ret := &view.Fields.ReturnAssets[i]
		if !needed[ret.ReturnID] || ret.LearningEvidenceBasis != nil {
			continue
		}
		ids := ret.ItemIDs
		if ret.AutoMatch {
			ids = ret.CandidateItemIDs
		}
		basis, err := d.practiceReturnLearningEvidenceBasis(ctx, *view, *ret, ids)
		if err != nil {
			return err
		}
		ret.LearningEvidenceBasis = basis
	}
	return nil
}

// olderPracticeResultReturn 不让较早回传的迟到投影覆盖已保存的较新结果。
func olderPracticeResultReturn(view PracticeSetView, item k12.PracticeItem, incoming string) bool {
	current := item.ResultReturnID
	if current == "" {
		for _, event := range item.LearningEvidence {
			if event.Source == "guardian_report" || event.Source == "system_timestamps" {
				current = event.EventID
				break
			}
		}
	}
	if current == "" || current == incoming {
		return false
	}
	oldIndex, incomingIndex := -1, -1
	for i, ret := range view.Fields.ReturnAssets {
		covered := false
		for _, id := range ret.ItemIDs {
			if id == item.ItemID {
				covered = true
			}
		}
		if !covered {
			continue
		}
		if ret.ReturnID == current {
			oldIndex = i
		}
		if ret.ReturnID == incoming {
			incomingIndex = i
		}
	}
	return incomingIndex >= 0 && oldIndex > incomingIndex
}

func (d Deps) practiceResultLearningEvidence(ctx context.Context, view PracticeSetView, item k12.PracticeItem, result PracticeGradeResult, evidence string) ([]k12.LearningEvidence, error) {
	if evidence == k12.PracticeResultHumanConfirmed {
		return []k12.LearningEvidence{{Kind: k12.LearningEvidenceManual, Source: "guardian_confirmation", EventID: item.PracticeProblemID, OccurredAt: d.now()}}, nil
	}
	// 旧调用没有回传关联时保持未知，不能借用最近一张照片的作答条件。
	if result.ReturnID == "" {
		return nil, nil
	}
	var matched *k12.PracticeReturnAsset
	for i := range view.Fields.ReturnAssets {
		ret := &view.Fields.ReturnAssets[i]
		if ret.ReturnID != result.ReturnID {
			continue
		}
		for _, id := range ret.ItemIDs {
			if id == item.ItemID {
				matched = ret
			}
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("%w: result has no matching return evidence", ErrInvalidInput)
	}
	events := append([]k12.LearningEvidence(nil), matched.LearningEvidence...)
	if item.SourceProblemID == "" {
		return events, nil
	}
	basisAt := matched.LearningEvidenceBasis[item.SourceProblemID]
	if basisAt > 0 && matched.ReturnedAt-basisAt >= MasteryGapInterval {
		events = append(events, k12.LearningEvidence{Kind: k12.LearningEvidenceDelayed, Source: "system_timestamps", EventID: matched.ReturnID, OccurredAt: matched.ReturnedAt, BasisID: item.SourceProblemID, BasisAt: basisAt})
	}
	return events, nil
}
