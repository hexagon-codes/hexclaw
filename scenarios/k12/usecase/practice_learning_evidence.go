package usecase

import (
	"context"
	"fmt"
	"strings"

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
	source, err := d.Records.Get(ctx, item.SourceProblemID)
	if err != nil {
		return nil, err
	}
	if source.AgentName != view.Record.AgentName || source.Collection != k12.CollectionMistakes {
		return events, nil
	}
	fields, err := k12.ParseMistakeFields(source.Fields)
	if err != nil {
		return nil, err
	}
	if fields.LastRetriedAt > 0 && matched.ReturnedAt-fields.LastRetriedAt >= MasteryGapInterval {
		events = append(events, k12.LearningEvidence{Kind: k12.LearningEvidenceDelayed, Source: "system_timestamps", EventID: matched.ReturnID, OccurredAt: matched.ReturnedAt, BasisID: source.RecordID, BasisAt: fields.LastRetriedAt})
	}
	return events, nil
}
