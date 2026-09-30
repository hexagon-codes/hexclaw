package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/records"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
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

// TutorTextbookContext 将本轮采用的正文与同一冻结来源绑定，不复用全库检索结果。
type TutorTextbookContext struct {
	Title    string
	Snapshot GroundingSnapshot
	Result   GroundingSnapshotResult
}

// TutorTextbookGrounding 为普通聊天读取当前绑定的已核验数学教材；无相关正文时正常返回空结果。
func (d *Deps) TutorTextbookGrounding(ctx context.Context, agentName, query, grade string) (TutorTextbookContext, error) {
	if d == nil || d.Records == nil || d.Grounding == nil || strings.TrimSpace(d.TextbookOwnerID) == "" || strings.TrimSpace(query) == "" {
		return TutorTextbookContext{}, nil
	}
	snapshotter, supportsSnapshot := d.Grounding.(SnapshotGrounding)
	grounding, supportsEvidence := d.Grounding.(SnapshotGroundingEvidence)
	if !supportsSnapshot || !supportsEvidence {
		return TutorTextbookContext{}, nil
	}
	requested := k12storage.TextbookScope{OwnerID: strings.TrimSpace(d.TextbookOwnerID), AgentName: agentName, Subject: "math"}
	scope, found, err := d.Records.GetActiveTextbookGroundingScope(ctx, requested)
	if err != nil || !found {
		return TutorTextbookContext{}, err
	}
	catalog, handled, err := d.Records.GetActiveTextbookCatalog(ctx, requested)
	if err != nil {
		return TutorTextbookContext{}, err
	}
	if !handled || catalog.TextbookBindingID != scope.TextbookBindingID {
		return TutorTextbookContext{}, fmt.Errorf("tutor textbook binding changed while reading context")
	}
	snapshot, err := snapshotter.FreezeGroundingSnapshot(ctx, GroundingSnapshot{
		AgentName: agentName, LearnerID: agentName, Subject: "math", OwnerID: requested.OwnerID,
		TextbookBindingID: scope.TextbookBindingID, TextbookManifestID: scope.TextbookManifestID,
		DocumentID: scope.DocumentID, DocumentGeneration: scope.DocumentGeneration,
		SourceDigest: scope.SourceDigest, Edition: scope.Edition, Volume: scope.Volume,
		SegmentRefs: scope.SegmentRefs, PageRefs: scope.PageRefs, SourceMode: GroundingSourceModeVerifiedText,
	})
	if err != nil {
		return TutorTextbookContext{}, err
	}
	result, err := grounding.GroundSnapshotWithEvidence(ctx, snapshot, tutorTextbookQuery(query), grade)
	if err != nil {
		return TutorTextbookContext{}, err
	}
	if err := result.validate(snapshot); err != nil {
		return TutorTextbookContext{}, err
	}
	return TutorTextbookContext{Title: catalog.Title, Snapshot: snapshot, Result: result}, nil
}

// 保留原查询供完整课时名匹配，只补充题干中的连续长片段；短单位词、泛问法和讲解要求不作为命中依据。
func tutorTextbookQuery(query string) string {
	terms := []string{strings.ReplaceAll(strings.TrimSpace(query), "、", " ")}
	seen := map[string]bool{terms[0]: true}
	for _, phrase := range strings.FieldsFunc(query, func(r rune) bool { return !unicode.Is(unicode.Han, r) }) {
		if utf8.RuneCountInString(phrase) < 5 || seen[phrase] {
			continue
		}
		generic := false
		for _, word := range []string{"请", "帮我", "教材", "课本", "学段", "年级", "这道题", "讲解", "解释", "为什么", "多少", "怎么", "如何", "方法", "步骤", "易错", "家长", "孩子", "答案", "计算", "解法"} {
			if strings.Contains(phrase, word) {
				generic = true
				break
			}
		}
		if generic {
			continue
		}
		terms = append(terms, phrase)
		seen[phrase] = true
	}
	return strings.Join(terms, "、")
}
