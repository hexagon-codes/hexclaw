package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrProfileRejected = errors.New("Profile request was rejected before generation")
var ErrProfileStale = errors.New("Memory changed while the profile was updating; refresh the current version")
var ErrProfileOutcomeUnknown = errors.New("Profile request outcome is unknown; the original request will not be resent")

type profileSnapshot struct {
	Entries []MemoryEntry
	Profile MemoryEntry
	Digest  string
}

func profileHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isProfileEntry(entry MemoryEntry) bool {
	return entry.Subject == ProfileSubject || entry.Source == "reflect_profile"
}

// 来源版本排除命中次数与文件行号，召回统计和移行不改变事实版本。
func (fm *FileMemory) profileSnapshotUnlocked(role string, now time.Time) profileSnapshot {
	var snapshot profileSnapshot
	for _, entry := range fm.parseEntriesForRoleUnlocked(role) {
		if isProfileEntry(entry) || entry.Status == MemoryStatusArchived || !entryValidAt(entry, now) || isPersonSubject(entry.Subject) {
			continue
		}
		switch entry.Type {
		case "identity", "preference", "fact", "context":
		default:
			continue
		}
		if strings.TrimSpace(entry.Content) == "" {
			continue
		}
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	snapshot.Digest = profileEntriesDigest(snapshot.Entries)
	for _, entry := range fm.parseEntriesFromDirUnlocked(fm.roleDir(role)) {
		if isProfileEntry(entry) {
			snapshot.Profile = entry
			break
		}
	}
	snapshot.Profile.ProfileRevision = profileHash([]string{snapshot.Digest, snapshot.Profile.ID, snapshot.Profile.Content})
	return snapshot
}

func profileEntriesDigest(entries []MemoryEntry) string {
	var versions []string
	for _, entry := range entries {
		id := entry.ID
		if looksLikeLineID(id) {
			id = ""
		}
		versions = append(versions, profileHash([]any{id, entry.Type, entry.Source, entry.Subject, entry.Content, entry.Pinned, entry.ValidFrom, entry.ValidTo, entry.ManualCorrection}))
	}
	sort.Strings(versions)
	return profileHash(versions)
}

func (fm *FileMemory) requestProfileRefresh() {
	select {
	case fm.profileWake <- struct{}{}:
	default:
	}
}

func (fm *FileMemory) roleForDir(dir string) string {
	if dir == fm.roleDir("") {
		return ""
	}
	return filepath.Base(dir)
}

// 旧画像保留在文件内用于追溯，但不再作为当前事实展示或注入模型。
func (fm *FileMemory) currentProfilesUnlocked(dir string, entries []MemoryEntry) []MemoryEntry {
	var out []MemoryEntry
	var snapshot *profileSnapshot
	for _, entry := range entries {
		if !isProfileEntry(entry) {
			out = append(out, entry)
			continue
		}
		if snapshot == nil {
			s := fm.profileSnapshotUnlocked(fm.roleForDir(dir), time.Now())
			snapshot = &s
		}
		if entry.ProfileDigest != snapshot.Digest || len(snapshot.Entries) == 0 {
			fm.requestProfileRefresh()
			continue
		}
		entry.ProfileRevision = profileHash([]string{snapshot.Digest, entry.ID, entry.Content})
		out = append(out, entry)
	}
	return out
}

// 保留行号以便搜索引用和稳定 ID 定位；仅清空过期画像块的读取投影。
func (fm *FileMemory) currentMemoryTextUnlocked(dir string) string {
	data, _ := os.ReadFile(filepath.Join(dir, memoryActiveFile))
	raw := string(data)
	entries := fm.parseEntriesFromDirUnlocked(dir)
	snapshot := fm.profileSnapshotUnlocked(fm.roleForDir(dir), time.Now())
	lines := strings.Split(raw, "\n")
	for _, entry := range entries {
		if !isProfileEntry(entry) || (entry.ProfileDigest == snapshot.Digest && len(snapshot.Entries) > 0) {
			continue
		}
		start, end := findBlockByStartLine(raw, entry.lineIdx)
		if start < 0 {
			continue
		}
		for line := start; line < end; line++ {
			lines[line] = ""
		}
		fm.requestProfileRefresh()
	}
	return strings.Join(lines, "\n")
}

type profileReceipt struct {
	State  string           `json:"state"`
	Digest string           `json:"digest"`
	Output string           `json:"output,omitempty"`
	Edit   *ProfileEditPlan `json:"edit,omitempty"`
}

func (fm *FileMemory) profileReceiptPath(key string) string {
	return filepath.Join(fm.dir, ".profile-operations", profileHash(key)+".json")
}

func readProfileReceipt(path string) (profileReceipt, error) {
	var receipt profileReceipt
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return receipt, nil
	}
	if err != nil {
		return receipt, err
	}
	err = json.Unmarshal(data, &receipt)
	return receipt, err
}

func saveProfileReceipt(path string, receipt profileReceipt) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0600)
}

func (fm *FileMemory) distillCurrentProfile(ctx context.Context, role string, syn ProfileSynthesizer, cfg DistillProfileConfig, now time.Time) (string, error) {
	fm.mu.RLock()
	snapshot := fm.profileSnapshotUnlocked(role, now)
	fm.mu.RUnlock()
	if len(snapshot.Entries) < cfg.minFacts() {
		return "skip", nil
	}
	if snapshot.Profile.ProfileDigest == snapshot.Digest {
		return "skip", nil
	}
	path := fm.profileReceiptPath("synthesis-v1:" + role + ":" + snapshot.Digest)
	receipt, err := readProfileReceipt(path)
	if err != nil {
		return "skip", err
	}
	if receipt.State == "rejected" && !cfg.RetryRejected {
		return "skip", ErrProfileRejected
	}
	switch receipt.State {
	case "running", "unknown":
		return "skip", ErrProfileOutcomeUnknown
	case "ready", "applied":
	default:
		receipt = profileReceipt{State: "running", Digest: snapshot.Digest}
		if err = saveProfileReceipt(path, receipt); err != nil {
			return "skip", err
		}
		facts := make([]string, 0, len(snapshot.Entries))
		for _, entry := range snapshot.Entries {
			fact := entry.Content
			if entry.ManualCorrection || entry.Source == "manual" {
				fact = "[Explicit user fact; authoritative] " + fact
			}
			facts = append(facts, fact)
		}
		out, callErr := syn.Synthesize(ctx, facts, "")
		if callErr != nil {
			receipt.State = "unknown"
			if errors.Is(callErr, ErrProfileRejected) {
				receipt.State = "rejected"
			}
			if err := saveProfileReceipt(path, receipt); err != nil {
				return "skip", fmt.Errorf("%w: %v", ErrProfileOutcomeUnknown, err)
			}
			if receipt.State == "rejected" {
				return "skip", callErr
			}
			return "skip", fmt.Errorf("%w: %v", ErrProfileOutcomeUnknown, callErr)
		}
		receipt.Output = flattenProfile(out)
		receipt.State = "ready"
		if err := saveProfileReceipt(path, receipt); err != nil {
			return "skip", err
		}
	}
	if receipt.Output == "" || !isUsableSynthesis(receipt.Output) {
		return "skip", errors.New("Profile generation returned no usable summary")
	}
	if r := []rune(receipt.Output); len(r) > cfg.maxRunes() {
		receipt.Output = strings.TrimSpace(string(r[:cfg.maxRunes()]))
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.profileSnapshotUnlocked(role, time.Now()).Digest != snapshot.Digest {
		return "skip", ErrProfileStale
	}
	action, err := fm.upsertProfileUnlocked(receipt.Output, role, snapshot.Digest)
	if err != nil {
		return "skip", err
	}
	receipt.State = "applied"
	if err := saveProfileReceipt(path, receipt); err != nil {
		return action, err
	}
	return action, nil
}
