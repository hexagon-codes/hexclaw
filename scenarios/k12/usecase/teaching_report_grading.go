package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func (o *GradingOrchestrator) freezeGradingTeachingReport(ctx context.Context, run *gradingRun, job GradingJobView, artifact *k12.GradingFinalArtifact, assessments []k12.GradingAssessmentItem, skips []k12.ProblemSkipReceipt, sourceDigests []byte, generation int64) error {
	if run.req.TeachingReport == nil {
		return nil
	}
	input := GradingTeachingReportInput{AgentName: job.Record.AgentName, Metadata: run.req.TeachingReport.Metadata, Questions: run.questions, Assessments: assessments, Skips: skips,
		Source: k12.TeachingReportSource{Kind: k12.TeachingReportSourceGrading, SourceID: job.Record.RecordID, Revision: int(generation) + 1, Digest: teachingReportResourceDigest(sourceDigests)}}
	if input.Metadata.Subject == "" {
		var subjects []string
		seen := map[string]bool{}
		for _, question := range run.questions {
			if question.ProblemKind == ProblemKindCompoundParent {
				continue
			}
			subject := firstNonEmpty(question.Subject, firstNonEmpty(run.req.Subject, "数学"))
			if !seen[subject] {
				subjects = append(subjects, subject)
				seen[subject] = true
			}
		}
		input.Metadata.Subject = strings.Join(subjects, "、")
	}
	byID := make(map[string]RecognizedQuestion, len(run.questions))
	for _, q := range run.questions {
		byID[q.ProblemID] = q
	}
	for _, assessment := range assessments {
		q := byID[assessment.ProblemID]
		item, err := replayGradingAssessmentItem(q, assessment)
		if err != nil {
			return err
		}
		solved := item.Solve
		if solved.Solution == "" {
			solved.Solution = item.Grade.Solution
			solved.Evidence = item.Grade.Evidence
		}
		if solved.Solution == "" || solved.OutOfScope || solved.hasDeterministicProblemIssue() {
			input.Content = append(input.Content, TeachingReportQuestionContent{ProblemID: q.ProblemID, ShortTitle: "本题所求", AnswerAnalysis: k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceUnavailable, UnavailableReason: firstNonEmpty(solved.ReportUnavailableReason, "本题尚无可用的参考解答")}})
			continue
		}
		var guide ParentTeachingGuide
		sourceDigest := solved.Evidence.SolverOutputDigest
		restoredReceipt := ""
		if item.ParentGuide != nil && item.ParentGuide.ReportContent != nil {
			guide = *item.ParentGuide
		} else if solved.ReportContent != nil {
			guide.ReportContent = solved.ReportContent
		} else if solved.Generation != nil {
			guide, err = auditedSolveParentTeachingGuide(solved)
			if err != nil {
				return err
			}
		} else {
			var ok bool
			guide, ok = deterministicParentTeachingGuideForEvidence(o.deps.parentTeachingGuideRequest(ctx, photoItemGradeRequest(run.req, q), solved, item.Grade.Outcome), solved.Evidence)
			if !ok {
				guide, sourceDigest, restoredReceipt, err = o.restoreTeachingReportGuide(ctx, job, q, solved)
				if err != nil {
					return fmt.Errorf("restore report teaching for problem %s: %w", q.ProblemID, err)
				}
			}
		}
		if guide.ReportContent == nil {
			return fmt.Errorf("%w: problem %s", ErrTeachingReportContentRequired, q.ProblemID)
		}
		if sourceDigest == "" && solved.Generation != nil {
			sourceDigest = solved.Generation.SourceSolutionDigest
		}
		if sourceDigest == "" {
			// 这是已冻结解法的内容身份，不提升原来的数值验证等级。
			sourceDigest = teachingReportResourceDigest([]byte(solved.Solution))
		}
		guide, err = o.deps.attachReportTeachingDiagram(ctx, job.Record.AgentName, q, solved.Solution, sourceDigest, guide)
		if err != nil {
			return err
		}
		source := TeachingReportContentSource{Source: k12.TeachingReportProblemSource{ProblemID: q.ProblemID, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest, SolutionDigest: sourceDigest}, Solution: solved.Solution, Guide: &guide}
		receipt := assessment.SolveInvocationID
		if restoredReceipt != "" {
			receipt = restoredReceipt
		}
		if assessment.ParentGuideInvocationID != "" {
			receipt = assessment.ParentGuideInvocationID
		}
		if receipt == "" && assessment.AnswerSource != nil {
			receipt = firstNonEmpty(assessment.AnswerSource.InvocationID, assessment.AnswerSource.AdoptionID)
		}
		if solved.Generation != nil && solved.Generation.AdaptationAuditID != "" {
			receipt = solved.Generation.AdaptationAuditID
		}
		content, sections, err := bindSingleProblemTeachingDraft(source, guide, receipt)
		if err != nil {
			return err
		}
		if len(sections) > 0 && len(guide.TeachingDiagrams) > 0 {
			sections[0].Diagrams = append(sections[0].Diagrams, guide.TeachingDiagrams...)
		}
		input.Content = append(input.Content, content...)
		input.DeepSections = append(input.DeepSections, sections...)
	}
	source, _, err := o.deps.gradingFinalArtifactImageSource(ctx, *artifact)
	if err != nil {
		return err
	}
	media := source
	id := run.req.SourcePageAssetID
	if artifact.HasAnnotatedAsset() {
		if run.result == nil || run.result.AnnotatedImage == nil {
			return fmt.Errorf("report annotated image is unavailable")
		}
		media = DeliveryAttachment{Data: run.result.AnnotatedImage.Data, MIME: artifact.AnnotatedMIME}
		id = artifact.AnnotatedAssetID
	}
	if id == "" {
		id = "grading-input:" + job.Record.RecordID
	}
	geometry, _, err := image.DecodeConfig(bytes.NewReader(media.Data))
	if err != nil {
		return fmt.Errorf("report source image: %w", err)
	}
	input.SourcePages = []k12.TeachingReportSourcePage{{AssetID: id, AssetDigest: teachingReportResourceDigest(media.Data), SourceDigest: teachingReportResourceDigest(source.Data), MIME: media.MIME, Width: geometry.Width, Height: geometry.Height}}
	if run.result != nil {
		if warning := photoAnnotationCoverageWarning(*run.result); warning != "" {
			var ids []string
			var origins []k12.TeachingReportContentOrigin
			for _, item := range input.Content {
				if len(item.AnswerAnalysis.Answers) > 0 {
					ids = append(ids, item.ProblemID)
					origins = append(origins, item.AnswerAnalysis.Answers[0].Origin)
				}
			}
			if len(ids) > 0 {
				input.DeepSections = append(input.DeepSections, k12.TeachingReportDeepSection{ID: "report:annotation", Title: "批注说明", ProblemIDs: ids, Blocks: []k12.TeachingReportExplanationBlock{{ID: "report:annotation:note", Text: warning, Purpose: k12.TeachingReportExplanationSupplement, Origins: origins}}})
			}
		}
	}
	report, err := BuildGradingTeachingReport(input)
	if err != nil {
		return err
	}
	canonical, err := TeachingReportMarkdown(report)
	if err != nil {
		return err
	}
	snapshot := TeachingReportFrozenSnapshot{Report: report, Bundle: run.req.TeachingReport.Bundle}
	if _, err := snapshot.Bundle.Resolve(); err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	artifact.ReportSnapshotJSON = string(raw)
	artifact.TeachingReport = &report
	artifact.CanonicalMarkdown = canonical
	return nil
}

func (d Deps) gradingTeachingReportPrintRequest(ctx context.Context, artifact k12.GradingFinalArtifact, title string) (PreparePrintableArtifactRequest, error) {
	var snapshot TeachingReportFrozenSnapshot
	if err := json.Unmarshal([]byte(artifact.ReportSnapshotJSON), &snapshot); err != nil {
		return PreparePrintableArtifactRequest{}, err
	}
	if snapshot.Report.Source.SourceID != artifact.JobID || snapshot.Report.Source.Kind != k12.TeachingReportSourceGrading {
		return PreparePrintableArtifactRequest{}, k12.ErrTeachingReportInvalid
	}
	canonical, err := TeachingReportMarkdown(snapshot.Report)
	if err != nil || canonical != artifact.CanonicalMarkdown {
		return PreparePrintableArtifactRequest{}, fmt.Errorf("frozen report canonical content differs from its final artifact")
	}
	assets := make(map[string]TeachingReportAsset)
	for _, page := range snapshot.Report.SourcePages {
		if page.AssetID == artifact.AnnotatedAssetID && artifact.HasAnnotatedAsset() {
			media, err := d.Records.OpenGradingFinalAnnotatedAsset(ctx, artifact.AgentName, artifact.ArtifactID)
			if err != nil {
				return PreparePrintableArtifactRequest{}, err
			}
			assets[page.AssetID] = TeachingReportAsset{Data: media.Data, MIME: media.MIME}
		} else {
			media, _, err := d.gradingFinalArtifactImageSource(ctx, artifact)
			if err != nil {
				return PreparePrintableArtifactRequest{}, err
			}
			assets[page.AssetID] = TeachingReportAsset{Data: media.Data, MIME: media.MIME}
		}
	}
	for _, section := range snapshot.Report.DeepSections {
		for _, diagram := range section.Diagrams {
			owner, err := d.Records.TeachingDiagramOwnerForAgent(ctx, artifact.AgentName, diagram.AssetID)
			if err != nil {
				return PreparePrintableArtifactRequest{}, err
			}
			data, err := d.Records.GetTeachingDiagramAsset(ctx, owner, artifact.AgentName, diagram.AssetID)
			if err != nil {
				return PreparePrintableArtifactRequest{}, err
			}
			assets[diagram.AssetID] = TeachingReportAsset{Data: data, MIME: diagram.MIME}
		}
	}
	return prepareTeachingReportPrintRequest(artifact.AgentName, "final_artifact:"+artifact.ArtifactID+":"+artifact.ArtifactDigest, title, snapshot, assets)
}
