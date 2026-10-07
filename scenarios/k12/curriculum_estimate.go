package k12

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var coverGradePattern = regexp.MustCompile(`([一二三四五六]年级)\s*(上册|下册)`)

func ParseTextbookGradeTerm(text string) string {
	terms := map[string]bool{}
	for _, m := range coverGradePattern.FindAllStringSubmatch(strings.ReplaceAll(text, "#", ""), -1) {
		terms[m[1]+strings.TrimSuffix(m[2], "册")] = true
	}
	if len(terms) != 1 {
		return ""
	}
	for term := range terms {
		return term
	}
	return ""
}

// EstimateCurriculumUnit 参考学期仅用于目录建议，不代表真实学校校历或学习完成。
func EstimateCurriculumUnit(catalog CurriculumCatalog, gradeTerm string, now time.Time) (CurriculumCatalogUnit, *CurriculumEstimateBasis, error) {
	if !ValidProfileGradeTerm(gradeTerm) || len(catalog.Units) == 0 {
		return CurriculumCatalogUnit{}, nil, fmt.Errorf("grade and curriculum units required")
	}
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return CurriculumCatalogUnit{}, nil, err
	}
	now = now.In(zone)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	var start, end time.Time
	var distance time.Duration
	for year := today.Year() - 1; year <= today.Year()+1; year++ {
		a, b := time.Date(year, 9, 1, 0, 0, 0, 0, zone), time.Date(year+1, 1, 31, 0, 0, 0, 0, zone)
		if strings.HasSuffix(gradeTerm, "下") {
			a, b = time.Date(year, 3, 1, 0, 0, 0, 0, zone), time.Date(year, 6, 30, 0, 0, 0, 0, zone)
		}
		d := time.Duration(0)
		if today.Before(a) {
			d = a.Sub(today)
		} else if today.After(b) {
			d = today.Sub(b)
		}
		if start.IsZero() || d < distance {
			start, end, distance = a, b, d
		}
	}
	total, elapsed := 0, 0
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday {
			total++
			if !day.After(today) {
				elapsed++
			}
		}
	}
	weights := make([]int, len(catalog.Units))
	sum := 0
	for i, u := range catalog.Units {
		w := len(u.Lessons)
		if w == 0 && u.PageFrom > 0 && u.PageTo >= u.PageFrom {
			w = u.PageTo - u.PageFrom + 1
		}
		if w == 0 {
			w = 1
		}
		weights[i] = w
		sum += w
	}
	index := 0
	if !today.Before(start) && total > 0 {
		target := float64(elapsed) / float64(total) * float64(sum)
		cumulative := 0
		index = len(weights) - 1
		for i, w := range weights {
			cumulative += w
			if target < float64(cumulative) {
				index = i
				break
			}
		}
	}
	basis := &CurriculumEstimateBasis{AsOfDate: today.Format("2006-01-02"), ReferenceTermStart: start.Format("2006-01-02"), ReferenceTermEnd: end.Format("2006-01-02"), WeightMethod: "catalog_lessons_or_page_span"}
	return catalog.Units[index], basis, nil
}
