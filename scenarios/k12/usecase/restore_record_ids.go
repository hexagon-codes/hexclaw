package usecase

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// RestoreRecordIDs 分记录集保存实际发生变更的归档根 ID。未发生冲突的历史记录继续使用原 ID。
type RestoreRecordIDs map[string]map[string]string

func (ids RestoreRecordIDs) resolve(collection, id string) string {
	if target, ok := ids[collection][id]; ok {
		return target
	}
	return id
}

func (ids RestoreRecordIDs) relinkLearningRecords(archive *Hexbak) error {
	if len(ids) == 0 {
		return nil
	}
	type identity struct{ collection, id string }
	claimed := make(map[identity]string, len(archive.Records))
	for _, rec := range archive.Records {
		if rec == nil {
			return fmt.Errorf("%w: empty archive record", ErrInvalidInput)
		}
		old := rec.RecordID
		rec.RecordID = ids.resolve(rec.Collection, old)
		target := identity{rec.Collection, rec.RecordID}
		if previous, exists := claimed[target]; exists && previous != old {
			return fmt.Errorf("%w: two archived records claim the same target identity", records.ErrVersionConflict)
		}
		claimed[target] = old
		if old != rec.RecordID && strings.HasSuffix(rec.DedupeKey, "#released#"+old) {
			rec.DedupeKey = strings.TrimSuffix(rec.DedupeKey, old) + rec.RecordID
		}
		if rec.Collection == k12.CollectionPracticeSet {
			var set k12.PracticeSetFields
			if err := json.Unmarshal([]byte(rec.Fields), &set); err != nil {
				return err
			}
			changed := false
			for i := range set.Items {
				item := &set.Items[i]
				if replacement := ids.practiceSource(item.SourceProblemID); replacement != item.SourceProblemID {
					item.SourceProblemID = replacement
					changed = true
				}
				changed = ids.relinkEvidence(item.LearningEvidence) || changed
			}
			for i := range set.ReturnAssets {
				ret := &set.ReturnAssets[i]
				changed = ids.relink(k12.CollectionGradingJob, &ret.RegradeJobID) || changed
				changed = ids.relinkEvidence(ret.LearningEvidence) || changed
				for id, basis := range ret.LearningEvidenceBasis {
					if replacement := ids.practiceSource(id); replacement != id {
						delete(ret.LearningEvidenceBasis, id)
						ret.LearningEvidenceBasis[replacement] = basis
						changed = true
					}
				}
			}
			if changed {
				raw, err := json.Marshal(set)
				if err != nil {
					return err
				}
				rec.Fields = string(raw)
			}
		}
	}
	for id, schedule := range archive.ReviewSchedules {
		if replacement := ids.resolve(k12.CollectionMistakes, id); id != replacement {
			delete(archive.ReviewSchedules, id)
			archive.ReviewSchedules[replacement] = schedule
		}
	}
	for i := range archive.CurrentAccumulations {
		entry := &archive.CurrentAccumulations[i]
		if ids.relink(k12.CollectionAccumulation, &entry.AccumulationID) {
			if err := relinkArchiveDeleteReceipt(&entry.DeleteReceiptJSON, entry.AccumulationID); err != nil {
				return err
			}
		}
	}
	for i := range archive.CurrentCreativeWorks {
		work := &archive.CurrentCreativeWorks[i]
		old := work.WorkID
		if !ids.relink(k12.CollectionCreativeWork, &work.WorkID) {
			continue
		}
		if work.DeletedBy == work.AgentName {
			work.DeletedBy = archive.AgentName
		}
		if err := relinkArchiveDeleteReceipt(&work.DeleteReceiptJSON, work.WorkID); err != nil {
			return err
		}
		for j := range work.Invocations {
			inv := &work.Invocations[j]
			if rest, found := strings.CutPrefix(inv.OperationKey, "work:"+old+":version:"); found {
				inv.OperationKey = "work:" + work.WorkID + ":version:" + rest
			}
			inv.WorkRecordID = work.WorkID
		}
	}
	for i := range archive.WeeklyHistory {
		week := &archive.WeeklyHistory[i]
		ids.relinkWeeklyTracks(week.Plan.Tracks)
		ids.relinkWeeklyTracks(week.Snapshot.Tracks)
		for j := range week.Attempts {
			ids.relink(k12.CollectionMistakes, &week.Attempts[j].Attempt.MistakeRecordID)
		}
	}
	return nil
}

func (ids RestoreRecordIDs) relink(collection string, id *string) bool {
	replacement := ids.resolve(collection, *id)
	if replacement == *id {
		return false
	}
	*id = replacement
	return true
}

// 练习来源可指向错题或积累；到期队列允许两类记录共存于同一张练习卷。
func (ids RestoreRecordIDs) practiceSource(id string) string {
	if target, ok := ids[k12.CollectionMistakes][id]; ok {
		return target
	}
	return ids.resolve(k12.CollectionAccumulation, id)
}

func (ids RestoreRecordIDs) relinkEvidence(evidence []k12.LearningEvidence) bool {
	changed := false
	for i := range evidence {
		changed = ids.relink(k12.CollectionMistakes, &evidence[i].BasisID) || changed
	}
	return changed
}

func (ids RestoreRecordIDs) relinkWeeklyTracks(tracks []k12.WeeklyPracticeTrack) {
	for i := range tracks {
		for j := range tracks[i].Items {
			item := &tracks[i].Items[j]
			if item.SourceKind == "mistake" {
				ids.relink(k12.CollectionMistakes, &item.SourceRef)
			}
		}
	}
}

func relinkArchiveDeleteReceipt(raw *string, target string) error {
	if *raw == "" {
		return nil
	}
	var receipt k12.CurrentDeleteReceipt
	if err := json.Unmarshal([]byte(*raw), &receipt); err != nil {
		return err
	}
	receipt.ObjectID = target
	encoded, err := json.Marshal(receipt)
	if err == nil {
		*raw = string(encoded)
	}
	return err
}
