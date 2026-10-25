package marketplace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 发布记录只描述一次目录交换；目录名确定暂存与备份位置，不维护另一份启用状态。
type bundleInstallRecord struct {
	SkillID string `json:"skill_id"`
	Digest  string `json:"digest"`
}

func writeBundleInstallRecord(stage string, manifest BundleManifest) error {
	digest, err := manifest.SemanticDigest()
	if err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(stage, ".install.pending"))
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(bundleInstallRecord{SkillID: manifest.SkillID, Digest: digest}); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// 完整记录先原子发布，之后才允许移动当前可用目录。
	return os.Rename(file.Name(), stage+".pending")
}

func (m *Marketplace) recoverBundleInstalls() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoverBundleInstallsLocked()
}

func (m *Marketplace) recoverBundleInstallsLocked() error {
	entries, err := os.ReadDir(m.skillDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".hexclaw-bundle-") && strings.HasSuffix(entry.Name(), ".pending") {
			if err := m.recoverBundleInstallLocked(filepath.Join(m.skillDir, entry.Name())); err != nil {
				return fmt.Errorf("recover skill bundle installation: %w", err)
			}
		}
	}
	// 兼容未写发布记录的旧安装窗口：从完整备份的清单确定原目录，先恢复再执行 seed。
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".hexclaw-bundle-") || !strings.HasSuffix(entry.Name(), ".previous") {
			continue
		}
		backup := filepath.Join(m.skillDir, entry.Name())
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			continue
		}
		previous, err := LoadSkillFromFile(filepath.Join(backup, "SKILL.md"))
		if err != nil {
			return fmt.Errorf("recover previous skill bundle: %w", err)
		}
		if !bundleInstallName(previous.Name()) {
			return fmt.Errorf("invalid previous skill bundle identity")
		}
		if _, err := os.Stat(filepath.Join(backup, "bundle.json")); err == nil {
			if _, err := ReadBundleManifest(backup); err != nil {
				return fmt.Errorf("recover previous skill bundle: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		destination := filepath.Join(m.skillDir, previous.Name())
		if _, err := os.Lstat(destination); os.IsNotExist(err) {
			if err := os.Rename(backup, destination); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if _, err := ReadBundleManifest(destination); err != nil {
				return fmt.Errorf("recover published skill bundle: %w", err)
			}
			if err := os.Remove(filepath.Join(m.skillDir, previous.Name()+".md")); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.RemoveAll(backup); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(strings.TrimSuffix(backup, ".previous")); err != nil {
			return err
		}
	}
	return nil
}

func bundleInstallName(name string) bool {
	return name != "" && name != "." && filepath.Base(name) == name && !strings.Contains(name, "..")
}

func (m *Marketplace) recoverBundleInstallLocked(recordPath string) error {
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return err
	}
	var record bundleInstallRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if !bundleInstallName(record.SkillID) || record.Digest == "" {
		return fmt.Errorf("invalid skill bundle publication record")
	}
	stage := strings.TrimSuffix(recordPath, ".pending")
	backup := stage + ".previous"
	destination := filepath.Join(m.skillDir, record.SkillID)
	_, destinationErr := os.Lstat(destination)
	if os.IsNotExist(destinationErr) {
		if _, err := os.Lstat(backup); err == nil {
			if err := os.Rename(backup, destination); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if _, err := os.Lstat(stage); err != nil {
			return fmt.Errorf("skill bundle publication has no recoverable directory: %w", err)
		}
	} else if destinationErr != nil {
		return destinationErr
	} else if _, err := os.Lstat(stage); os.IsNotExist(err) {
		// 暂存目录已移动，第二次 rename 是提交点；核对完整版本后收尾。
		manifest, err := ReadBundleManifest(destination)
		if err != nil {
			return err
		}
		digest, err := manifest.SemanticDigest()
		if err != nil {
			return err
		}
		if manifest.SkillID != record.SkillID || digest != record.Digest {
			return fmt.Errorf("published skill bundle does not match its publication record")
		}
		if err := os.Remove(filepath.Join(m.skillDir, record.SkillID+".md")); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := m.clearUninstalledLocked(record.SkillID); err != nil {
			return err
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// 回滚时先移除记录，再丢弃暂存目录，恢复过程本身中断也不会误认旧版为新提交。
	if err := os.Remove(recordPath); err != nil {
		return err
	}
	return os.RemoveAll(stage)
}
