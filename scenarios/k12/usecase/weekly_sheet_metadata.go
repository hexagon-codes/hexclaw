package usecase

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var weeklySheetPeriodLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-第([1-9]|[1-4][0-9]|5[0-3])周$`)

// mistakeSheetPeriod 使用实例既有时区，并把生成日期和ISO自然周一起冻结到卷面。
func (d Deps) mistakeSheetPeriod(ctx context.Context, agent string) (string, error) {
	settings, err := d.GetWeeklyPracticeSettings(ctx, agent)
	if err != nil {
		return "", err
	}
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return "", fmt.Errorf("invalid weekly sheet timezone: %w", err)
	}
	at := time.Unix(d.now(), 0).In(location)
	_, week := at.ISOWeek()
	return fmt.Sprintf("%s-第%d周", at.Format("2006-01-02"), week), nil
}

// weeklySheetDocumentParts 仅提取本应用写入标题后的日期行，题目正文不参与识别。
func weeklySheetDocumentParts(canonical string) (period, body string) {
	const heading = "# 本周错题卷\n\n"
	if !strings.HasPrefix(canonical, heading) {
		return "", canonical
	}
	remainder := strings.TrimPrefix(canonical, heading)
	line, rest, ok := strings.Cut(remainder, "\n\n")
	if !ok || !weeklySheetPeriodLine.MatchString(line) {
		return "", canonical
	}
	if _, err := time.Parse("2006-01-02", line[:10]); err != nil {
		return "", canonical
	}
	return line, heading + rest
}

func weeklySheetDocumentTitle(canonical string) string {
	period, _ := weeklySheetDocumentParts(canonical)
	if period == "" {
		return "本周错题卷"
	}
	return "本周错题卷-" + period
}
