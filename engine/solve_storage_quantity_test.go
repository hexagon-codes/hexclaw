package engine

import "testing"

// 容量单位和关系填空都属于完整答案，不能丢单位或只比较最后一个数。
func TestVerificationStorageQuantityAnswers(t *testing.T) {
	const relation = "（已用空间）=（总容量）×2/5；列式：4.8÷2/5=12（TB）"
	for _, tc := range []struct {
		name, computed, candidate string
		equal, comparable         bool
	}{
		{"storage equation", "12 TB", "4.8÷2×5＝12（TB）", true, true},
		{"storage alias", "12 TB", "4.8÷2×5＝12（太字节）", true, true},
		{"complete named conclusion", "12 TB", "这个硬盘的总容量是12 TB。", true, true},
		{"wrong named conclusion", "12 TB", "这个硬盘的总容量是10 TB。", false, true},
		{"different named target", "12 TB", "剩余空间是12 TB。", false, false},
		{"different value", "12 TB", "4.8÷2×6＝14.4（TB）", false, true},
		{"invalid equation", "12 TB", "4.8÷2×6＝12（TB）", false, false},
		{"different unit", "12 TB", "4.8÷2×5＝12（kg）", false, false},
		{"missing unit", "12 TB", "4.8÷2×5＝12", false, false},
		{"unknown unit", "12 TB", "12 GB", false, false},
		{"complete relationship", "已用空间=总容量×0.4；12 TB", relation, true, true},
		{"relationship prefix", "已用空间=总容量×2/5；12 TB", "数量关系：" + relation, true, true},
		{"reversed relationship", "已用空间=总容量×2/5；12 TB", "总容量=已用空间×2/5；12 TB", false, true},
		{"wrong fraction", "已用空间=总容量×2/5；12 TB", "已用空间=总容量×3/5；12 TB", false, true},
		{"wrong relationship equation", "已用空间=总容量×2/5；12 TB", "已用空间=总容量×2/5；4.8÷2×6=12（TB）", false, false},
		{"missing relationship", "已用空间=总容量×2/5；12 TB", "12 TB", false, false},
		{"missing computed relationship", "12 TB", relation, false, false},
		{"missing calculation", "已用空间=总容量×2/5；12 TB", "已用空间=总容量×2/5", false, false},
		{"extra answer", "已用空间=总容量×2/5；12 TB", relation + "；1 TB", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual("硬盘已用空间占总容量的2/5，已用4.8 TB。这个硬盘的总容量是多少太字节？", tc.computed, tc.candidate)
			if equal != tc.equal || comparable != tc.comparable {
				t.Fatalf("comparison=%t/%t, want %t/%t", equal, comparable, tc.equal, tc.comparable)
			}
		})
	}
}

func TestVerificationQuantityEquationKeepsRepeatedConclusion(t *testing.T) {
	const problem = "已用空间占总容量的2/5，已用4.8 TB。这个硬盘的总容量是多少太字节？"
	for _, tc := range []struct {
		answer string
		valid  bool
	}{
		{"4.8÷2×5＝12（TB），这个硬盘的总容量是 12 TB", true},
		{"4.8÷2×5＝12（TB），这个硬盘的总容量为 12 太字节", true},
		{"4.8÷2×5＝12（TB），已用空间是 12 TB", false},
		{"4.8÷2×5＝12（TB），这个硬盘的总容量是 10 TB", false},
		{"4.8÷2×6＝12（TB），这个硬盘的总容量是 12 TB", false},
		{"4.8÷2×5＝12（TB），这个硬盘的总容量是 12", false},
		{"4.8÷2×5＝12（TB），这个硬盘的总容量是 12 TB；剩余空间是12 TB", false},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			equal, comparable := verificationAnswersEqual(problem, "12.0 TB", tc.answer)
			if equal != tc.valid || comparable != tc.valid {
				t.Fatalf("repeated conclusion comparison=%t/%t, want %t/%t", equal, comparable, tc.valid, tc.valid)
			}
			if tc.valid && !completeVerificationAnswer(verificationAnswerProjection(problem, tc.answer)) {
				t.Fatal("complete audited answer did not reach standalone verification")
			}
		})
	}
}

func TestSolveStorageQuantityExecutionEvidence(t *testing.T) {
	for _, tc := range []struct{ candidate, computed string }{
		{"4.8÷2×5＝12（TB）", "12 TB"},
		{"（已用空间）=（总容量）×2/5；列式：4.8÷2/5=12（TB）", "已用空间=总容量×2/5；12 TB"},
	} {
		exec := &solveExec{
			solverOuts:     []string{"根据已用空间与总容量的关系计算。\n答案：" + tc.candidate},
			verifierOut:    "VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: " + tc.computed,
			verifierStdout: "COMPUTED: " + tc.computed + "\n",
		}
		result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), map[string]any{
			"problem": "硬盘已用空间占总容量的2/5，已用4.8 TB，求总容量并填写数量关系。", "self_consistency": 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Metadata["solve_evidence"] != "numeric_exec" || result.Metadata["solve_verdict"] != "agree" || result.Metadata["solve_verification_run_id"] == "" {
			t.Fatalf("successful independent execution lost numerical evidence: %+v", result.Metadata)
		}
		if len(exec.specs) != 2 {
			t.Fatalf("comparison added model calls: %d", len(exec.specs))
		}
	}
}

// 同一目录必须同时贯通裸数值、带单位列式和真实执行结果的比较。
func TestVerificationElementaryUnitsReachQuantityEquations(t *testing.T) {
	for _, units := range [][2]string{
		{"mm", "毫米"}, {"cm", "厘米"}, {"dm", "分米"}, {"m", "米"}, {"km", "公里"},
		{"mm²", "平方毫米"}, {"cm^2", "平方厘米"}, {"dm2", "平方分米"}, {"m²", "平方米"}, {"km²", "平方千米"}, {"ha", "公顷"},
		{"mm³", "立方毫米"}, {"cm^3", "立方厘米"}, {"dm3", "立方分米"}, {"m³", "立方米"}, {"L", "升"}, {"mL", "毫升"},
		{"g", "克"}, {"kg", "公斤"}, {"t", "吨"}, {"mg", "毫克"},
		{"s", "秒"}, {"min", "分钟"}, {"h", "时"}, {"d", "日"}, {"星期", "周"}, {"月", "月"}, {"年", "年"}, {"世纪", "世纪"},
		{"元", "圆"}, {"角", "角"}, {"分", "分"}, {"万元", "万元"}, {"亿元", "亿元"},
		{"°", "°"}, {"℃", "摄氏度"}, {"kWh", "千瓦时"}, {"kW", "千瓦"},
		{"B", "字节"}, {"KB", "千字节"}, {"MB", "兆字节"}, {"GB", "吉字节"}, {"TB", "太字节"},
		{"km/h", "千米/时"}, {"m/min", "米/分钟"}, {"m/s", "米/秒"}, {"元/kg", "元/千克"}, {"元/个", "元/个"},
		{"份", "份"}, {"人", "人"}, {"个", "个"}, {"只", "只"},
	} {
		t.Run(units[0], func(t *testing.T) {
			computed, candidate := "12 "+units[0], "4×3＝12（"+units[1]+"）"
			if equal, comparable := verificationAnswersEqual("", computed, candidate); !equal || !comparable {
				t.Fatalf("complete quantity comparison failed: computed=%q candidate=%q equal=%t comparable=%t", computed, candidate, equal, comparable)
			}
		})
	}
}
