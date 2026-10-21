package skill

// ToolExecutionDetail 描述同一次执行的实际结果，不将上游 HTTP 拒绝等同于本应用审批。
type ToolExecutionDetail struct {
	Phase             string `json:"phase,omitempty"`
	Outcome           string `json:"outcome"`
	Reason            string `json:"reason,omitempty"`
	Action            string `json:"action,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	ResponseReceived  bool   `json:"response_received"`
	TargetHost        string `json:"target_host,omitempty"`
	ApprovalRequestID string `json:"approval_request_id,omitempty"`
	ApprovalStatus    string `json:"approval_status,omitempty"`
	ApprovalDeadline  string `json:"approval_deadline,omitempty"`
}

// ExecutionError 保留底层错误链及已知结果；未知副作用不能据此自动重试。
type ExecutionError struct {
	Detail ToolExecutionDetail
	Err    error
}

func (e *ExecutionError) Error() string { return e.Err.Error() }
func (e *ExecutionError) Unwrap() error { return e.Err }
