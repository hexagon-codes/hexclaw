package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

const repeatedUnmasteredThreshold = 2

var insightLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// TrendCounts is the current grade-term mistake-state distribution.
type TrendCounts struct {
	Mastered  int `json:"mastered"`
	Reviewing int `json:"reviewing"`
	Retried   int `json:"retried"`
	Archived  int `json:"archived"`
	Total     int `json:"total"`
}

// WeakPoint is a current-month, current-term source projection. Share always
// uses MonthNewMistakes as its denominator; Desktop must not recompute it from
// another request or window.
type WeakPoint struct {
	Subject        string  `json:"subject"`
	KnowledgePoint string  `json:"knowledge_point"`
	Count          int     `json:"count"`
	Share          float64 `json:"share"`
}

// InsightReport is a one-snapshot read model. Every visible number, drill-down
// filter and canonical report render is derived from SourceRecordIDs at AsOf.
type InsightReport struct {
	Learner             string   `json:"learner"`
	GradeTerm           string   `json:"grade_term"`
	AsOf                int64    `json:"as_of"`
	SourceDigest        string   `json:"source_digest"`
	SourceRecordIDs     []string `json:"source_record_ids"`
	UnscopedSourceCount int      `json:"unscoped_source_count"`
	ReviewWeekStart     int64    `json:"review_week_start"`
	ReviewWeekEnd       int64    `json:"review_week_end"`

	Trend            TrendCounts `json:"trend"`
	WeakTop3         []WeakPoint `json:"weak_top3"`
	MonthNewMistakes int         `json:"month_new_mistakes"`
	WeekPending      int         `json:"week_pending"`
	PracticePending  int         `json:"practice_pending"`
	// -1 means the current review-week denominator is zero.
	ReviewCompletionRate float64  `json:"review_completion_rate"`
	ConsecutiveFailKPs   []string `json:"consecutive_fail_kps"`
	Suggestion           string   `json:"suggestion"`
}

type weakPointKey struct {
	subject string
	point   string
}

// InsightReport freezes profile, mistakes, corrective accumulations and
// practice sets in one storage transaction, then derives independent semester,
// month and review-week windows from that exact source set.
func (d Deps) InsightReport(ctx context.Context, agentName string) (InsightReport, error) {
	if strings.TrimSpace(agentName) == "" {
		return InsightReport{}, fmt.Errorf("usecase: agentName 不可空")
	}
	if d.Records == nil {
		return InsightReport{}, fmt.Errorf("usecase: 未配置 K12 记录存储")
	}
	asOf := d.now()
	source, err := d.Records.ReadInsightSourceSnapshot(ctx, agentName, asOf)
	if err != nil {
		return InsightReport{}, fmt.Errorf("usecase: 冻结学情快照: %w", err)
	}
	weekStart, weekEnd := reviewWeekWindow(asOf)
	report := InsightReport{
		Learner:              source.Learner,
		GradeTerm:            source.Profile.GradeTerm,
		AsOf:                 source.AsOf,
		SourceDigest:         source.SourceDigest,
		SourceRecordIDs:      append([]string(nil), source.SourceRecordIDs...),
		UnscopedSourceCount:  source.UnscopedSourceCount,
		ReviewWeekStart:      weekStart,
		ReviewWeekEnd:        weekEnd,
		ReviewCompletionRate: -1,
	}
	monthStart := startOfInsightMonth(asOf)
	weakCount := map[weakPointKey]int{}
	failCount := map[string]int{}

	for _, record := range source.Mistakes {
		fields, parseErr := k12.ParseMistakeFields(record.Fields)
		if parseErr != nil {
			return InsightReport{}, fmt.Errorf("usecase: 解析学情错题 %s: %w", record.RecordID, parseErr)
		}
		report.Trend.Total++
		switch record.Status {
		case k12.StatusMastered:
			report.Trend.Mastered++
		case k12.StatusRetried:
			report.Trend.Retried++
		case k12.StatusArchived:
			report.Trend.Archived++
		default:
			report.Trend.Reviewing++
		}
		if record.Status != k12.StatusMastered &&
			record.Status != k12.StatusArchived &&
			strings.TrimSpace(fields.KnowledgePoint) != "" {
			failCount[fields.KnowledgePoint]++
		}
		if record.CreatedAt >= monthStart && record.CreatedAt <= asOf {
			report.MonthNewMistakes++
			point := strings.TrimSpace(fields.KnowledgePoint)
			if point != "" {
				subject := strings.TrimSpace(fields.Subject)
				if subject == "" {
					subject = "数学"
				}
				weakCount[weakPointKey{subject: subject, point: point}]++
			}
		}
		if insightMistakeDue(record.Status, fields.SpotCheckState, record.DueAt, asOf) {
			report.WeekPending++
		}
	}
	for _, record := range source.Accumulations {
		fields, parseErr := k12.ParseAccumFields(record.Fields)
		if parseErr != nil {
			return InsightReport{}, fmt.Errorf("usecase: 解析学情积累 %s: %w", record.RecordID, parseErr)
		}
		if record.Status == k12.AccumStatusReviewing &&
			k12.AccumIsCorrective(fields.EntryType) &&
			record.DueAt != nil &&
			*record.DueAt <= asOf {
			report.WeekPending++
		}
	}

	total, done := 0, 0
	for _, record := range source.PracticeSets {
		if record.Status == k12.PracticeStatusCancelled {
			continue
		}
		fields, parseErr := k12.ParsePracticeSetFields(record.Fields)
		if parseErr != nil {
			return InsightReport{}, fmt.Errorf("usecase: 解析学情练习集 %s: %w", record.RecordID, parseErr)
		}
		if record.Status == k12.PracticeStatusDraft {
			for _, item := range fields.Items {
				if k12.PracticeItemPublishable(item) {
					report.PracticePending++
				}
			}
		}
		if fields.FinalizedAt < weekStart || fields.FinalizedAt >= weekEnd {
			continue
		}
		wholeGraded := record.Status == k12.PracticeStatusGraded ||
			record.Status == k12.PracticeStatusClosed
		for _, item := range fields.Items {
			if !k12.PracticeItemPublishable(item) {
				continue
			}
			total++
			if item.ResultCorrect != nil || wholeGraded {
				done++
			}
		}
	}
	if total > 0 {
		report.ReviewCompletionRate = float64(done) / float64(total)
	}
	report.WeakTop3 = topWeakPoints(weakCount, report.MonthNewMistakes, 3)
	report.ConsecutiveFailKPs = keysAtLeast(failCount, repeatedUnmasteredThreshold)
	report.Suggestion = buildSourceBackedSuggestion(report, source)
	return report, nil
}

func insightMistakeDue(status, spotCheckState string, dueAt *int64, asOf int64) bool {
	if dueAt == nil || *dueAt > asOf || spotCheckState == k12.SpotCheckScheduled {
		return false
	}
	return status == k12.StatusNew || status == k12.StatusExplained
}

func topWeakPoints(counts map[weakPointKey]int, denominator, limit int) []WeakPoint {
	out := make([]WeakPoint, 0, len(counts))
	for key, count := range counts {
		share := 0.0
		if denominator > 0 {
			share = float64(count) / float64(denominator)
		}
		out = append(out, WeakPoint{
			Subject:        key.subject,
			KnowledgePoint: key.point,
			Count:          count,
			Share:          share,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].KnowledgePoint < out[j].KnowledgePoint
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func keysAtLeast(counts map[string]int, minimum int) []string {
	var out []string
	for key, count := range counts {
		if count >= minimum {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func buildSuggestion(report InsightReport) string {
	if report.Trend.Total == 0 {
		return "本月还没有错题记录，继续保持。"
	}
	if len(report.ConsecutiveFailKPs) > 0 {
		return fmt.Sprintf("「%s」有多条尚未掌握的错题记录，建议优先核对这些题的作答。", report.ConsecutiveFailKPs[0])
	}
	if len(report.WeakTop3) > 0 {
		return fmt.Sprintf("本月薄弱点是「%s」，可优先出这个知识点的错题卷。", report.WeakTop3[0].KnowledgePoint)
	}
	return "本月复习进展不错，保持节奏。"
}

func buildSourceBackedSuggestion(report InsightReport, source k12storage.InsightSourceSnapshot) string {
	base := buildSuggestion(report)
	mistakes := append([]*records.AgentRecord(nil), source.Mistakes...)
	sort.Slice(mistakes, func(i, j int) bool {
		if mistakes[i].CreatedAt != mistakes[j].CreatedAt {
			return mistakes[i].CreatedAt > mistakes[j].CreatedAt
		}
		return mistakes[i].RecordID < mistakes[j].RecordID
	})
	for _, record := range mistakes {
		if record.Status == k12.StatusArchived || record.Status == k12.StatusMastered {
			continue
		}
		fields, err := k12.ParseMistakeFields(record.Fields)
		if err != nil || strings.TrimSpace(fields.ObservedStudentAnswer) == "" || strings.TrimSpace(fields.CanonicalAnswer) == "" {
			continue
		}
		if len(report.WeakTop3) > 0 && (fields.KnowledgePoint != report.WeakTop3[0].KnowledgePoint || fields.Subject != report.WeakTop3[0].Subject && !(fields.Subject == "" && report.WeakTop3[0].Subject == "数学")) {
			continue
		}
		var out strings.Builder
		_, _, sourceAnswer := SplitRetryPresentation(fields.CanonicalAnswer)
		if strings.TrimSpace(sourceAnswer) == "" {
			sourceAnswer = fields.CanonicalAnswer
		}
		fmt.Fprintf(&out, "%s 当前观察：题目“%s”留存的作答是“%s”。家长讲法：先请孩子说明题目条件和每一步的含义，再对照本题参考答案：%s。", base, mathtext.PlainMarkdownText(fields.Question), mathtext.PlainMarkdownText(fields.ObservedStudentAnswer), mathtext.PlainMarkdownText(sourceAnswer))
		for _, set := range source.PracticeSets {
			if set.Status == k12.PracticeStatusCancelled || set.Status == k12.PracticeStatusClosed {
				continue
			}
			practice, err := k12.ParsePracticeSetFields(set.Fields)
			if err != nil {
				continue
			}
			for _, item := range practice.Items {
				if item.SourceProblemID != record.RecordID || !k12.PracticeItemPublishable(item) || item.ResultCorrect != nil || item.GenerationJobID == "" || item.GenerationStatus != k12.PracticeItemGenerationReady {
					continue
				}
				// 建议正文沿既有纯文本区域展示；保留核对题完整参考，避免泄漏 Markdown 控制字符。
				fmt.Fprintf(&out, " 核对题：使用已有已验证变式“%s”；家长参考答案：%s。本次通过依据：新作答的答案、单位或题目要求的要点正确，并能说明关键步骤的依据。作答条件仍以真实记录为准，一次做对不代表稳定掌握。", mathtext.PlainMarkdownText(item.QuestionMarkdown), mathtext.PlainMarkdownText(item.ExpectedAnswerMarkdown))
				return out.String()
			}
		}
		out.WriteString(" 当前没有可用于本次核对的待答已验证变式，先让孩子解释原题各步骤；这只能核对本题理解，不能据此认定独立掌握。")
		return out.String()
	}
	if report.Trend.Total > 0 {
		return base + " 当前记录没有足够的清楚作答和标准解答来形成逐步讲法，先核对原题与作答，不据此推断具体误区。"
	}
	return base
}

func startOfInsightMonth(unixSeconds int64) int64 {
	current := time.Unix(unixSeconds, 0).In(insightLocation)
	return time.Date(current.Year(), current.Month(), 1, 0, 0, 0, 0, insightLocation).Unix()
}

func reviewWeekWindow(unixSeconds int64) (int64, int64) {
	current := time.Unix(unixSeconds, 0).In(insightLocation)
	daysSinceFriday := (int(current.Weekday()) - int(time.Friday) + 7) % 7
	startDay := current.AddDate(0, 0, -daysSinceFriday)
	start := time.Date(
		startDay.Year(), startDay.Month(), startDay.Day(), 19, 0, 0, 0, insightLocation,
	)
	if current.Before(start) {
		start = start.AddDate(0, 0, -7)
	}
	return start.Unix(), start.AddDate(0, 0, 7).Unix()
}
