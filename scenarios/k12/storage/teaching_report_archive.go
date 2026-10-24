package k12storage

import (
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// migrateTeachingReportSnapshotReferences 仅翻译既有归档映射中的引用，不改模板、题目或答案。
func migrateTeachingReportSnapshotReferences(raw string, assets, receipts map[string]string) (string, k12.TeachingReport, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return "", k12.TeachingReport{}, err
	}
	var typed k12.TeachingReport
	if err := json.Unmarshal(envelope["report"], &typed); err != nil {
		return "", typed, err
	}
	mapRef := func(value string, mapping map[string]string) string {
		if mapped := mapping[value]; mapped != "" {
			return mapped
		}
		return value
	}
	for i := range typed.SourcePages {
		typed.SourcePages[i].AssetID = mapRef(typed.SourcePages[i].AssetID, assets)
	}
	for i := range typed.Items {
		typed.Items[i].AnswerAnalysis.Evidence.ReceiptRef = mapRef(typed.Items[i].AnswerAnalysis.Evidence.ReceiptRef, receipts)
	}
	for i := range typed.DeepSections {
		for j := range typed.DeepSections[i].Diagrams {
			typed.DeepSections[i].Diagrams[j].AssetID = mapRef(typed.DeepSections[i].Diagrams[j].AssetID, assets)
		}
		for j := range typed.DeepSections[i].MethodNotes {
			typed.DeepSections[i].MethodNotes[j].EvidenceRef = mapRef(typed.DeepSections[i].MethodNotes[j].EvidenceRef, receipts)
		}
	}
	typed, err := k12.NewTeachingReport(typed)
	if err != nil {
		return "", typed, err
	}
	reportRaw, err := json.Marshal(typed)
	if err != nil {
		return "", typed, err
	}
	envelope["report"] = reportRaw
	encoded, err := json.Marshal(envelope)
	return string(encoded), typed, err
}
