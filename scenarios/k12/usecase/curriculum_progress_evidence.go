package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// curriculumProgressHomeworkEvidence 仅从当前成功批改使用的教材引用生成内容建议。
// 教材引用不证明学校实际进度；人工进度保持，旧复习内容不倒退已采用的 AI 进度。
func (d Deps) curriculumProgressHomeworkEvidence(
	ctx context.Context, ownerID, agentName string,
	catalog k12.CurriculumCatalog, scope k12.TextbookGroundingScope,
	current *k12.CurriculumProgress,
) (unitID string, sourceAt int64, receiptHash string, found bool, err error) {
	if d.Records == nil || ownerID == "" || agentName == "" ||
		k12.ValidateTextbookGroundingScope(scope) != nil ||
		catalog.AgentName != agentName || catalog.Subject != "math" ||
		catalog.TextbookBindingID != scope.TextbookBindingID ||
		catalog.TextbookManifestID != scope.TextbookManifestID ||
		catalog.TextbookEdition != scope.Edition || catalog.Volume != scope.Volume ||
		(current != nil && current.EvidenceSource == "parent_confirmed") {
		return
	}
	candidates, listErr := d.Records.ListCurriculumProgressHomeworkCandidates(ctx, ownerID, agentName)
	if listErr != nil {
		err = listErr
		return
	}
	// 此投影只读取既有 assessment 和成功 invocation，不启动编排器生命周期。
	projection := GradingOrchestrator{deps: d}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		if candidate.CreatedAt <= 0 {
			continue
		}
		job, readErr := d.GetGradingJob(ctx, agentName, candidate.GradingJobID)
		if readErr != nil || job.Record == nil || job.Record.Status != k12.GradingStageCompleted ||
			job.Fields.SubmissionID != candidate.SubmissionID ||
			job.Fields.ConfirmationState != k12.GradingConfirmationConfirmed {
			continue
		}
		questions, readErr := d.loadCurrentConfirmedQuestions(ctx, agentName, candidate.SubmissionID)
		if readErr != nil {
			continue
		}
		receipts, readErr := projection.projectProblemGroundingReceipts(ctx, agentName, candidate.GradingJobID, questions, true)
		if readErr != nil {
			continue
		}
		items, readErr := d.Records.ListGradingAssessmentItems(ctx, agentName, candidate.GradingJobID)
		if readErr != nil {
			continue
		}
		usableProblems := make(map[string]bool)
		for _, question := range questions {
			if question.Subject == "数学" || question.Subject == "math" {
				usableProblems[question.ProblemID] = true
			}
		}
		for _, item := range items {
			if item.Status != k12.GradingAssessmentCorrect && item.Status != k12.GradingAssessmentProcessIssue &&
				item.Status != k12.GradingAssessmentWrong {
				delete(usableProblems, item.ProblemID)
			}
		}
		usableReceipts := make([]ProblemGroundingReceipt, 0, len(receipts))
		for _, receipt := range receipts {
			if usableProblems[receipt.ProblemID] {
				usableReceipts = append(usableReceipts, receipt)
			}
		}
		matchedUnit, usable := curriculumProgressEvidenceUnit(catalog, scope, current, usableReceipts)
		if !usable {
			continue
		}
		encoded, encodeErr := json.Marshal(struct {
			JobID, SubmissionID string
			CreatedAt           int64
			Receipts            []ProblemGroundingReceipt
		}{candidate.GradingJobID, candidate.SubmissionID, candidate.CreatedAt, usableReceipts})
		if encodeErr != nil {
			err = encodeErr
			return
		}
		digest := sha256.Sum256(encoded)
		return matchedUnit, candidate.CreatedAt, hex.EncodeToString(digest[:]), true, nil
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return
}

func curriculumProgressEvidenceUnit(
	catalog k12.CurriculumCatalog, scope k12.TextbookGroundingScope,
	current *k12.CurriculumProgress, receipts []ProblemGroundingReceipt,
) (string, bool) {
	if len(receipts) == 0 {
		return "", false
	}
	unitIndex, currentIndex := -1, -1
	if current != nil && current.EvidenceSource == "ai_estimated" &&
		current.TextbookBindingID == scope.TextbookBindingID &&
		current.TextbookManifestID == scope.TextbookManifestID {
		for index, unit := range catalog.Units {
			if unit.UnitID == current.UnitID {
				currentIndex = index
			}
		}
		if currentIndex < 0 {
			return "", false
		}
	}
	for _, receipt := range receipts {
		if receipt.TextbookBindingID != scope.TextbookBindingID ||
			receipt.TextbookManifestID != scope.TextbookManifestID ||
			receipt.DocumentID != scope.DocumentID || receipt.DocumentGeneration != scope.DocumentGeneration ||
			receipt.SourceDigest != scope.SourceDigest {
			return "", false
		}
		pageMapped := false
		for _, page := range scope.PageRefs {
			if page.LogicalPage == receipt.LogicalPage && page.PDFPage == receipt.PDFPage {
				for _, segment := range page.SegmentRefs {
					pageMapped = pageMapped || segment == receipt.ChunkID
				}
			}
		}
		if !pageMapped {
			return "", false
		}
		matched := -1
		for index, unit := range catalog.Units {
			if unit.UnitID != "" && unit.PageFrom > 0 && unit.PageTo >= unit.PageFrom &&
				receipt.LogicalPage >= unit.PageFrom && receipt.LogicalPage <= unit.PageTo {
				if matched >= 0 {
					return "", false
				}
				matched = index
			}
		}
		if matched < 0 || (unitIndex >= 0 && unitIndex != matched) {
			return "", false
		}
		unitIndex = matched
	}
	if unitIndex < currentIndex {
		return "", false
	}
	return catalog.Units[unitIndex].UnitID, true
}
