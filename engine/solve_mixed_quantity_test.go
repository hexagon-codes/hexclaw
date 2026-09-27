package engine

import "testing"

// 带分数的程序输出必须保持一个数值，不能丢单位或拆成答案集合。
func TestSolveMixedQuantityExecution(t *testing.T) {
	stdout := "step1: per_bottle=5/6, bottle_count=4\nstep2: 5/6 * 4 = 10/3\nstep3: 10/3 = 3 1/3\nCOMPUTED: 3 1/3 千克\n"
	for _, tc := range []struct {
		name, candidate string
		verdict         verifyVerdict
		grounded        bool
	}{
		{"mixed", "3 1/3 千克", verdictAgree, true},
		{"improper", "10/3 千克", verdictAgree, true},
		{"Chinese separator", "3又1/3 千克", verdictAgree, true},
		{"wrong value", "31/3 千克", verdictDisagree, true},
		{"wrong unit", "10/3 克", verdictAgree, false},
		{"missing unit", "10/3", verdictAgree, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &solveExec{verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 3 1/3 千克", verifierStdout: stdout}
			v, computed, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), "一瓶果汁重5/6千克，4瓶共重多少？", "每瓶重量乘瓶数。答案："+tc.candidate, tc.candidate, "")
			if v != tc.verdict || computed != "3 1/3 千克" || grounded != tc.grounded || len(exec.specs) != 1 {
				t.Fatalf("verdict=%v computed=%q grounded=%v calls=%d", v, computed, grounded, len(exec.specs))
			}
		})
	}
	for _, answer := range []string{"3 1/3 千克", "答案：3又1/3 千克", "5/6×4=3 1/3 千克"} {
		quantity, ok := parseAnswerQuantity(answer)
		if !ok || quantity.value != "10/3" || quantity.unit != "千克" {
			t.Fatalf("quantity %q = %+v,%v", answer, quantity, ok)
		}
	}
	if !answersEqual("3 1/3", "10/3") || answersEqual("3 1/3", "3;1/3") || answersEqual("3 1/3 千克", "31/3 千克") {
		t.Fatal("mixed scalar was split or concatenated")
	}
	for _, bad := range []string{"3 1/0 千克", "3 4/3 千克"} {
		if _, ok := parseAnswerQuantity(bad); ok {
			t.Fatalf("invalid mixed quantity accepted: %s", bad)
		}
	}
	for _, variant := range []string{"missing", "failed", "truncated"} {
		r := &CodeExecutionReceipt{InputDigest: executionInputDigest("same task"), RunID: "run", Status: "success", Stdout: stdout, StdoutBytes: int64(len(stdout))}
		switch variant {
		case "missing":
			r = nil
		case "failed":
			r.ExitCode = 1
		case "truncated":
			r.Truncated = true
		}
		if _, ok := r.computed("same task"); ok {
			t.Fatalf("%s receipt became execution evidence", variant)
		}
	}
	for _, candidate := range []string{"10/3 千克", "3 1/3 千克", "10/3 千克（也就是 3 1/3 千克）"} {
		t.Run("equivalent forms "+candidate, func(t *testing.T) {
			computed := "10/3 千克（也就是 3 1/3 千克）"
			exec := &solveExec{verifierOut: "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: " + computed, verifierStdout: "COMPUTED: " + computed + "\n"}
			v, got, grounded := NewSolveSkill(exec.fn, nil).verifySolution(t.Context(), "一瓶果汁重5/6千克，4瓶共重多少？", "每瓶重量乘瓶数。", candidate, "")
			if v != verdictAgree || got != computed || !grounded || len(exec.specs) != 1 {
				t.Fatalf("equivalent quantity lost execution evidence: %v %q %v calls=%d", v, got, grounded, len(exec.specs))
			}
		})
	}
	for _, bad := range []string{"10/3 千克（也就是 3 2/3 千克）", "10/3 千克（也就是 3 1/3 克）", "10/3 千克（也就是 3 1/3）"} {
		r := &CodeExecutionReceipt{InputDigest: executionInputDigest("same task"), RunID: "run", Status: "success", Stdout: bad, StdoutBytes: int64(len(bad))}
		if _, ok := r.computed("same task"); ok {
			t.Fatalf("contradictory representation became execution evidence: %s", bad)
		}
	}
}
