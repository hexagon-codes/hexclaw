package engineadapter

import "testing"

func TestSolveAdapterQuantityAnswerCompletenessUsesEngineWithoutExecution(t *testing.T) {
	adapter := NewSolveAdapter(nil)
	const question = "涂色面积是（ ）cm²。"
	if required, complete := adapter.QuantityAnswerCompleteness(question, "25.12平方厘米"); !required || !complete {
		t.Fatal("the read-only capability must accept a complete numerical answer")
	}
	if required, complete := adapter.QuantityAnswerCompleteness(question, "无法确定面积"); !required || complete {
		t.Fatal("the read-only capability must reject missing numerical content")
	}
}
