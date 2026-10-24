package usecase

import (
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type reviewPaperSharedSource struct {
	Key, Current, Material string
}

// 父材料的呈现身份与同题归并分开；周练仍保留每道题独立可读的完整题面。
func attachReviewPaperSharedSources(projected []reviewPaperItem, queue []ReviewItem, sources []k12storage.ReviewPaperSource, groups []k12.ReviewSourceGroup) []reviewPaperItem {
	items := make(map[string]ReviewItem, len(queue))
	for _, item := range queue {
		if item.Record != nil {
			items[item.Record.RecordID] = item
		}
	}
	byGroup := make(map[string]k12.ReviewSourceGroup, len(groups))
	for _, group := range groups {
		byGroup[group.GroupID] = group
	}
	for i := range projected {
		item := projected[i].ReviewItem
		var selected *k12.ReviewSourceGroupMember
		if len(projected[i].SourceGroupIDs) > 0 {
			group, ok := byGroup[projected[i].SourceGroupIDs[0]]
			if !ok || group.CanonicalStatement != projected[i].Statement {
				continue
			}
			item, ok = items[group.CanonicalRecordID]
			if !ok {
				continue
			}
			for j := range group.Members {
				if group.Members[j].RecordID == group.CanonicalRecordID {
					selected = &group.Members[j]
					break
				}
			}
		}
		if item.Record == nil || item.IsAccum() {
			continue
		}
		var shared *reviewPaperSharedSource
		for _, source := range sources {
			if source.RecordID != item.Record.RecordID {
				continue
			}
			if selected != nil && (source.JobID != selected.JobID || source.ProblemID != selected.ProblemID ||
				source.AttemptID != selected.AttemptID || source.InputRevision != selected.InputRevision ||
				source.ConfirmedVersion != selected.ConfirmedVersion || source.InputDigest != selected.InputDigest || source.ResultDigest != selected.ResultDigest) {
				continue
			}
			q, valid := reviewedSourceQuestion(item, source)
			if !valid || !reviewPaperRecordMatchesQuestion(item, q) || q.ParentProblemID == "" ||
				source.ParentProblemID != q.ParentProblemID || source.ParentPageAssetID != q.PageAssetID ||
				source.ParentKind != "compound_parent" || source.ParentCanonicalVersion < 1 {
				continue
			}
			rest, ok := strings.CutPrefix(projected[i].Statement, q.RawTranscription)
			material := strings.Trim(rest, "\r\n")
			if !ok || !strings.HasPrefix(rest, "\n\n") || strings.TrimSpace(material) == "" {
				continue
			}
			key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%s", item.Record.AgentName, item.Subject(),
				source.JobID, source.PageDigest, q.ParentProblemID, source.ParentCanonicalVersion, source.ConfirmedVersion,
				source.InputRevision, k12.ReviewSourceTextDigest(material))
			if shared != nil && (shared.Key != key || shared.Current != q.RawTranscription) {
				shared = nil
				break
			}
			shared = &reviewPaperSharedSource{Key: key, Current: q.RawTranscription, Material: material}
		}
		projected[i].SharedSource = shared
	}
	return projected
}

// 仅相邻且同源、同修订、共同材料逐字相同的子题共享题干；题目数量和来源均不合并。
func reviewPaperDisplayStatements(items []reviewPaperItem) []string {
	statements := make([]string, len(items))
	for i, item := range items {
		statements[i] = item.Statement
	}
	for i := 0; i < len(items); {
		shared := items[i].SharedSource
		if shared == nil {
			i++
			continue
		}
		end := i + 1
		for end < len(items) && items[end].SharedSource != nil && items[end].SharedSource.Key == shared.Key {
			end++
		}
		if end > i+1 {
			statements[i] = shared.Material + "\n\n" + shared.Current
			for j := i + 1; j < end; j++ {
				statements[j] = items[j].SharedSource.Current
			}
		}
		i = end
	}
	return statements
}
