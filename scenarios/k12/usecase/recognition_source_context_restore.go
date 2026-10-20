package usecase

import (
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 类型化题目不复制识题扩展；恢复只取同题同作答的冻结识题回执，重算完整输入摘要。
func (o *GradingOrchestrator) restoreRecognitionSourceContexts(job GradingJobView, questions []RecognizedQuestion) {
	source, ok := o.readRecognitionReceipt(job.Record.RecordID)
	if !ok || source.AgentName != job.Record.AgentName {
		return
	}
	for i := range questions {
		q := &questions[i]
		for _, prior := range source.Questions {
			if prior.ImageSourceContext == nil || prior.ProblemID != q.ProblemID || prior.AttemptID != q.AttemptID ||
				prior.RawTranscription != q.RawTranscription || prior.AnswerRawTranscription != q.AnswerRawTranscription ||
				prior.CanonicalMarkdown != q.CanonicalMarkdown || prior.AnswerCanonicalMarkdown != q.AnswerCanonicalMarkdown ||
				prior.ConfirmedVersion != q.ConfirmedVersion || prior.InputDigest == "" || prior.InputDigest != q.InputDigest {
				continue
			}
			raw, _ := json.Marshal(prior.ImageSourceContext)
			var context k12.RecognitionImageSourceContextV1
			if json.Unmarshal(raw, &context) != nil {
				continue
			}
			q.ImageSourceContext = &context
		}
	}
}
