package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// reviewPaperItem 保留最早到期的代表题与全部来源，不改变各次作答的学习事实。
type reviewPaperItem struct {
	ReviewItem
	Statement       string
	SourceRecordIDs []string
	SourceGroupIDs  []string
	SharedSource    *reviewPaperSharedSource
}

func (d Deps) reviewPaperProjection(ctx context.Context, agentName string, queue []ReviewItem) ([]reviewPaperItem, error) {
	ids := make([]string, 0, len(queue))
	for _, item := range queue {
		if item.Record != nil && item.Record.Collection == k12.CollectionMistakes {
			ids = append(ids, item.Record.RecordID)
		}
	}
	groups, err := d.Records.ListReviewSourceGroups(ctx, agentName, ids)
	if err != nil {
		return nil, err
	}
	// 审核代表的来源不受复习时间限制，补读记录只供来源核对，不加入应练队列。
	references := append([]ReviewItem(nil), queue...)
	for _, group := range groups {
		if containsReviewSource(ids, group.CanonicalRecordID) {
			continue
		}
		record, err := d.Records.Get(ctx, group.CanonicalRecordID)
		if errors.Is(err, records.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if record == nil || record.AgentName != agentName || record.Collection != k12.CollectionMistakes {
			continue
		}
		fields, err := k12.ParseMistakeFields(record.Fields)
		if err != nil {
			continue
		}
		ids = append(ids, record.RecordID)
		references = append(references, ReviewItem{Record: record, Fields: fields})
	}
	sources, err := d.Records.ListReviewPaperSources(ctx, agentName, ids)
	if err != nil {
		return nil, err
	}
	projected := projectReviewPaper(queue, sources, groups...)
	return attachReviewPaperSharedSources(projected, references, sources, groups), nil
}

func projectReviewPaper(queue []ReviewItem, sources []k12storage.ReviewPaperSource, groups ...k12.ReviewSourceGroup) []reviewPaperItem {
	byRecord := make(map[string][]k12storage.ReviewPaperSource)
	for _, source := range sources {
		byRecord[source.RecordID] = append(byRecord[source.RecordID], source)
	}
	reviewed := make(map[string][]k12.ReviewSourceGroup)
	for _, group := range groups {
		normalized, err := group.Normalized()
		if err != nil {
			continue
		}
		for _, member := range normalized.Members {
			reviewed[member.RecordID] = append(reviewed[member.RecordID], normalized)
		}
	}
	var candidates []reviewPaperItem
	var roots []int
	root := func(index int) int {
		for roots[index] != index {
			index = roots[index]
		}
		return index
	}
	byKey := make(map[string]int)
	for _, item := range queue {
		if item.Record == nil {
			continue
		}
		statement := item.Title()
		if !item.IsAccum() {
			statement = restoreReviewPaperStatement(item, byRecord[item.Record.RecordID])
		}
		prefix := item.Record.AgentName + "\x00" + item.Record.Collection + "\x00" + item.Subject() + "\x00"
		keys := []string{prefix + "record:" + item.Record.RecordID}
		sourceIDs, groupIDs := []string{item.Record.RecordID}, []string(nil)
		selectedGroups := applicableReviewSourceGroups(item, reviewed[item.Record.RecordID])
		for _, group := range selectedGroups {
			statement = group.CanonicalStatement
			keys = append(keys, prefix+"reviewed:"+group.GroupID)
			groupIDs = append(groupIDs, group.GroupID)
			for _, member := range group.Members {
				if !containsReviewSource(sourceIDs, member.RecordID) {
					sourceIDs = append(sourceIDs, member.RecordID)
				}
			}
		}
		// 文本来源比较完整题面；照片来源同时保留原图身份，不能忽略图中条件。
		if full := strings.Join(strings.Fields(statement), " "); !item.IsAccum() && !reviewPaperUsesPhotoSource(item.Fields) && full != "" {
			keys = append(keys, prefix+"statement:"+full)
		}
		if reviewPaperUsesPhotoSource(item.Fields) && !item.IsAccum() {
			for _, source := range byRecord[item.Record.RecordID] {
				if q, valid := frozenReviewPaperQuestion(item, source); valid && reviewPaperRecordMatchesQuestion(item, q) {
					path := normalizedReviewNumberPath(q.SourceNumberPath)
					if full := reviewSourceTextKey(statement); path != "" && full != "" {
						keys = append(keys, prefix+"statement:"+source.PageDigest+"\x00"+string(q.ProblemKind)+"\x00"+
							normalizedReviewNumberPath(q.SourceSectionPath)+"\x00"+path+"\x00"+full)
					}
				}
				if key := reviewPaperSourceKey(item, source); key != "" {
					keys = append(keys, prefix+"source:"+key)
				}
			}
		}
		index := len(candidates)
		candidates = append(candidates, reviewPaperItem{ReviewItem: item, Statement: statement, SourceRecordIDs: sourceIDs, SourceGroupIDs: groupIDs})
		roots = append(roots, index)
		for _, key := range keys {
			if existing, found := byKey[key]; found {
				a, b := root(existing), root(index)
				if a < b {
					roots[b] = a
				} else {
					roots[a] = b
				}
			}
		}
		for _, key := range keys {
			byKey[key] = index
		}
	}
	var projected []reviewPaperItem
	byRoot := make(map[int]int)
	for index, candidate := range candidates {
		representative := root(index)
		position, found := byRoot[representative]
		if !found {
			position = len(projected)
			byRoot[representative] = position
			projected = append(projected, candidates[representative])
		}
		for _, id := range candidate.SourceRecordIDs {
			if !containsReviewSource(projected[position].SourceRecordIDs, id) {
				projected[position].SourceRecordIDs = append(projected[position].SourceRecordIDs, id)
			}
		}
		for _, id := range candidate.SourceGroupIDs {
			if !containsReviewSource(projected[position].SourceGroupIDs, id) {
				projected[position].SourceGroupIDs = append(projected[position].SourceGroupIDs, id)
			}
		}
	}
	return projected
}

// 旧记录未携带来源字段时沿照片兼容口径；没有真实来源凭据时不能只凭文字相同归并。
func reviewPaperUsesPhotoSource(fields k12.MistakeFields) bool {
	return fields.EntrySource == "" || fields.EntrySource == k12.MistakeEntryPhoto
}

// 来源组已由存储核对完整当前输入；这里再次限定当前题面，避免把其他实例或修订加入卷面。
func applicableReviewSourceGroups(item ReviewItem, groups []k12.ReviewSourceGroup) []k12.ReviewSourceGroup {
	if item.Record == nil || item.Record.Collection != k12.CollectionMistakes {
		return nil
	}
	var selected []k12.ReviewSourceGroup
	var statementDigest string
	for _, group := range groups {
		if group.AgentName != item.Record.AgentName {
			continue
		}
		for _, member := range group.Members {
			if member.RecordID != item.Record.RecordID || member.QuestionDigest != k12.ReviewSourceTextDigest(item.Fields.Question) {
				continue
			}
			if statementDigest != "" && group.CanonicalStatementDigest != statementDigest {
				return nil
			}
			statementDigest = group.CanonicalStatementDigest
			selected = append(selected, group)
			break
		}
	}
	return selected
}

func containsReviewSource(ids []string, id string) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

func reviewPaperSourceKey(item ReviewItem, source k12storage.ReviewPaperSource) string {
	q, valid := frozenReviewPaperQuestion(item, source)
	if !valid || !reviewPaperRecordMatchesQuestion(item, q) {
		return ""
	}
	current := reviewSourceTextKey(q.RawTranscription)
	if current == "" {
		return ""
	}
	// 当前题号不能代替完整条件；只使用已经被该错题采用的规范题面和全部材料。
	context := q
	context.RawTranscription = ""
	shared := reviewSourceTextKey(compactReviewPaperQuestion(context))
	if shared == "" {
		return ""
	}
	// 印刷题号保留完整父子路径；系统顺序、模型小题别名不能成为物理题目身份。
	path := normalizedReviewNumberPath(q.SourceNumberPath)
	if path == "" {
		// 无印刷题号时只接受已有合同能证明的单一份数空位；完整父条件必须同时相同。
		if q.ParentProblemID == "" {
			return ""
		}
		if _, ok := elementarymath.BuildPartCountSlotProblemFromSources(q.CanonicalMarkdown, q.RawTranscription, ""); !ok {
			return ""
		}
		if shared == current {
			return ""
		}
		path = "single-part-count-slot"
	}
	return source.PageDigest + "\x00" + string(q.ProblemKind) + "\x00" + normalizedReviewNumberPath(q.SourceSectionPath) + "\x00" + path + "\x00" + current + "\x00" + shared
}

func frozenReviewPaperQuestion(item ReviewItem, source k12storage.ReviewPaperSource) (RecognizedQuestion, bool) {
	if item.Record == nil || source.JobID == "" || source.ProblemID == "" || source.AttemptID == "" || source.InputDigest == "" ||
		source.AgentName != item.Record.AgentName || source.RecordID != item.Record.RecordID ||
		source.InputRevision != 1 || source.ConfirmedVersion != 1 || source.PageAssetID == "" ||
		source.PageDigest == "" || source.ResultDigest != modelInvocationDigest([]byte(source.ResultJSON)) {
		return RecognizedQuestion{}, false
	}
	var frozen PhotoGradeItem
	if json.Unmarshal([]byte(source.ResultJSON), &frozen) != nil {
		return RecognizedQuestion{}, false
	}
	q := frozen.Recognized
	if q.ProblemID != source.ProblemID || q.AttemptID != source.AttemptID || q.InputDigest != source.InputDigest ||
		q.PageAssetID != source.PageAssetID || q.ConfirmedVersion != source.ConfirmedVersion || q.CanonicalVersion != 1 ||
		q.RawTranscription == "" {
		return RecognizedQuestion{}, false
	}
	return q, true
}

func normalizedReviewNumberPath(path []string) string {
	parts := make([]string, 0, len(path))
	for _, part := range path {
		part = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r >= '！' && r <= '～' {
				return r - 0xFEE0
			}
			return r
		}, part))
		if strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") {
			part = strings.TrimSpace(part[1 : len(part)-1])
		}
		if part == "" {
			return ""
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "/")
}

// 同页原文只归一化排版字符，保留数字、英文单词和单位的大小写及词边界。
func reviewSourceTextKey(text string) string {
	text, _ = mathtext.ProjectReadable(text)
	text = strings.Map(func(r rune) rune {
		if r >= '！' && r <= '～' {
			return r - 0xFEE0
		}
		switch r {
		case '。':
			return '.'
		case '，':
			return ','
		case '∶', '：':
			return ':'
		}
		return r
	}, text)
	runes := []rune(text)
	var tokens []string
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		start := i
		i++
		if runes[start] >= '0' && runes[start] <= '9' {
			for i < len(runes) && (runes[i] >= '0' && runes[i] <= '9' || runes[i] == '.' && i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9') {
				i++
			}
		} else if runes[start] >= 'a' && runes[start] <= 'z' || runes[start] >= 'A' && runes[start] <= 'Z' {
			for i < len(runes) && (runes[i] >= 'a' && runes[i] <= 'z' || runes[i] >= 'A' && runes[i] <= 'Z') {
				i++
			}
		}
		tokens = append(tokens, string(runes[start:i]))
	}
	return strings.Join(tokens, "\x1f")
}
