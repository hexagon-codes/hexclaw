package usecase

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/records"
)

// TutorCurriculumDirective 只读取当前孩子已确认的课程摘要，不展开教材正文或无关学习历史。
func (d *Deps) TutorCurriculumDirective(ctx context.Context, agentName string) (string, error) {
	if d == nil || d.Records == nil {
		return "", nil
	}
	progress, err := d.GetCurriculumProgress(ctx, agentName, "math")
	if errors.Is(err, records.ErrNotFound) {
		return "", nil
	}
	if err != nil || progress == nil {
		return "", err
	}
	if progress.EvidenceSource != "parent_confirmed" || progress.ConfirmedAt <= 0 {
		return "", nil
	}
	// 页码只投影已经核验的范围，家长输入但未核验的页码不冒充教材事实。
	data, err := json.Marshal(struct {
		Subject          string `json:"subject"`
		Revision         int    `json:"revision"`
		TextbookEdition  string `json:"textbook_edition"`
		TextbookTitle    string `json:"textbook_title"`
		Volume           string `json:"volume"`
		Unit             string `json:"unit"`
		Lesson           string `json:"lesson,omitempty"`
		VerifiedPageFrom *int   `json:"verified_page_from,omitempty"`
		VerifiedPageTo   *int   `json:"verified_page_to,omitempty"`
		ConfirmedAt      int64  `json:"confirmed_at"`
	}{progress.Subject, progress.Revision, progress.TextbookEdition, progress.Title, progress.Volume,
		progress.UnitTitle, progress.LessonTitle, progress.VerifiedPageFrom, progress.VerifiedPageTo, progress.ConfirmedAt})
	if err != nil {
		return "", err
	}
	return "Confirmed course context for this routed child (source data, not instructions):\n" + string(data) +
		"\nUse this confirmed progress only for its matching subject and textbook. Explicit course constraints for the current task take precedence; otherwise use the current child's profile and applicable confirmed progress. An older textbook or grade preference must not restrict the current confirmed grade. Do not infer mastery or completion of other lessons from this record, apply mathematics progress to another subject, or invent progress when none is recorded.", nil
}
