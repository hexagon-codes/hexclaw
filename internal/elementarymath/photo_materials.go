package elementarymath

import (
	"encoding/json"
	"strings"
)

// PhotoMaterialSeparator 是现有求解请求的只读来源分隔符，参与持久调用摘要。
const PhotoMaterialSeparator = "\n\nRead-only source material (do not answer as another question):\n"
const photoCurrentQuestion = "\n\nCurrent question and answer requirements: "
const photoAnswerRequirements = ". The read-only materials supply the shared conditions and any explicitly printed common main problem. When this item specifies a method, relationship or multiplier for that common problem, complete both its own intermediate answers and the common requested final quantity, including the capacity calculation line; do not stop at a ratio conversion. Do not answer other numbered items. Answer every requested blank, relationship, multiplier, intermediate quantity and final expression. Put ALL required reference answers in the Answer section, with the conditions for alternative valid fillings; a final scalar alone is insufficient when the current question asks for other answers. Preserve the printed equation template's sides, operations and fixed coefficients when filling blanks. A rearranged equation can explain a relationship but cannot replace the filling of a different printed template. These completeness requirements apply to the reference solution; a student may give any one valid alternative rather than enumerate all alternatives."

// WithPhotoAnswerRequirements 附上当前题及完整作答要求，保持原请求的序列化身份。
func WithPhotoAnswerRequirements(problem, current string) string {
	quoted, _ := json.Marshal(current)
	return problem + photoCurrentQuestion + string(quoted) + photoAnswerRequirements
}

// PhotoVerificationProblem 只折叠与首段逐字相同的题面复述，避免独立验算重复计空。
// 共享图示和补全片段仍作为事实参与核验，旧回执及调用摘要继续使用原请求。
func PhotoVerificationProblem(problem string) string {
	body, ok := strings.CutSuffix(problem, photoAnswerRequirements)
	if !ok {
		return problem
	}
	before, quoted, ok := strings.Cut(body, photoCurrentQuestion)
	if !ok {
		return problem
	}
	primary, materials, ok := strings.Cut(before, PhotoMaterialSeparator)
	var current string
	if !ok || json.Unmarshal([]byte(quoted), &current) != nil || primary != current {
		return problem
	}
	scoped, ok := BuildMathSubproblem(materials, current, "")
	if !ok {
		return problem
	}
	return scoped
}
