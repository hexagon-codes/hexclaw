package session

import (
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/storage"
)

// 历史只提供已保存的媒体身份用于续接，不重新注入文件字节或上游链接。
func historicalAssistantMediaReferences(record *storage.MessageRecord) string {
	if record.Role != "assistant" {
		return record.Content
	}
	var metadata struct {
		CompletionDigest string `json:"media_completion_digest"`
		Operations       string `json:"media_operations"`
		PrintDigest      string `json:"print_reply_digest"`
		PrintArtifacts   string `json:"print_artifacts"`
		TextSolveRefs    string `json:"text_solve_refs"`
		Assets           []struct {
			ID          string `json:"asset_id"`
			OperationID string `json:"operation_id"`
			Kind        string `json:"kind"`
			Role        string `json:"role"`
		} `json:"media_assets"`
	}
	if json.Unmarshal([]byte(record.Metadata), &metadata) != nil {
		return record.Content
	}
	content := record.Content
	if metadata.PrintDigest != "" {
		var reports []struct {
			ArtifactID string `json:"artifact_id"`
			Name       string `json:"name"`
		}
		if json.Unmarshal([]byte(metadata.PrintArtifacts), &reports) == nil && len(reports) > 0 {
			refs, err := json.Marshal(struct {
				MessageID string `json:"report_message_id"`
				Artifacts any    `json:"artifacts"`
			}{record.ID, reports})
			if err == nil {
				content += "\n\n[Persisted report references; k12_grade action=resend_report reads the existing PDF] " + string(refs)
			}
		}
	}
	if metadata.TextSolveRefs != "" {
		var reports []struct {
			RunID          string `json:"solve_run_id"`
			FinalizationID string `json:"finalization_id"`
			PDFStatus      string `json:"pdf_status"`
		}
		if json.Unmarshal([]byte(metadata.TextSolveRefs), &reports) == nil {
			for _, report := range reports {
				if report.PDFStatus == "failed" && report.RunID != "" && report.FinalizationID != "" {
					ref, _ := json.Marshal(map[string]string{"report_message_id": record.ID, "pdf_status": "failed", "retry_action": "prepare_report"})
					content += "\n\n[Persisted report preparation; the verified body is available, but one or more PDF files are not ready] " + string(ref)
					break
				}
			}
		}
	}
	if metadata.CompletionDigest == "" {
		return content
	}
	var operations []string
	_ = json.Unmarshal([]byte(metadata.Operations), &operations)
	if len(metadata.Assets) == 0 && len(operations) == 0 {
		return content
	}
	references, err := json.Marshal(struct {
		Operations []string `json:"operation_ids,omitempty"`
		Assets     any      `json:"assets,omitempty"`
	}{Operations: operations, Assets: metadata.Assets})
	if err != nil {
		return content
	}
	return content + "\n\n[Persisted media references] " + string(references)
}
