package usecase

import (
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type frozenGradingMark struct {
	Box            BBox `json:"bbox"`
	QuestionRegion bool `json:"question_region,omitempty"`
}

// 实际进入终稿的图和题目在同一快照冻结。基于题目区域绘制的问号不产生学生答案框。
func freezeGradingMarks(result *PhotoGradeResult, artifact k12.GradingFinalArtifact) map[string]frozenGradingMark {
	if result == nil || len(result.Items) == 0 || result.EffectiveTaskIntent() != PhotoTaskCompletedHomework {
		return nil
	}
	boxes := make(map[string]frozenGradingMark)
	image := result.AnnotatedImage
	if image == nil {
		if artifact.HasAnnotatedAsset() {
			return nil
		}
		return boxes
	}
	if image.VisibleMarkIndexes == nil || !artifact.HasAnnotatedAsset() || teachingReportResourceDigest(image.Data) != artifact.AnnotatedDigest {
		return nil
	}
	for _, mark := range renderedPhotoMarks(result.Items, image) {
		question := result.Items[mark.QuestionNumber-1].Recognized
		boxes[question.ProblemID] = frozenGradingMark{Box: mark.BBox, QuestionRegion: question.BBox == nil}
	}
	return boxes
}

func restoreDrawnAnswerBoxes(photo *PhotoGradeResult, artifact k12.GradingFinalArtifact, assessments []k12.GradingAssessmentItem) bool {
	report := artifact.TeachingReport
	if report == nil || report.Source.SourceID != artifact.JobID || report.Source.Kind != k12.TeachingReportSourceGrading || len(report.SourcePages) != 1 {
		return false
	}
	var frozen struct {
		Boxes map[string]frozenGradingMark `json:"drawn_marks"`
	}
	if json.Unmarshal([]byte(artifact.ReportSnapshotJSON), &frozen) != nil || frozen.Boxes == nil {
		return false
	}
	if artifact.ArtifactDigest != k12.ComputeGradingFinalArtifactDigest(artifact) || len(assessments) != len(photo.Items) {
		return false
	}
	page := report.SourcePages[0]
	if artifact.HasAnnotatedAsset() {
		if page.AssetID != artifact.AnnotatedAssetID || page.AssetDigest != artifact.AnnotatedDigest || page.SourceDigest != artifact.OriginalSourceDigest ||
			photo.AnnotatedImage == nil || teachingReportResourceDigest(photo.AnnotatedImage.Data) != page.AssetDigest {
			return false
		}
	} else if len(frozen.Boxes) != 0 || photo.AnnotatedImage != nil || page.SourceDigest != page.AssetDigest {
		return false
	}
	sources := make(map[string]k12.TeachingReportProblemSource, len(report.ProblemSources))
	for _, source := range report.ProblemSources {
		sources[source.ProblemID] = source
	}
	matched := 0
	for _, assessment := range assessments {
		source, ok := sources[assessment.ProblemID]
		if !ok || source.AttemptID != assessment.AttemptID || source.InputRevision != assessment.InputRevision ||
			source.InputDigest != assessment.InputDigest || source.AssessmentDigest != assessment.ResultDigest {
			return false
		}
		if mark, drawn := frozen.Boxes[assessment.ProblemID]; drawn {
			if !photoAnnotationHasTrustedBBox(PhotoAnnotation{BBox: mark.Box}) || (mark.QuestionRegion &&
				assessment.Status != k12.GradingAssessmentAnswerUnclear && assessment.Status != k12.GradingAssessmentUntrusted) {
				return false
			}
			matched++
		}
	}
	if matched != len(frozen.Boxes) {
		return false
	}
	for _, item := range photo.Items {
		q := item.Recognized
		source, ok := sources[q.ProblemID]
		if !ok || q.AttemptID != source.AttemptID || q.ConfirmedVersion != source.InputRevision || q.InputDigest != source.InputDigest {
			return false
		}
	}
	restoreRenderedAnswerBoxes(photo, frozen.Boxes)
	photo.ImageWarning = photoAnnotationCoverageWarning(*photo)
	return true
}

// Only an older snapshot without drawn_marks may fall back to the aggregate.
// When a snapshot explicitly records marks, failed validation must not recover
// possibly unrelated rectangles from an earlier stage.
func canRestoreLegacyAnswerBoxes(snapshot string) bool {
	if snapshot == "" {
		return true
	}
	var markField struct {
		Boxes json.RawMessage `json:"drawn_marks"`
	}
	return json.Unmarshal([]byte(snapshot), &markField) == nil && len(markField.Boxes) == 0
}

func restoreRenderedAnswerBoxes(photo *PhotoGradeResult, boxes map[string]frozenGradingMark) {
	for i := range photo.Items {
		q := &photo.Items[i].Recognized
		q.BBox = nil
		if mark, drawn := boxes[q.ProblemID]; drawn && !mark.QuestionRegion {
			q.BBox = &mark.Box
		}
	}
}

// 老报告的这一节只在冻结时确有覆盖提示才写入；没有完整逐题几何时仍可原样回放该提示。
func frozenAnnotationWarning(artifact k12.GradingFinalArtifact) string {
	if report := artifact.TeachingReport; report != nil && artifact.ArtifactDigest == k12.ComputeGradingFinalArtifactDigest(artifact) {
		for _, section := range report.DeepSections {
			if section.ID == "report:annotation" {
				for _, block := range section.Blocks {
					if block.ID == "report:annotation:note" {
						return block.Text
					}
				}
			}
		}
	}
	return ""
}
