package usecase

import (
	"encoding/json"
	"os"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 类型化题目不复制识题扩展；恢复只取同题同作答且完整输入摘要相同的冻结来源。
func (o *GradingOrchestrator) restoreRecognitionSourceContexts(job GradingJobView, questions []RecognizedQuestion) {
	var sources []RecognizedQuestion
	if audit, ok := o.readRecognitionAudit(job.Record.RecordID); ok && audit.AgentName == job.Record.AgentName {
		sources = audit.Questions
	} else {
		raw, err := os.ReadFile(o.runPath(job.Record.RecordID, "run.json"))
		var run gradingRunFile
		if err == nil && json.Unmarshal(raw, &run) == nil && run.AgentName == job.Record.AgentName {
			frozen := FreezeRecognizedQuestionInputDigests(run.Questions, run.Grade)
			for i, q := range run.Questions {
				if q.InputDigest != "" && frozen[i].InputDigest == q.InputDigest {
					sources = append(sources, q)
				}
			}
		}
		if len(sources) == 0 {
			if receipt, ok := o.readRecognitionReceipt(job.Record.RecordID); ok && receipt.AgentName == job.Record.AgentName {
				sources = receipt.Questions
			}
		}
	}
	if len(sources) == 0 {
		return
	}
	for i := range questions {
		q := &questions[i]
		for _, prior := range sources {
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
