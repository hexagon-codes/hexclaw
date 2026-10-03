package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// InsightsConsumer 学情信号 Outbox 消费者（§6.9/§6.11「学情刷新：消费领域事件并幂等重算」）。
//
// 它接替此前用例内联的 WriteWeakness：错题域写与 k12.mistake.recorded 事件同事务落库，
// 本消费者按 payload 还原两条录入路径的既有措辞——
//   - photo（批改判错，含老记录空 entry_source）：「在「kp」出错：错因」，同题再错（created=false）
//     仍写信号（重复出错本身是有效的薄弱证据，与原 GradeHomeworkProblem 行为一致）；
//   - manual（家长手动记入）：「在「kp」出错（家长手动记入）：错因」，仅新建时写
//     （与原 RecordMistake 行为一致）。
//
// 事件身份贯穿文件投影；文件侧回执覆盖写入成功但消费标记尚未提交的重放窗口。
// 不同作答的事件 ID 不同，即使薄弱点文字相同也保留独立证据。
type InsightsConsumer struct {
	Insights Insights
	Records  interface {
		LatestInsightCorrection(context.Context, string, string) (k12.GradingAssessmentCorrection, error)
	}
}

// Name 消费者标识（outbox_consumptions 去重键的一半）。
func (c InsightsConsumer) Name() string { return "learning-insights" }

// Handle 消费一条事件。未接线 Insights 时静默跳过（与原 d.Insights != nil 判定同语义）。
func (c InsightsConsumer) Handle(ctx context.Context, ev k12storage.OutboxEvent) error {
	if c.Insights == nil || (ev.EventType != k12storage.EventMistakeRecorded && ev.EventType != k12storage.EventAssessmentCorrected) {
		return nil
	}
	sourceID := ev.EventID
	if ev.EventType == k12storage.EventAssessmentCorrected {
		var corrected k12storage.AssessmentCorrectedPayload
		if err := json.Unmarshal([]byte(ev.Payload), &corrected); err != nil {
			return err
		}
		if corrected.AgentName != ev.AgentName || corrected.OriginalEventID == "" {
			return errors.New("insight correction source mismatch")
		}
		sourceID = corrected.OriginalEventID
	}
	if c.Records != nil {
		correction, err := c.Records.LatestInsightCorrection(ctx, ev.AgentName, sourceID)
		if err == nil {
			return c.applyCorrection(ctx, sourceID, correction)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if ev.EventType == k12storage.EventAssessmentCorrected {
		return errors.New("insight correction could not be loaded")
	}
	var p k12storage.MistakeRecordedPayload
	if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
		return fmt.Errorf("学情消费者: 解析 payload: %w", err)
	}
	if p.KnowledgePoint == "" {
		return nil // 与原语义一致：无知识点不写薄弱信号
	}
	var note string
	if p.EntrySource == k12.MistakeEntryManual {
		if !p.Created {
			return nil // 手动路径幂等去重命中不重复写信号（原语义）
		}
		note = fmt.Sprintf("在「%s」出错（家长手动记入）：%s", p.KnowledgePoint, p.ErrorCause)
	} else {
		note = fmt.Sprintf("在「%s」出错：%s", p.KnowledgePoint, p.ErrorCause)
	}
	return c.Insights.WriteWeakness(ctx, ev.EventID, p.AgentName, p.KnowledgePoint, note)
}

func (c InsightsConsumer) applyCorrection(ctx context.Context, sourceID string, correction k12.GradingAssessmentCorrection) error {
	writer, ok := c.Insights.(RevisableInsights)
	if !ok {
		return errors.New("insight writer does not support corrections")
	}
	var note, knowledgePoint string
	if correction.Assessment.Status == k12.GradingAssessmentWrong {
		var result PhotoGradeItem
		if err := json.Unmarshal([]byte(correction.Assessment.ResultJSON), &result); err != nil {
			return err
		}
		knowledgePoint = result.Grade.Outcome.KnowledgePoint
		if knowledgePoint != "" {
			note = fmt.Sprintf("在「%s」出错：%s", knowledgePoint, result.Grade.Outcome.ErrorCause)
		}
	}
	return writer.ReviseWeakness(ctx, sourceID, correction.Revision, correction.Assessment.AgentName, knowledgePoint, note)
}
