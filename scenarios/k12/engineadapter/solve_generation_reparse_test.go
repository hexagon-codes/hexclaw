package engineadapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestStoredSolveGenerationReparseKeepsActualRawAndTrustedExecution(t *testing.T) {
	for i := 1; i <= 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "engine", "testdata", fmt.Sprintf("solve-generation-observation-%d.json", i)))
			if err != nil {
				t.Fatal(err)
			}
			var before engine.SubAgentResult
			if err := json.Unmarshal(raw, &before); err != nil {
				t.Fatal(err)
			}
			parsed, err := reparseSolveGenerationPayload(string(raw), usecase.SolveOutputWithTeachingReportV2)
			if err != nil {
				t.Fatal(err)
			}
			var after engine.SubAgentResult
			if err := json.Unmarshal([]byte(parsed), &after); err != nil {
				t.Fatal(err)
			}
			if after.Generation == nil || after.Generation.RawResponse != before.Output || !reflect.DeepEqual(after.ExecutionReceipt, before.ExecutionReceipt) || !reflect.DeepEqual(after.ExecutionReceipts, before.ExecutionReceipts) || after.SessionID != before.SessionID {
				t.Fatal("stored response parsing changed its original evidence")
			}
			if _, err := reparseSolveGenerationPayload(string(raw), usecase.SolveOutputWithParentGuideV1); err == nil {
				t.Fatal("stored generation was rebound to another teaching version")
			}
			if _, err := reparseSolveGenerationPayload(parsed, usecase.SolveOutputWithTeachingReportV2); err == nil {
				t.Fatal("a previously parsed result was treated as a raw protocol failure")
			}
		})
	}
}
