package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ProfileEditChange 只描述用户文本中的明确增删片段，未涉及的源事实原样保留。
type ProfileEditChange struct {
	ID      string `json:"id"`
	Before  string `json:"before"`
	OldSpan string `json:"old_span"`
	NewSpan string `json:"new_span"`
	Type    string `json:"type,omitempty"`
}
type ProfileEditPlan struct {
	Ambiguous bool                `json:"ambiguous"`
	Changes   []ProfileEditChange `json:"changes"`
}
type ProfileEditor interface {
	PlanProfileEdit(context.Context, []MemoryEntry, string, string) (ProfileEditPlan, error)
}

// EditProfile 在同一个文件原子提交源修正与摘要；失败保留调用方草稿。
func (fm *FileMemory) EditProfile(ctx context.Context, revision, content string, editor ProfileEditor) error {
	if editor == nil || strings.TrimSpace(revision) == "" || strings.TrimSpace(content) == "" {
		return errors.New("Profile revision and content are required")
	}
	fm.profileRunMu.Lock()
	defer fm.profileRunMu.Unlock()
	path := fm.profileReceiptPath("edit-v1:" + revision + ":" + content)
	receipt, err := readProfileReceipt(path)
	if err != nil {
		return err
	}
	if receipt.State == "applied" {
		return nil
	}
	fm.mu.RLock()
	snapshot := fm.profileSnapshotUnlocked("", time.Now())
	operation := fm.readEntryMetaUnlocked(snapshot.Profile.ID).ProfileOperation
	fm.mu.RUnlock()
	if operation == profileHash([]string{revision, content}) {
		receipt.State = "applied"
		return saveProfileReceipt(path, receipt)
	}
	if snapshot.Profile.ProfileRevision != revision || snapshot.Profile.ProfileDigest != snapshot.Digest {
		return ErrProfileStale
	}
	if strings.TrimSpace(content) == snapshot.Profile.Content {
		return nil
	}
	switch receipt.State {
	case "running", "unknown":
		return ErrProfileOutcomeUnknown
	case "ready":
	default:
		receipt = profileReceipt{State: "running", Digest: snapshot.Digest}
		if err := saveProfileReceipt(path, receipt); err != nil {
			return err
		}
		plan, callErr := editor.PlanProfileEdit(ctx, snapshot.Entries, snapshot.Profile.Content, content)
		if callErr != nil {
			receipt.State = "unknown"
			if errors.Is(callErr, ErrProfileRejected) {
				receipt.State = "rejected"
			}
			if err := saveProfileReceipt(path, receipt); err != nil {
				return err
			}
			if receipt.State == "rejected" {
				return callErr
			}
			return fmt.Errorf("%w: %v", ErrProfileOutcomeUnknown, callErr)
		}
		receipt.State, receipt.Edit = "ready", &plan
		if err := saveProfileReceipt(path, receipt); err != nil {
			return err
		}
	}
	if receipt.Edit == nil || receipt.Edit.Ambiguous || (len(receipt.Edit.Changes) == 0 && flattenProfile(content) != flattenProfile(snapshot.Profile.Content)) {
		return errors.New("Profile changes cannot be reliably linked to source memories; the draft has not been saved")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	latest := fm.profileSnapshotUnlocked("", time.Now())
	if latest.Profile.ProfileRevision != revision || latest.Digest != receipt.Digest {
		return ErrProfileStale
	}
	if err := fm.applyProfileEditUnlocked(latest, content, *receipt.Edit); err != nil {
		return err
	}
	receipt.State = "applied"
	return saveProfileReceipt(path, receipt)
}

func (fm *FileMemory) applyProfileEditUnlocked(snapshot profileSnapshot, content string, plan ProfileEditPlan) error {
	dir := fm.roleDir("")
	path := filepath.Join(dir, memoryActiveFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	type replacement struct {
		start, end int
		text       string
	}
	var patches []replacement
	var added []string
	entries := append([]MemoryEntry(nil), snapshot.Entries...)
	seen := map[string]bool{}
	for _, change := range plan.Changes {
		if change.ID == "" {
			if change.OldSpan != "" || strings.TrimSpace(change.NewSpan) == "" || !strings.Contains(content, change.NewSpan) || strings.Contains(snapshot.Profile.Content, change.NewSpan) {
				return errors.New("New memory must be an explicit addition to the profile")
			}
			switch change.Type {
			case "identity", "preference", "fact", "context":
			default:
				return errors.New("Invalid memory type")
			}
			id := newStableID()
			meta := EntryMeta{ID: id, ManualCorrection: true}
			added = append(added, fmt.Sprintf("- [%s] [%s:manual] %s %s", time.Now().Format("15:04"), change.Type, change.NewSpan, meta.serialize()))
			entries = append(entries, MemoryEntry{ID: id, Content: change.NewSpan, Type: change.Type, Source: "manual", ManualCorrection: true})
			continue
		}
		if seen[change.ID] {
			return errors.New("A source memory was changed more than once")
		}
		seen[change.ID] = true
		index := -1
		for i := range entries {
			if entries[i].ID == change.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return errors.New("Profile edit references an unrelated source memory")
		}
		entry := entries[index]
		// 精确引文约束：不允许模型按相似度猜测删除整个源条目。
		if entry.Content != change.Before || change.OldSpan == "" || strings.Count(entry.Content, change.OldSpan) != 1 || !strings.Contains(snapshot.Profile.Content, change.OldSpan) || strings.Contains(content, change.OldSpan) || (change.NewSpan != "" && !strings.Contains(content, change.NewSpan)) {
			return errors.New("Profile correction is ambiguous; source memories were not changed")
		}
		next := strings.TrimSpace(strings.Replace(entry.Content, change.OldSpan, change.NewSpan, 1))
		sourceDir, filename, line := fm.resolveEntryLocationUnlocked(entry.ID)
		if sourceDir != dir || filename != memoryActiveFile {
			return errors.New("Profile source is outside the active memory file")
		}
		start, end := findBlockByStartLine(string(raw), line)
		if start < 0 {
			return ErrProfileStale
		}
		text := ""
		if next != "" {
			meta := fm.readEntryMetaUnlocked(entry.ID)
			meta.ManualCorrection = true
			text = rebuildEntryLine(strings.TrimSpace(lines[start]), next+" "+meta.serialize())
			entries[index].Content, entries[index].ManualCorrection = next, true
		} else {
			entries = append(entries[:index:index], entries[index+1:]...)
		}
		patches = append(patches, replacement{start, end, text})
	}
	// 每一处事实变化都必须有逐字来源，防止模型遗漏修正后只保存表面画像。
	beforeRemainder, afterRemainder := snapshot.Profile.Content, content
	removed, addedSpans := map[string]bool{}, map[string]bool{}
	for _, change := range plan.Changes {
		if change.OldSpan != "" && !removed[change.OldSpan] {
			beforeRemainder = strings.Replace(beforeRemainder, change.OldSpan, "", 1)
			removed[change.OldSpan] = true
		}
		if change.NewSpan != "" && !addedSpans[change.NewSpan] {
			afterRemainder = strings.Replace(afterRemainder, change.NewSpan, "", 1)
			addedSpans[change.NewSpan] = true
		}
	}
	normalize := func(text string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return r
		}, text)
	}
	if normalize(beforeRemainder) != normalize(afterRemainder) {
		return errors.New("Profile changes were not fully linked to source memories; the draft has not been saved")
	}
	digest := profileEntriesDigest(entries)
	profileDir, profileFile, profileLine := fm.resolveEntryLocationUnlocked(snapshot.Profile.ID)
	if profileDir != dir || profileFile != memoryActiveFile {
		return ErrProfileStale
	}
	start, end := findBlockByStartLine(string(raw), profileLine)
	if start < 0 {
		return ErrProfileStale
	}
	meta := fm.readEntryMetaUnlocked(snapshot.Profile.ID)
	meta.ProfileDigest, meta.Pinned, meta.Subject = digest, true, ProfileSubject
	meta.ValidFrom = time.Now().UTC().Format(metaTimeFormat)
	meta.ProfileOperation = profileHash([]string{snapshot.Profile.ProfileRevision, content})
	patches = append(patches, replacement{start, end, rebuildEntryLine(strings.TrimSpace(lines[start]), strings.TrimSpace(content)+" "+meta.serialize())})
	sort.Slice(patches, func(i, j int) bool { return patches[i].start > patches[j].start })
	for _, patch := range patches {
		var replacementLines []string
		if patch.text != "" {
			replacementLines = strings.Split(patch.text, "\n")
		}
		lines = append(append(lines[:patch.start:patch.start], replacementLines...), lines[patch.end:]...)
	}
	lines = append(lines, added...)
	return atomicWriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}
