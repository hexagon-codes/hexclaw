package usecase

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func reviewedSourceAssetIDs(archive *k12storage.ReviewSourceArchive) []string {
	if archive == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, page := range archive.Pages {
		seen[page.PageAssetID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// PackHexbakReviewSourceAssets 将冻结关联所需的历史原图纳入已有精确资产清单。
func PackHexbakReviewSourceAssets(agent string, archive *k12storage.ReviewSourceArchive) ([]HexbakAsset, error) {
	return packHexbakAssetIDs(agent, reviewedSourceAssetIDs(archive))
}

func reviewMemberArchiveKey(member k12.ReviewSourceGroupMember) string {
	raw, _ := json.Marshal(member)
	return string(raw)
}

// migrateReviewSourceArchive 只重定位冻结来源中的资产归属，不迁移或重建模型执行身份。
func migrateReviewSourceArchive(source, bak *Hexbak, assets map[string]string) error {
	if bak.ReviewSources == nil {
		return nil
	}
	questions := map[string]string{}
	originalQuestions := map[string]string{}
	for _, record := range source.Records {
		if record == nil || record.Collection != k12.CollectionMistakes {
			continue
		}
		var fields k12.MistakeFields
		if err := json.Unmarshal([]byte(record.Fields), &fields); err != nil {
			return err
		}
		originalQuestions[record.RecordID] = k12.ReviewSourceTextDigest(fields.Question)
	}
	for _, record := range bak.Records {
		if record == nil || record.Collection != k12.CollectionMistakes {
			continue
		}
		var fields k12.MistakeFields
		if err := json.Unmarshal([]byte(record.Fields), &fields); err != nil {
			return err
		}
		questions[record.RecordID] = k12.ReviewSourceTextDigest(fields.Question)
	}
	rewrite := func(value string) string {
		for from, to := range assets {
			value = strings.ReplaceAll(value, from, to)
		}
		return value
	}
	for i := range bak.ReviewSources.Pages {
		page := &bak.ReviewSources.Pages[i]
		page.AgentName = bak.AgentName
		if mapped := assets[page.PageAssetID]; mapped != "" {
			page.PageAssetID = mapped
		} else {
			return fmt.Errorf("reviewed page asset mapping is missing")
		}
	}
	members := map[string]k12.ReviewSourceGroupMember{}
	for i := range bak.ReviewSources.Snapshots {
		snapshot := &bak.ReviewSources.Snapshots[i]
		key := reviewMemberArchiveKey(snapshot.Member)
		snapshot.Source.AgentName = bak.AgentName
		for _, id := range []string{snapshot.Source.PageAssetID, snapshot.Source.ParentPageAssetID} {
			if id != "" && strings.HasPrefix(id, "asset://") && assets[id] == "" {
				return fmt.Errorf("reviewed source archive asset mapping is missing")
			}
		}
		snapshot.Source.PageAssetID = rewrite(snapshot.Source.PageAssetID)
		snapshot.Source.ParentPageAssetID = rewrite(snapshot.Source.ParentPageAssetID)
		snapshot.Source.ParentStemRaw = rewrite(snapshot.Source.ParentStemRaw)
		snapshot.Source.ResultJSON = rewrite(snapshot.Source.ResultJSON)
		snapshot.Source.ResultDigest = modelInvocationDigest([]byte(snapshot.Source.ResultJSON))
		snapshot.Member.ResultDigest = snapshot.Source.ResultDigest
		// 只有迁移前仍匹配原题的摘要才随资产归属重写，失效关联不能被当前题面重新签认。
		if digest := questions[snapshot.Member.RecordID]; digest != "" && originalQuestions[snapshot.Member.RecordID] == snapshot.Member.QuestionDigest {
			snapshot.Member.QuestionDigest = digest
		} else {
			snapshot.Active = false
		}
		members[key] = snapshot.Member
	}
	for i := range bak.ReviewSources.Groups {
		group := &bak.ReviewSources.Groups[i]
		group.AgentName, group.GroupID, group.CanonicalStatementDigest = bak.AgentName, "", ""
		group.CanonicalStatement = rewrite(group.CanonicalStatement)
		for j, member := range group.Members {
			if migrated, found := members[reviewMemberArchiveKey(member)]; found {
				group.Members[j] = migrated
			} else if digest := questions[member.RecordID]; digest != "" && originalQuestions[member.RecordID] == member.QuestionDigest {
				group.Members[j].QuestionDigest = digest
			}
		}
		normalized, err := group.Normalized()
		if err != nil {
			return err
		}
		*group = normalized
	}
	return nil
}
