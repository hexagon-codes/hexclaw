package usecase

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestBlankWorksheetCanonicalPreservesSevenFieldsAndOriginalOrder(t *testing.T) {
	entries := make([]gradingFinalEntry, 0, 3)
	for index, label := range []string{"1", "2", "3"} {
		question := RecognizedQuestion{ProblemID: "problem-" + label, AttemptID: "attempt-" + label, InputDigest: "input-" + label, ConfirmedVersion: 1, SourceNumberPath: []string{label}, DisplayLabel: label, Question: label + "+1=", CanonicalMarkdown: label + "+1="}
		item := PhotoGradeItem{Recognized: question, Status: PhotoBlankSolved, Grade: GradeResult{SolveOnly: true}, ParentGuide: &ParentTeachingGuide{Answer: "答案" + label, FullSolutionSteps: []string{"步骤" + label}, GradeLevelMethod: "方法" + label, LikelyMistakes: []string{"易错点" + label}, ParentTeachingSequence: []string{"讲解顺序" + label}, FollowUpQuestions: []string{"追问" + label}, CheckingMethod: "检查" + label}}
		status := k12.GradingAssessmentBlankSolved
		if index == 1 {
			item.Status, item.ParentGuide, status = PhotoOutOfScope, nil, k12.GradingAssessmentOutOfScope
		}
		raw, err := json.Marshal(gradingAssessmentCanonicalResult(item))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, gradingFinalEntry{question: question, assessment: &k12.GradingAssessmentItem{Status: status, ResultJSON: string(raw), ResultDigest: modelInvocationDigest(raw)}})
	}
	before := []string{entries[0].assessment.ResultJSON, entries[1].assessment.ResultJSON, entries[2].assessment.ResultJSON}
	markdown := renderCanonicalFinalForTask(entries, PhotoTaskBlankWorksheet, k12.GradingFinalArtifactCoverageComplete)
	assertBlankWorksheetCanonicalMode(t, markdown)
	if !strings.Contains(markdown, "共 3 题 · 2 题已解答 · 1 题未解答") {
		t.Fatal("solve statistics counted student correctness")
	}
	last := -1
	for _, label := range []string{"1", "2", "3"} {
		position := strings.Index(markdown, "### "+label+"\n")
		if position <= last {
			t.Fatalf("original order lost for %s:\n%s", label, markdown)
		}
		last = position
	}
	for _, label := range []string{"1", "3"} {
		for _, field := range []string{"答案", "步骤", "方法", "易错点", "讲解顺序", "追问", "检查"} {
			if !strings.Contains(markdown, field+label) {
				t.Fatalf("seven-field guide lost %s%s", field, label)
			}
		}
	}
	for index := range entries {
		if entries[index].assessment.ResultJSON != before[index] {
			t.Fatal("canonical renderer changed a source receipt")
		}
	}
	if grade := renderCanonicalFinalForTask(entries, PhotoTaskCompletedHomework, k12.GradingFinalArtifactCoverageComplete); grade != renderCanonicalGradingFinal(entries, nil) {
		t.Fatal("existing completed-homework body changed")
	}
}

func TestBlankWorksheetCanonicalGeneralGuidanceUsesSameModeVocabulary(t *testing.T) {
	for _, sample := range []struct {
		intent PhotoTaskIntent
		word   string
	}{{PhotoTaskBlankWorksheet, "解题"}, {PhotoTaskCompletedHomework, "批改"}} {
		markdown := renderCanonicalFinalForTask(nil, sample.intent, k12.GradingFinalArtifactCoverageGeneralGuidance)
		if !strings.Contains(markdown, "以上"+sample.word+"与家长讲法为通用参考。") {
			t.Fatalf("general guidance lost mode %s", sample.word)
		}
	}
}

func assertBlankWorksheetCanonicalMode(t *testing.T, markdown string) {
	t.Helper()
	for _, want := range []string{"# 空白卷 · 家长讲题指南", "## 解题摘要", "**解题状态：**"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("solve canonical missing %q:\n%s", want, markdown)
		}
	}
	for _, forbidden := range []string{"作业批改结果", "批改摘要", "批改状态", "题正确", "需要订正", "需关注的题", "未作答"} {
		if strings.Contains(markdown, forbidden) {
			t.Fatalf("solve canonical contains grading semantics %q:\n%s", forbidden, markdown)
		}
	}
}

func TestBlankWorksheetCanonicalModeUsesPersistentIntentSQLite(t *testing.T) {
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentBlankWorksheet)
	assertBlankWorksheetCanonicalMode(t, f.old.CanonicalMarkdown)
	stored, err := f.store.GetGradingFinalArtifact(context.Background(), "mingming", f.old.ArtifactID)
	if err != nil || !reflect.DeepEqual(stored, f.old) {
		t.Fatalf("frozen source mismatch: %v", err)
	}
	if strings.Index(stored.CanonicalMarkdown, "### target") > strings.Index(stored.CanonicalMarkdown, "### other") {
		t.Fatal("solve output changed the frozen original question order")
	}
}

func TestBlankWorksheetCanonicalPrintReplayKeepsFrozenSourceSQLite(t *testing.T) {
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentBlankWorksheet)
	before := f.old
	req, err := f.o.deps.gradingFinalArtifactPrintRequest(context.Background(), before, "空白卷 · 家长讲题指南")
	if err != nil {
		t.Fatal(err)
	}
	assertBlankWorksheetCanonicalMode(t, req.CanonicalMarkdown)
	stored, err := f.store.GetGradingFinalArtifact(context.Background(), "mingming", before.ArtifactID)
	if err != nil || !reflect.DeepEqual(stored, before) {
		t.Fatalf("print projection mutated the frozen source: %v", err)
	}
	if !strings.HasPrefix(req.SourceRef, "final_artifact:"+before.ArtifactID+":"+before.ArtifactDigest+":") || !strings.HasSuffix(req.SourceRef, ":"+imageFinalPDFRenderContractVersion) {
		t.Fatal("print projection lost the exact source identity")
	}
}

func TestBlankWorksheetCanonicalSourceCorrectionKeepsModeAndHistorySQLite(t *testing.T) {
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentBlankWorksheet)
	before := f.old
	result, err := f.o.CorrectCompletedSource(context.Background(), "guardian-final", f.dispatch, "target", f.request)
	if err != nil || result.Artifact == nil {
		t.Fatalf("controlled correction failed: %v", err)
	}
	assertBlankWorksheetCanonicalMode(t, result.Artifact.CanonicalMarkdown)
	if result.Artifact.ArtifactID == before.ArtifactID {
		t.Fatal("correction overwrote the old artifact identity")
	}
	stored, err := f.store.GetGradingFinalArtifact(context.Background(), "mingming", before.ArtifactID)
	if err != nil || !reflect.DeepEqual(stored, before) {
		t.Fatalf("correction mutated the old artifact: %v", err)
	}
}
