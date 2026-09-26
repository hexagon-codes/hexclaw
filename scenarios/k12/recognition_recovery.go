package k12

// RecognitionRecoveryAuthorization 关联历史未知回执与唯一新尝试；状态从新回执读取。
type RecognitionRecoveryAuthorization struct {
	SourceTimeoutMS         int64  `json:"source_timeout_ms,omitempty"`
	TimeoutOverrideMS       int64  `json:"timeout_override_ms,omitempty"`
	AuthorizationID         string `json:"authorization_id"`
	AgentName               string `json:"agent"`
	DispatchID              string `json:"dispatch_id"`
	JobID                   string `json:"job_id"`
	SourceParentID          string `json:"source_parent_id"`
	SourcePhysicalID        string `json:"source_physical_invocation_id"`
	NewParentID             string `json:"new_parent_invocation_id"`
	NewPhysicalID           string `json:"new_physical_invocation_id"`
	NewRequestDigest        string `json:"new_request_digest"`
	PageDigest              string `json:"page_digest"`
	SourcePlanDigest        string `json:"source_plan_digest"`
	SourceRequestDigest     string `json:"source_request_digest"`
	CandidateExactSetDigest string `json:"candidate_exact_set_digest"`
	IdempotencyKey          string `json:"idempotency_key"`
	RequestDigest           string `json:"request_digest"`
	CreatedAt               int64  `json:"created_at"`
}
