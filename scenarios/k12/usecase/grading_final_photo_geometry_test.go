package usecase

import (
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestFinalDrawnAnswerBoxRequiresTheSameArtifactAndAssessment(t *testing.T) {
	box := BBox{X: .1, Y: .2, W: .3, H: .1}
	rendered := &RenderedPhoto{Data: []byte("marked image"), MIME: "image/png", VisibleMarkIndexes: []int{0}}
	photo := PhotoGradeResult{TaskIntent: PhotoTaskCompletedHomework, AnnotatedImage: rendered, Items: []PhotoGradeItem{
		{Status: PhotoCorrect, Recognized: RecognizedQuestion{ProblemID: "p", AttemptID: "a", ConfirmedVersion: 1, InputDigest: "input", BBox: &box}},
	}}
	source := k12.TeachingReportProblemSource{ProblemID: "p", AttemptID: "a", InputRevision: 1, InputDigest: "input", AssessmentDigest: "receipt"}
	assessment := k12.GradingAssessmentItem{ProblemID: "p", AttemptID: "a", InputRevision: 1, InputDigest: "input", ResultDigest: "receipt"}
	artifact := k12.GradingFinalArtifact{JobID: "job", AnnotatedAssetOwnerScope: "owner", AnnotatedAssetID: "asset", AnnotatedMIME: rendered.MIME,
		AnnotatedDigest: teachingReportResourceDigest(rendered.Data), OriginalSourceDigest: teachingReportResourceDigest([]byte("original image"))}
	report := k12.TeachingReport{Source: k12.TeachingReportSource{Kind: k12.TeachingReportSourceGrading, SourceID: artifact.JobID},
		ProblemSources: []k12.TeachingReportProblemSource{source},
		SourcePages:    []k12.TeachingReportSourcePage{{AssetID: artifact.AnnotatedAssetID, AssetDigest: artifact.AnnotatedDigest, SourceDigest: artifact.OriginalSourceDigest}}}
	snapshot := TeachingReportFrozenSnapshot{Report: report, DrawnMarks: freezeGradingMarks(&photo, artifact)}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	artifact.TeachingReport, artifact.ReportSnapshotJSON = &report, string(raw)
	artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
	photo.Items[0].Recognized.BBox = nil
	if !restoreDrawnAnswerBoxes(&photo, artifact, []k12.GradingAssessmentItem{assessment}) || photo.ImageWarning != "" ||
		photo.Items[0].Recognized.BBox == nil || *photo.Items[0].Recognized.BBox != box {
		t.Fatal("actual mark was not restored")
	}
	photo.Items[0].Recognized.BBox = nil
	other := assessment
	other.InputRevision++
	if restoreDrawnAnswerBoxes(&photo, artifact, []k12.GradingAssessmentItem{other}) {
		t.Fatal("different input revision reused a drawn answer")
	}
	photo.AnnotatedImage = &RenderedPhoto{Data: []byte("another image"), MIME: rendered.MIME}
	if restoreDrawnAnswerBoxes(&photo, artifact, []k12.GradingAssessmentItem{assessment}) || photo.Items[0].Recognized.BBox != nil {
		t.Fatal("different final image reused a drawn answer")
	}
}

func TestFrozenQuestionRegionMarkIsNotAnAnswerBox(t *testing.T) {
	photo := PhotoGradeResult{TaskIntent: PhotoTaskCompletedHomework,
		AnnotatedImage: &RenderedPhoto{Data: []byte("question mark"), MIME: "image/png", VisibleMarkIndexes: []int{0}},
		Items: []PhotoGradeItem{{Status: PhotoAnswerUnclear, Recognized: RecognizedQuestion{ProblemID: "p", SourceWidth: 100,
			SourceHeight: 100, SourceRegion: &k12.SourcePixelRegion{X: 10, Y: 20, Width: 30, Height: 40}}}},
	}
	artifact := k12.GradingFinalArtifact{AnnotatedAssetOwnerScope: "owner", AnnotatedAssetID: "asset", AnnotatedMIME: "image/png",
		AnnotatedDigest: teachingReportResourceDigest(photo.AnnotatedImage.Data), OriginalSourceDigest: teachingReportResourceDigest([]byte("source"))}
	marks := freezeGradingMarks(&photo, artifact)
	want := BBox{X: .1, Y: .2, W: .3, H: .4}
	if mark := marks["p"]; !mark.QuestionRegion || mark.Box != want {
		t.Fatalf("missing actual question region: %+v", mark)
	}
	restoreRenderedAnswerBoxes(&photo, marks)
	if photo.Items[0].Recognized.BBox != nil {
		t.Fatal("region-only question mark claimed student handwriting coordinates")
	}
}

func TestLegacyGeometryFallbackRequiresAnAbsentDrawingField(t *testing.T) {
	for snapshot, want := range map[string]bool{
		"": true, "{}": true, `{"drawn_marks":{}}`: false,
		`{"drawn_marks":null}`: false, `{"drawn_marks":{"x":"invalid"}}`: false, "{": false,
	} {
		if got := canRestoreLegacyAnswerBoxes(snapshot); got != want {
			t.Errorf("snapshot %q: legacy fallback = %v, want %v", snapshot, got, want)
		}
	}
}
