package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 回传提醒（架构设计-v0.5.0 §3.13 回传提醒规则，2026-07-18 补）：闭环最后一米，
// 关键转化不交给家长记性。cron 每日 20:00 扫描节拍（cronspec.go KindReturnReminder），
// "T+1 事件"语义由本层按 finalized_at=昨日 筛选实现。

// reminderLoc 提醒窗口时区（§3.13 默认 Asia/Shanghai；无夏令时，固定 +8，
// 免依赖系统 tzdata）。
var reminderLoc = time.FixedZone("Asia/Shanghai", 8*3600)

// ReturnReminder 只读生成昨日固化且仍缺回传题目的提醒文案。
func (d Deps) ReturnReminder(ctx context.Context, agentName string) (text string, skip bool, err error) {
	candidates, err := d.returnReminderCandidates(ctx, agentName)
	if err != nil {
		return "", false, err
	}
	lines := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		lines = append(lines, candidate.text)
	}
	return strings.Join(lines, "\n"), len(lines) == 0, nil
}

type returnReminderCandidate struct {
	recordID string
	text     string
}

func (d Deps) returnReminderCandidates(ctx context.Context, agentName string) ([]returnReminderCandidate, error) {
	if agentName == "" {
		return nil, fmt.Errorf("%w: agentName is required", ErrInvalidInput)
	}
	recs, err := d.Records.ListByScope(ctx, agentName, k12.CollectionPracticeSet, "")
	if err != nil {
		return nil, fmt.Errorf("usecase: list return reminder candidates: %w", err)
	}
	now := time.Unix(d.now(), 0).In(reminderLoc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, reminderLoc)
	yesterdayStart := todayStart.AddDate(0, 0, -1)

	var candidates []returnReminderCandidate
	for _, r := range recs {
		if r.Status != k12.PracticeStatusAssigned && r.Status != k12.PracticeStatusSubmitted {
			continue
		}
		f, perr := k12.ParsePracticeSetFields(r.Fields)
		if perr != nil {
			continue // 字段损坏的卷不阻断整轮提醒
		}
		if f.FinalizedAt < yesterdayStart.Unix() || f.FinalizedAt >= todayStart.Unix() {
			continue // 非昨日固化：过窗不补发，未到不早发
		}
		if f.ReminderSentAt != 0 || f.ReminderDismissed {
			continue // 每卷最多一次；家长手动关闭后不再发
		}
		publishable, _ := k12.PublishableItems(f)
		missingReturn := false
		for _, item := range publishable {
			if !item.Returned {
				missingReturn = true
				break
			}
		}
		if !missingReturn {
			continue
		}
		// §3.13 文案口径「昨天的卷子做完了吗？拍照回传就能自动批改」+ paper_no + 题数；
		// 温和、不催促、不制造焦虑（§4.11 家长向用语）。
		candidates = append(candidates, returnReminderCandidate{recordID: r.RecordID,
			text: fmt.Sprintf("昨天的练习卷 %s（共 %d 题）做完了吗？拍照回传就能自动批改。", f.PaperNo, len(publishable))})
	}
	return candidates, nil
}

// DeliverCronResult 使用现有批次投递默认任务结果；无直接绑定时保留应用内通知。
func (d Deps) DeliverCronResult(ctx context.Context, agentName string, kind K12CronKind, content string, notify func(string) error) error {
	if kind == KindReturnReminder {
		return d.deliverReturnReminders(ctx, agentName, notify)
	}
	var commandID string
	switch kind {
	case KindWeeklySheet:
		start, _ := reviewWeekWindow(d.now())
		commandID = fmt.Sprintf("%d", start)
	case KindSemesterSpring, KindSemesterFall:
		commandID = fmt.Sprintf("%d", time.Unix(d.now(), 0).In(reminderLoc).Year())
	default:
		return fmt.Errorf("%w: unsupported automation kind %q", ErrInvalidInput, kind)
	}
	_, err := d.deliverAutomationText(ctx, agentName, string(kind), commandID, content)
	if errors.Is(err, ErrNoActiveDirectBindings) && notify != nil {
		return notify(content)
	}
	return err
}

func (d Deps) deliverReturnReminders(ctx context.Context, agentName string, notify func(string) error) error {
	candidates, err := d.returnReminderCandidates(ctx, agentName)
	if err != nil {
		return err
	}
	var failures []error
	var localCandidates []returnReminderCandidate
	for _, candidate := range candidates {
		_, err := d.deliverAutomationText(ctx, agentName, string(KindReturnReminder), candidate.recordID, candidate.text)
		if errors.Is(err, ErrNoActiveDirectBindings) && notify != nil {
			localCandidates = append(localCandidates, candidate)
			continue
		}
		if err == nil {
			err = d.markReturnReminderSent(context.WithoutCancel(ctx), agentName, candidate.recordID)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(localCandidates) > 0 {
		lines := make([]string, 0, len(localCandidates))
		for _, candidate := range localCandidates {
			lines = append(lines, candidate.text)
		}
		if err := notify(strings.Join(lines, "\n")); err != nil {
			failures = append(failures, err)
		} else {
			for _, candidate := range localCandidates {
				if err := d.markReturnReminderSent(context.WithoutCancel(ctx), agentName, candidate.recordID); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}
	return errors.Join(failures...)
}

func (d Deps) markReturnReminderSent(ctx context.Context, agentName, recordID string) error {
	// 发送后重新读取最新卷面，乐观锁避免覆盖同时发生的回传或批改。
	v, err := d.GetPracticeSet(ctx, agentName, recordID)
	if err != nil || v.Fields.ReminderSentAt != 0 {
		return err
	}
	v.Fields.ReminderSentAt = d.now()
	return d.savePracticeFields(ctx, v, v.Record.Status)
}
