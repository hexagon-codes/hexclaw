package k12

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGradingModelSnapshotKeepsMissingQuantityPolicyLegacy(t *testing.T) {
	var legacy GradingModelSnapshot
	if err := json.Unmarshal([]byte(`{"provider":"p","model":"m"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	normalized := NormalizeGradingModelSnapshot(legacy)
	raw, err := json.Marshal(normalized)
	if err != nil || normalized.QuantityAnswerPolicy != "" || strings.Contains(string(raw), "quantity_answer_policy") {
		t.Fatalf("old snapshot acquired a current policy: %s err=%v", raw, err)
	}
}
