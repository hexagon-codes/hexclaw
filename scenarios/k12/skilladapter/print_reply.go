package skilladapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

// PrintableArtifactResult 把已冻结 PDF 的可信引用交给原工具回合，不自行发送或结束回复。
func PrintableArtifactResult(ctx context.Context, deps usecase.Deps, agent, artifactID, content string) (*skill.Result, error) {
	ref, _, err := deps.ResolvePrintReplyArtifact(ctx, agent, artifactID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal([]any{ref})
	if err != nil {
		return nil, err
	}
	return &skill.Result{Content: content, Metadata: map[string]string{"print_artifacts": string(encoded)}}, nil
}

type textSolveReplyReference struct {
	RunID              string                        `json:"solve_run_id"`
	FinalizationID     string                        `json:"finalization_id"`
	FinalizationDigest string                        `json:"finalization_digest"`
	InputDigest        string                        `json:"input_digest"`
	SourceRequestID    string                        `json:"source_request_id,omitempty"`
	AnswerSource       *k12.ProblemAnswerSource      `json:"answer_source,omitempty"`
	GradeReportRef     *k12.TextGradeReportReference `json:"grade_report_ref,omitempty"`
	PDFStatus          string                        `json:"pdf_status,omitempty"`
	PDFError           string                        `json:"pdf_error,omitempty"`
	RetryAction        string                        `json:"retry_action,omitempty"`
}

// prepareReportResult 保留完整正式正文；文件准备失败只影响独立的产物状态。
func (s *GradeSkill) prepareReportResult(ctx context.Context, agent string, final *k12.TextSolveFinalization, content string, metadata map[string]string, gradeRefs ...k12.TextGradeReportReference) (*skill.Result, error) {
	result := &skill.Result{Content: content, Metadata: metadata}
	var prepareErr error
	if final == nil {
		prepareErr = fmt.Errorf("report finalization is unavailable")
	} else {
		var printable usecase.PrintableArtifactView
		var err error
		switch len(gradeRefs) {
		case 0:
			printable, _, err = s.deps.PrepareTextSolveTeachingReportPDF(ctx, final.OwnerID, agent, final.RunID, final.FinalizationID, final.ResultDigest, "")
		case 1:
			printable, _, err = s.deps.PrepareTextSolveTeachingReportPDFWithGrade(ctx, final.OwnerID, agent, final.RunID, final.FinalizationID, final.ResultDigest, "", gradeRefs[0])
		default:
			return nil, fmt.Errorf("report preparation requires one exact grading reference")
		}
		prepareErr = err
		if err == nil {
			var attachment *skill.Result
			attachment, prepareErr = PrintableArtifactResult(ctx, s.deps, agent, printable.Artifact.ArtifactID, content)
			if prepareErr == nil {
				result.Content = printable.Artifact.CanonicalMarkdown
				result.Metadata["print_artifacts"] = attachment.Metadata["print_artifacts"]
			}
		}
	}
	if requestcancel.IsExplicit(ctx) {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	status, failure, retry := "ready", "", ""
	if prepareErr != nil {
		status, failure = "failed", prepareErr.Error()
		if final != nil {
			retry = "prepare_report"
		}
	}
	var refs []textSolveReplyReference
	if json.Unmarshal([]byte(metadata["text_solve_refs"]), &refs) == nil {
		for i := range refs {
			if len(gradeRefs) == 1 {
				gradeRef := gradeRefs[0]
				refs[i].GradeReportRef = &gradeRef
			}
			if refs[i].SourceRequestID == "" {
				refs[i].SourceRequestID = skill.CurrentInvocation(ctx).RequestID
			}
			refs[i].PDFStatus, refs[i].PDFError, refs[i].RetryAction = status, failure, retry
		}
		encoded, err := json.Marshal(refs)
		if err != nil {
			return nil, err
		}
		result.Metadata["text_solve_refs"] = string(encoded)
	}
	result.Data = map[string]any{"report_output": map[string]string{"status": status, "error": failure, "retry_action": retry}}
	return result, nil
}

// savedReportResult 将当前会话的可信旧引用交给新回合，工具本身不创建投递批次。
func (s *GradeSkill) savedReportResult(ctx context.Context, agent, action, messageID string) (*skill.Result, error) {
	if requestcancel.IsExplicit(ctx) {
		return nil, context.Canceled
	}
	inv := skill.CurrentInvocation(ctx)
	if s.deps.Records == nil || inv.AuthenticatedUserID == "" || inv.SessionID == "" || inv.RoutedAgentName != agent {
		return nil, fmt.Errorf("report history requires a trusted conversation")
	}
	if err := s.deps.Records.ValidateTextSolveRequest(ctx, k12.TextSolveOrigin{SourceSessionID: inv.SessionID, SourceMessageID: inv.SourceMessageID}, inv.AuthenticatedUserID, inv.RequestID); err != nil {
		return nil, err
	}
	message, err := s.deps.Records.GetConversationReportMessage(ctx, inv.AuthenticatedUserID, agent, inv.SessionID, messageID, action == "prepare_report")
	if action == "resend_report" && messageID == "" && (err == nil || errors.Is(err, records.ErrNotFound)) {
		after := ""
		if message != nil {
			after = message.ID
		}
		imageReport, readErr := s.deps.ReadConversationImageReport(ctx, inv, after)
		if readErr == nil {
			encoded, encodeErr := json.Marshal([]k12.PrintReplyArtifact{imageReport.Artifact})
			if encodeErr != nil {
				return nil, encodeErr
			}
			return &skill.Result{Content: imageReport.Content, Metadata: map[string]string{"print_artifacts": string(encoded), "print_reply_action": action}, Data: map[string]any{"report_output": map[string]string{"status": "ready", "source_message_id": imageReport.Source.SourceMessageID, "dispatch_id": imageReport.Source.DispatchID}}}, nil
		}
		if !errors.Is(readErr, records.ErrNotFound) {
			return nil, readErr
		}
	}
	if err != nil {
		return nil, err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message.Metadata), &metadata); err != nil {
		return nil, err
	}
	var encodedRefs string
	_ = json.Unmarshal(metadata["print_artifacts"], &encodedRefs)
	var content []string
	seen := make(map[string]bool)
	var actualRefs []k12.PrintReplyArtifact
	if encodedRefs != "" {
		var refs []k12.PrintReplyArtifact
		if err := json.Unmarshal([]byte(encodedRefs), &refs); err != nil || len(refs) == 0 {
			return nil, fmt.Errorf("saved report attachments are unavailable")
		}
		for _, ref := range refs {
			if ref.AgentName != agent {
				return nil, fmt.Errorf("saved report agent mismatch")
			}
			actual, _, err := s.deps.ResolvePrintReplyArtifact(ctx, agent, ref.ArtifactID)
			if err != nil {
				return nil, err
			}
			if actual != ref {
				return nil, fmt.Errorf("saved report frozen identity mismatch")
			}
			if seen[ref.ArtifactID] {
				continue
			}
			seen[ref.ArtifactID] = true
			pdf, err := s.deps.GetPrintableArtifactPDF(ctx, agent, ref.ArtifactID)
			if err != nil {
				return nil, err
			}
			content = append(content, pdf.Artifact.CanonicalMarkdown)
			actualRefs = append(actualRefs, actual)
		}
	}
	result := &skill.Result{Content: message.Content, Metadata: make(map[string]string)}
	if action == "resend_report" {
		if len(actualRefs) == 0 {
			return nil, fmt.Errorf("saved PDF is unavailable; prepare the original report first")
		}
		encoded, err := json.Marshal(actualRefs)
		if err != nil {
			return nil, err
		}
		result.Content = strings.Join(content, "\n\n")
		result.Metadata["print_artifacts"] = string(encoded)
		result.Metadata["print_reply_action"] = action
		result.Data = map[string]any{"report_output": map[string]string{"status": "ready", "source_message_id": message.ID}}
		return result, nil
	}
	encodedRefs = ""
	_ = json.Unmarshal(metadata["text_solve_refs"], &encodedRefs)
	var refs []textSolveReplyReference
	if encodedRefs != "" && json.Unmarshal([]byte(encodedRefs), &refs) != nil {
		return nil, fmt.Errorf("saved report preparation identity is unavailable")
	}
	owner := inv.AuthenticatedUserID
	if s.deps.TextbookOwnerID != "" {
		owner = s.deps.TextbookOwnerID
	}
	// 先复核全部待恢复来源，再逐项准备；一项编译失败不丢弃其他来源的成功文件。
	finals := make(map[int]k12.TextSolveFinalization)
	for i, ref := range refs {
		if ref.PDFStatus != "failed" {
			continue
		}
		final, err := s.deps.Records.GetTextSolveFinalization(ctx, owner, agent, ref.RunID)
		if err != nil {
			return nil, err
		}
		if final.FinalizationID != ref.FinalizationID || final.ResultDigest != ref.FinalizationDigest || final.InputDigest != ref.InputDigest || final.SourceSessionID != inv.SessionID {
			return nil, fmt.Errorf("saved report finalization identity mismatch")
		}
		if err := s.deps.Records.ValidateTextSolveRequest(ctx, final.TextSolveOrigin, inv.AuthenticatedUserID, ref.SourceRequestID); err != nil {
			return nil, err
		}
		finals[i] = final
	}
	if len(finals) == 0 && len(actualRefs) == 0 {
		return nil, fmt.Errorf("saved report preparation identity is unavailable")
	}
	for i, ref := range refs {
		final, pending := finals[i]
		if !pending {
			continue
		}
		encoded, _ := json.Marshal([]textSolveReplyReference{ref})
		var gradeRefs []k12.TextGradeReportReference
		if ref.GradeReportRef != nil {
			gradeRefs = append(gradeRefs, *ref.GradeReportRef)
		}
		prepared, err := s.prepareReportResult(ctx, agent, &final, message.Content, map[string]string{"text_solve_refs": string(encoded)}, gradeRefs...)
		if err != nil {
			return nil, err
		}
		var updated []textSolveReplyReference
		if json.Unmarshal([]byte(prepared.Metadata["text_solve_refs"]), &updated) != nil || len(updated) != 1 {
			return nil, fmt.Errorf("report preparation receipt is unavailable")
		}
		refs[i] = updated[0]
		if prepared.Metadata["print_artifacts"] != "" {
			var added []k12.PrintReplyArtifact
			if err := json.Unmarshal([]byte(prepared.Metadata["print_artifacts"]), &added); err != nil {
				return nil, err
			}
			for _, artifact := range added {
				if !seen[artifact.ArtifactID] {
					seen[artifact.ArtifactID] = true
					actualRefs = append(actualRefs, artifact)
				}
			}
		}
	}
	if len(actualRefs) > 0 {
		encoded, _ := json.Marshal(actualRefs)
		result.Metadata["print_artifacts"] = string(encoded)
	}
	if len(refs) > 0 {
		encoded, _ := json.Marshal(refs)
		result.Metadata["text_solve_refs"] = string(encoded)
		last := refs[len(refs)-1]
		result.Metadata["text_solve_run_id"] = last.RunID
		result.Metadata["text_solve_finalization_id"] = last.FinalizationID
		result.Metadata["text_solve_input_digest"] = last.InputDigest
	}
	var failures []string
	for _, ref := range refs {
		if ref.PDFStatus == "failed" {
			failures = append(failures, ref.PDFError)
		}
	}
	status, retry := "ready", ""
	if len(failures) > 0 {
		status, retry = "failed", "prepare_report"
	}
	result.Data = map[string]any{"report_output": map[string]string{"status": status, "error": strings.Join(failures, "; "), "retry_action": retry, "source_message_id": message.ID}}
	return result, nil
}
