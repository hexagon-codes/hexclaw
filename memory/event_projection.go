package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// eventEntryProjection 保存同一事实来源的有效修订；只存摘要，不复制正文。
type eventEntryProjection struct {
	Stamp       string                `json:"stamp"`
	Revision    int                   `json:"revision"`
	Digest      string                `json:"digest"`
	ContentHash string                `json:"content_hash"`
	Removed     bool                  `json:"removed"`
	Suppressed  bool                  `json:"suppressed"`
	Pending     *eventProjectionWrite `json:"pending,omitempty"`
}

type eventProjectionWrite struct {
	Path       string `json:"path"`
	BeforeHash string `json:"before_hash"`
	AfterHash  string `json:"after_hash"`
}

// detachEventProjectionUnlocked 把人工修改的条目交还用户，后续自动纠正不覆盖它。
func (fm *FileMemory) detachEventProjectionUnlocked(id string) error {
	if !strings.HasPrefix(id, "event-") {
		return nil
	}
	key := strings.TrimPrefix(id, "event-")
	if len(key) != 64 || strings.ContainsAny(key, "/\\") {
		return nil
	}
	path := filepath.Join(fm.dir, ".event-receipts", key+".json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt eventEntryReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return err
	}
	if receipt.Projection == nil {
		return nil
	}
	receipt.Projection.Suppressed, receipt.Projection.Pending = true, nil
	return saveEventEntryReceipt(path, receipt)
}

// confirmEventProjectionUnlocked 只确认可证明已经完成的原子文件替换。
func (fm *FileMemory) confirmEventProjectionUnlocked(path string, receipt *eventEntryReceipt) error {
	p := receipt.Projection
	if p == nil || p.Pending == nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(fm.dir, p.Pending.Path))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	switch memoryEventHash(raw) {
	case p.Pending.AfterHash:
		p.Pending = nil
		return saveEventEntryReceipt(path, *receipt)
	case p.Pending.BeforeHash:
		return nil // 未写入，保留原请求等待消费者重放。
	default:
		return errors.New("memory projection changed before its write could be confirmed")
	}
}

// ReviseStructuredEvent 更新或撤回同一来源的派生摘要。空正文表示撤回。
// 修订单调递增，人工编辑、归档、删除的条目不由自动纠正覆盖或恢复。
func (fm *FileMemory) ReviseStructuredEvent(eventID string, revision int, content, memType, source, role string, meta EntryMeta) error {
	if strings.TrimSpace(eventID) == "" || revision < 1 {
		return errors.New("memory projection source and positive revision are required")
	}
	content = strings.TrimSpace(content)
	key := memoryEventHash([]byte(eventID))
	meta.ID = "event-" + key
	input, err := json.Marshal([]any{content, memType, source, role, meta})
	if err != nil {
		return err
	}
	digest := memoryEventHash(input)
	receiptPath := filepath.Join(fm.dir, ".event-receipts", key+".json")
	fm.mu.Lock()
	defer fm.mu.Unlock()
	var receipt eventEntryReceipt
	rawReceipt, err := os.ReadFile(receiptPath)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return err
	}
	if !fresh {
		if err = json.Unmarshal(rawReceipt, &receipt); err != nil {
			return err
		}
		if receipt.EntryID != meta.ID {
			return errors.New("memory projection source conflicts with its saved identity")
		}
		if err = fm.confirmEventProjectionUnlocked(receiptPath, &receipt); err != nil {
			return err
		}
	} else {
		receipt.EntryID = meta.ID
	}
	previous := receipt.Projection
	supersedingPrepared := false
	if previous != nil {
		if revision < previous.Revision {
			return nil
		}
		// 确认后仍有 pending，说明文件保持写入前状态；新修订可直接替代尚未发生的写入。
		if revision > previous.Revision && previous.Pending != nil {
			supersedingPrepared = true
			previous.Pending = nil
		}
		if revision == previous.Revision {
			if digest != previous.Digest {
				return errors.New("memory projection revision conflicts with its saved input")
			}
			if previous.Pending == nil {
				return nil
			}
		}
	}

	dir, filename, line := fm.scanStableIDUnlocked(meta.ID)
	targetDir := fm.roleDir(role)
	if isGlobalMemType(memType) || meta.Pinned {
		targetDir = fm.roleDir("")
	}
	path := filepath.Join(targetDir, memoryActiveFile)
	var oldContent string
	var oldMeta EntryMeta
	if line >= 0 {
		path = filepath.Join(dir, filename)
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, block := range splitEntryBlocks(string(raw)) {
			body, m := splitEntryMeta(stripEntryPrefix(block.text))
			if m.ID == meta.ID {
				oldContent, oldMeta = strings.TrimSpace(body), m
				break
			}
		}
	}
	p := &eventEntryProjection{Stamp: time.Now().Format("15:04"), Revision: revision, Digest: digest, ContentHash: memoryEventHash([]byte(content)), Removed: content == ""}
	if previous != nil && previous.Pending != nil {
		p = previous
	} else {
		// 旧回执通过其原始输入摘要识别正文；命中次数不是人工内容变更。
		managed := fresh || supersedingPrepared || (previous != nil && previous.Removed)
		if line >= 0 && !supersedingPrepared {
			if previous != nil {
				managed = memoryEventHash([]byte(oldContent)) == previous.ContentHash
			} else {
				initialMeta := oldMeta
				initialMeta.HitCount = 0
				initial, _ := json.Marshal([]any{oldContent, memType, source, role, initialMeta})
				managed = memoryEventHash(initial) == receipt.Digest
			}
		}
		p.Suppressed = !managed || (previous != nil && previous.Suppressed) || (line >= 0 && filename != memoryActiveFile)
		if p.Suppressed {
			receipt.Projection, receipt.Applied = p, true
			return saveEventEntryReceipt(receiptPath, receipt)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated := raw
	if line >= 0 {
		lines := strings.Split(string(raw), "\n")
		start, end := findBlockByStartLine(string(raw), line)
		if start < 0 {
			return errors.New("memory projection entry could not be located")
		}
		if content == "" {
			lines = removeLineRange(lines, start, end)
		} else {
			oldMeta.Subject = meta.Subject
			entry := rebuildEntryLine(strings.TrimSpace(lines[start]), content+" "+oldMeta.serialize())
			lines = append(append(lines[:start:start], entry), lines[end:]...)
		}
		updated = []byte(strings.Join(lines, "\n"))
	} else if content != "" {
		entry := fmt.Sprintf("\n- [%s] [%s:%s] %s %s\n", p.Stamp, memType, source, content, meta.serialize())
		updated = append(append([]byte(nil), raw...), []byte(entry)...)
	}
	if p.Pending == nil {
		relative, err := filepath.Rel(fm.dir, path)
		if err != nil {
			return err
		}
		p.Pending = &eventProjectionWrite{Path: relative, BeforeHash: memoryEventHash(raw), AfterHash: memoryEventHash(updated)}
	} else if p.Pending.AfterHash != memoryEventHash(updated) {
		return errors.New("memory projection replay no longer matches its prepared write")
	}
	receipt.Projection, receipt.Applied = p, true
	if err = saveEventEntryReceipt(receiptPath, receipt); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err = atomicWriteFile(path, updated, 0644); err != nil {
		return err
	}
	fm.requestProfileRefresh()
	p.Pending = nil
	return saveEventEntryReceipt(receiptPath, receipt)
}
