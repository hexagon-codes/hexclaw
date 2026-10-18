package k12

import "context"

// RecognitionExecutionPolicyCompactDAGV1 为新首读任务冻结紧凑输出与依赖调度策略。
const RecognitionExecutionPolicyCompactDAGV1 = "compact_dag_v1"

// RecognitionExecutionPolicyCompactDAGFinalAnswerV1 为新任务冻结独立最终答案取证合同。
const RecognitionExecutionPolicyCompactDAGFinalAnswerV1 = "compact_dag_final_answer_v1"

// RecognitionExecutionPolicyCompactDAGFinalAnswerV2 将首读过程置信度与独立末值取证分开冻结。
const RecognitionExecutionPolicyCompactDAGFinalAnswerV2 = "compact_dag_final_answer_v2"

// RecognitionFinalAnswerExecutionPolicy 标识包含独立最终答案证据的冻结合同。
func RecognitionFinalAnswerExecutionPolicy(policy string) bool {
	return policy == RecognitionExecutionPolicyCompactDAGFinalAnswerV1 || policy == RecognitionExecutionPolicyCompactDAGFinalAnswerV2
}

// RecognitionCompactDAGExecutionPolicy 标识复用紧凑首读和双槽调度的冻结合同。
func RecognitionCompactDAGExecutionPolicy(policy string) bool {
	return policy == RecognitionExecutionPolicyCompactDAGV1 || RecognitionFinalAnswerExecutionPolicy(policy)
}

type recognitionExecutionPolicyContextKey struct{}

// WithRecognitionExecutionPolicy 传递已冻结的执行策略；空值保留历史执行合同。
func WithRecognitionExecutionPolicy(ctx context.Context, policy string) context.Context {
	return context.WithValue(ctx, recognitionExecutionPolicyContextKey{}, policy)
}

// RecognitionExecutionPolicyFromContext 返回本次已冻结的执行策略。
func RecognitionExecutionPolicyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	policy, _ := ctx.Value(recognitionExecutionPolicyContextKey{}).(string)
	return policy
}
