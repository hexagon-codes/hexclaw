package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// eventEntryReceipt 独立于可编辑记忆保存投递回执，删除正文不撤销已完成的事件。
type eventEntryReceipt struct {
	Digest     string `json:"digest"`
	EntryID    string `json:"entry_id"`
	BeforeHash string `json:"before_hash"`
	Applied    bool   `json:"applied"`
}

func memoryEventHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SaveStructuredEvent 按事件身份写入一条记忆。同文不同事件不合并；重放不覆盖人工修改。
// 文件替换前保存准备回执，替换后确认；重启使用稳定条目身份补齐确认。
func (fm *FileMemory) SaveStructuredEvent(eventID, content, memType, source, role string, meta EntryMeta) error {
	if strings.TrimSpace(eventID) == "" {
		return errors.New("memory event ID is required")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("memory event content is required")
	}
	if memType == "" {
		memType = "fact"
	}
	if source == "" {
		source = "manual"
	}
	key := memoryEventHash([]byte(eventID))
	meta.ID = "event-" + key
	input, err := json.Marshal([]any{content, memType, source, role, meta})
	if err != nil {
		return err
	}
	digest := memoryEventHash(input)
	targetDir := fm.roleDir(role)
	if isGlobalMemType(memType) || meta.Pinned {
		targetDir = fm.roleDir("")
	}
	path := filepath.Join(targetDir, memoryActiveFile)
	receiptPath := filepath.Join(fm.dir, ".event-receipts", key+".json")

	fm.mu.Lock()
	defer fm.mu.Unlock()
	var receipt eventEntryReceipt
	data, err := os.ReadFile(receiptPath)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return fmt.Errorf("read memory event receipt: %w", err)
	}
	if !fresh {
		if err := json.Unmarshal(data, &receipt); err != nil {
			return fmt.Errorf("decode memory event receipt: %w", err)
		}
		if receipt.Digest != digest || receipt.EntryID != meta.ID {
			return errors.New("memory event identity conflicts with its saved input")
		}
		if receipt.Applied {
			return nil
		}
		if _, _, line := fm.scanStableIDUnlocked(meta.ID); line >= 0 {
			receipt.Applied = true
			return saveEventEntryReceipt(receiptPath, receipt)
		}
	} else {
		fm.evictIfNeededUnlocked(targetDir)
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read memory event destination: %w", err)
	}
	beforeHash := memoryEventHash(raw)
	if fresh {
		receipt = eventEntryReceipt{Digest: digest, EntryID: meta.ID, BeforeHash: beforeHash}
		if err := saveEventEntryReceipt(receiptPath, receipt); err != nil {
			return err
		}
	} else if receipt.BeforeHash != beforeHash {
		// 准备后发生其他变更且条目已不存在，不能猜测是未写入还是已被删除。
		return errors.New("memory event destination changed before its write could be confirmed")
	}
	// 沿用结构化写入的既有内容规则，不扩展到其他业务。
	if !LooksSensitive(content) {
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return err
		}
		entry := fmt.Sprintf("\n- [%s] [%s:%s] %s %s\n", time.Now().Format("15:04"), memType, source, content, meta.serialize())
		if err := atomicWriteFile(path, append(raw, []byte(entry)...), 0644); err != nil {
			return fmt.Errorf("write memory event: %w", err)
		}
		fm.requestProfileRefresh()
	}
	receipt.Applied = true
	return saveEventEntryReceipt(receiptPath, receipt)
}

func saveEventEntryReceipt(path string, receipt eventEntryReceipt) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0600)
}

// confirmMemoryEventsUnlocked 在删除或启动前确认已经落盘的事件，避免旧事件恢复已删正文。
func (fm *FileMemory) confirmMemoryEventsUnlocked() error {
	dir := filepath.Join(fm.dir, ".event-receipts")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var receipt eventEntryReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return err
		}
		if receipt.Applied {
			continue
		}
		if _, _, line := fm.scanStableIDUnlocked(receipt.EntryID); line >= 0 {
			receipt.Applied = true
			if err := saveEventEntryReceipt(path, receipt); err != nil {
				return err
			}
		}
	}
	return nil
}
