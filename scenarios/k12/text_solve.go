package k12

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// TextSolveAdoptionTaskID 将调用方run标识限制在实际辅导实例内，避免多孩子同名run串用采用回执。
func TextSolveAdoptionTaskID(agent, run string) string {
	raw, _ := json.Marshal([]string{agent, run})
	sum := sha256.Sum256(raw)
	return "text-solve:" + hex.EncodeToString(sum[:])
}

// TextSolveOrigin 由认证入口冻结；来源消息缺席时保持空，不由模型补写。
type TextSolveOrigin struct {
	OwnerID         string `json:"owner_id"`
	RunID           string `json:"solve_run_id"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	InputRevision   int    `json:"input_revision"`
}

// TextSolveRun 是文字求解输入及路由的不可变执行身份。
type TextSolveRun struct {
	TextSolveOrigin
	AgentName     string               `json:"agent_name"`
	Facts         ProblemAssetFacts    `json:"facts"`
	FactsDigest   string               `json:"facts_digest"`
	InputDigest   string               `json:"input_digest"`
	RequestJSON   string               `json:"request_json"`
	RouteSnapshot GradingModelSnapshot `json:"route_snapshot"`
	CreatedAt     int64                `json:"created_at"`
}

// TextSolveInvocation 保存生成、验证步骤及本地动作；Provider步骤可包含工具循环的多次请求。
// execution_kind 区分执行来源，逐HTTP请求或计费次数须读取实际请求/成本事实，不能按行数代算。
type TextSolveInvocation struct {
	InvocationID string `json:"invocation_id"`
	TextSolveOrigin
	AgentName         string                `json:"agent_name"`
	FactsDigest       string                `json:"facts_digest"`
	InputDigest       string                `json:"input_digest"`
	Operation         GradingItemOperation  `json:"operation"`
	ExecutionKind     GradingExecutionKind  `json:"execution_kind"`
	OperationAttempt  int                   `json:"operation_attempt"`
	RequestDigest     string                `json:"request_digest"`
	RouteSnapshot     GradingModelSnapshot  `json:"route_snapshot"`
	Status            ModelInvocationStatus `json:"status"`
	ResultDigest      string                `json:"result_digest,omitempty"`
	ResultJSON        string                `json:"result_json,omitempty"`
	ExternalRequestID string                `json:"external_request_id,omitempty"`
	FailureClass      string                `json:"failure_class,omitempty"`
	FailureCode       string                `json:"failure_code,omitempty"`
	CreatedAt         int64                 `json:"created_at"`
	UpdatedAt         int64                 `json:"updated_at"`
}

// TextSolveFinalization 保存原最终答案与采用/执行来源，发布失败不使求解失效。
type TextSolveFinalization struct {
	SourceKind     string `json:"source_kind"`
	FinalizationID string `json:"finalization_id"`
	TextSolveOrigin
	AgentName    string                   `json:"agent_name"`
	FactsDigest  string                   `json:"facts_digest"`
	InputDigest  string                   `json:"input_digest"`
	ResultJSON   string                   `json:"result_json"`
	ResultDigest string                   `json:"result_digest"`
	InvocationID string                   `json:"invocation_id"`
	AnswerSource *ProblemAnswerSource     `json:"answer_source,omitempty"`
	Publication  *ProblemAssetPublication `json:"publication,omitempty"`
	CreatedAt    int64                    `json:"created_at"`
}

const (
	ProblemAssetSourceGrading   = "grading_item"
	ProblemAssetSourceTextSolve = "text_solve"
)
