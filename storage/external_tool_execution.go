package storage

import (
	"context"
	"encoding/json"
	"errors"
)

const (
	ExternalExecutionPrepared   = "prepared"
	ExternalExecutionDispatched = "dispatched"
	ExternalExecutionObserved   = "observed"
	ExternalExecutionUnknown    = "unknown"
)

var ErrExternalExecutionConflict = errors.New("external execution conflict")

// ExternalToolExecution 保存一次实际派发的身份、阶段和结果元数据；秘密和文件字节不进入本记录。
type ExternalToolExecution struct {
	ID                   string          `json:"execution_id"`
	OwnerID              string          `json:"owner_id"`
	SessionID            string          `json:"session_id,omitempty"`
	RequestID            string          `json:"request_id,omitempty"`
	RunID                string          `json:"run_id,omitempty"`
	ToolCallID           string          `json:"tool_call_id,omitempty"`
	AgentName            string          `json:"agent_name,omitempty"`
	DispatchTaskRef      string          `json:"dispatch_task_ref,omitempty"`
	Kind                 string          `json:"kind"`
	LogicalCallKey       string          `json:"logical_call_key"`
	Attempt              uint32          `json:"attempt"`
	ParentExecutionID    string          `json:"parent_execution_id,omitempty"`
	ModelExchangeID      string          `json:"model_exchange_id,omitempty"`
	ModelExchangeOrdinal uint64          `json:"model_exchange_ordinal,omitempty"`
	UpstreamCallID       string          `json:"upstream_call_id,omitempty"`
	ActionIndex          int             `json:"action_index"`
	ToolName             string          `json:"tool_name,omitempty"`
	ServerName           string          `json:"server_name,omitempty"`
	TargetID             string          `json:"target_id,omitempty"`
	InputDigest          string          `json:"input_digest"`
	BrowserGeneration    uint64          `json:"browser_generation,omitempty"`
	PolicyProfile        string          `json:"policy_profile,omitempty"`
	PolicyRevision       uint64          `json:"policy_revision"`
	Phase                string          `json:"phase"`
	Outcome              string          `json:"outcome,omitempty"`
	Reason               string          `json:"reason,omitempty"`
	ResultManifestJSON   json.RawMessage `json:"result_manifest"`
	AcceptedResponse     bool            `json:"accepted_response"`
	Revision             uint64          `json:"revision"`
	CreatedAt            int64           `json:"created_at"`
	UpdatedAt            int64           `json:"updated_at"`
}

// ExternalToolExecutionUpdate 只修改对应尝试的执行事实；nil清单保留已存文件引用。
type ExternalToolExecutionUpdate struct {
	Phase              string
	Outcome            string
	Reason             string
	UpstreamCallID     string
	ResultManifestJSON json.RawMessage
	AcceptedResponse   bool
}

type ExternalToolExecutionFilter struct {
	OwnerID           string
	SessionID         string
	RequestID         string
	DispatchTaskRef   string
	ParentExecutionID string
	ModelExchangeID   string
	Kind              string
	Phase             string
	// ArtifactID 非空时精确匹配已登记产物身份；空值保留原执行查询。
	ArtifactID string
	Intent     *ExternalToolExecutionIntentFilter
	Limit      int
	Offset     int
}

// ExternalToolExecutionIntentFilter 精确匹配同任务的动作，空值也是查询条件。
// 不包含模型生成的工具调用ID，避免未决动作仅换ID就再次派发。
type ExternalToolExecutionIntentFilter struct {
	RequestID       string
	DispatchTaskRef string
	ToolName        string
	ServerName      string
	TargetID        string
	InputDigest     string
}

// ExternalToolExecutionRepository 是可选存储能力；重试是否允许由实际调用合同决定。
// Prepare 对同一逻辑键与attempt幂等读回，参数摘要变化或不连续attempt返回冲突。
// Update 使用Revision比较交换；派发者须成功提交dispatched后才能执行外部动作。
// 非终态允许在同一phase补写结果清单，保留原执行事实；observed记录不可覆盖。
type ExternalToolExecutionRepository interface {
	Prepare(context.Context, ExternalToolExecution) (ExternalToolExecution, error)
	Get(context.Context, string, string) (ExternalToolExecution, error)
	Latest(context.Context, string, string, string) (ExternalToolExecution, error)
	List(context.Context, ExternalToolExecutionFilter) ([]ExternalToolExecution, error)
	Update(context.Context, string, string, uint64, ExternalToolExecutionUpdate) (ExternalToolExecution, error)
	FindArtifact(context.Context, string, string) (*ExternalToolExecution, error)
	HasArtifact(context.Context, string) (bool, error)
}
