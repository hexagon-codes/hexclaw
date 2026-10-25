package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/storage"
)

func TestHistoricalReportReferencesPreserveOnlyTrustedIdentity(t *testing.T) {
	metadata := map[string]any{
		"print_reply_digest":      "committed",
		"print_artifacts":         `[{"artifact_id":"part-frozen","name":"report.pdf","url":"/private/file","byte_digest":"digest"}]`,
		"media_completion_digest": "media-committed", "media_operations": `["media-existing"]`,
		"text_solve_refs": `[{"solve_run_id":"failed-run","finalization_id":"failed-final","pdf_status":"failed"}]`,
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	record := &storage.MessageRecord{ID: "original-assistant", Role: "assistant", Content: "完整报告正文", Metadata: string(raw)}
	got := historicalAssistantMediaReferences(record)
	for _, required := range []string{"完整报告正文", "original-assistant", "part-frozen", "resend_report", "media-existing", "prepare_report"} {
		if !strings.Contains(got, required) {
			t.Fatalf("trusted history lost %q: %s", required, got)
		}
	}
	if strings.Contains(got, "/private/file") || strings.Contains(got, "byte_digest") {
		t.Fatal("model history must carry identities, not file locations or payload")
	}
	delete(metadata, "print_reply_digest")
	raw, _ = json.Marshal(metadata)
	record.Metadata = string(raw)
	if got := historicalAssistantMediaReferences(record); strings.Contains(got, "part-frozen") {
		t.Fatal("uncommitted attachment reference was exposed")
	}
}

func TestHistoricalFailedPDFKeepsRecoveryIdentity(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"text_solve_refs": `[{"solve_run_id":"run","finalization_id":"final","pdf_status":"failed"}]`})
	record := &storage.MessageRecord{ID: "failed-report-message", Role: "assistant", Content: "完整报告正文", Metadata: string(raw)}
	got := historicalAssistantMediaReferences(record)
	if !strings.Contains(got, "完整报告正文") || !strings.Contains(got, "failed-report-message") || !strings.Contains(got, "prepare_report") || strings.Contains(got, "resend_report") {
		t.Fatalf("failed PDF history must preserve the body and only preparation recovery: %s", got)
	}
}
