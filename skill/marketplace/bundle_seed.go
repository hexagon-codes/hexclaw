package marketplace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

func uninstalledSkillKey(name string) string { return "@uninstalled/" + name }

func (m *Marketplace) clearUninstalledLocked(name string) error {
	disabled := m.getDisabledLocked()
	if !disabled[uninstalledSkillKey(name)] {
		return nil
	}
	delete(disabled, uninstalledSkillKey(name))
	return m.saveDisabledLocked(disabled)
}

// SeedBundlesFromFS 首装内嵌包，并按出厂来源更新内容完整的官方旧包。
func (m *Marketplace) SeedBundlesFromFS(source fs.FS, subdir string) (int, error) {
	if err := m.recoverBundleInstalls(); err != nil {
		return 0, err
	}
	entries, err := fs.ReadDir(source, subdir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var predecessors map[string]map[string]string
	data, err := fs.ReadFile(source, path.Join(subdir, "seed-predecessors.json"))
	if err == nil {
		if err := json.Unmarshal(data, &predecessors); err != nil {
			return 0, fmt.Errorf("read bundled skill predecessors: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		disabled := m.getDisabled()
		if disabled[name] || disabled[uninstalledSkillKey(name)] {
			continue
		}
		root := path.Join(subdir, name)
		manifestBytes, err := fs.ReadFile(source, path.Join(root, "bundle.json"))
		if err != nil {
			return count, err
		}
		var manifest BundleManifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			return count, err
		}
		if manifest.SkillID != name || manifest.ContractVersion != 1 {
			return count, fmt.Errorf("bundled skill identity mismatch")
		}
		files := make(map[string][]byte, len(manifest.Files)+1)
		files["bundle.json"] = manifestBytes
		for _, resource := range manifest.Files {
			if !fs.ValidPath(resource.Path) || resource.Path == "bundle.json" {
				return count, fmt.Errorf("invalid bundled skill resource path")
			}
			data, err := fs.ReadFile(source, path.Join(root, resource.Path))
			if err != nil {
				return count, err
			}
			sum := sha256.Sum256(data)
			if int64(len(data)) != resource.Size || hex.EncodeToString(sum[:]) != resource.SHA256 {
				return count, fmt.Errorf("bundled skill resource digest mismatch")
			}
			files[resource.Path] = data
		}
		body, ok := files["SKILL.md"]
		if !ok {
			return count, fmt.Errorf("bundled skill has no SKILL.md")
		}
		destination := filepath.Join(m.skillDir, name)
		if current, err := ReadBundleManifest(destination); err == nil {
			upgraded, err := m.upgradeSeedBundle(destination, current, manifest, files, predecessors[name])
			if err != nil {
				return count, err
			}
			if upgraded {
				count++
			}
			continue
		}
		custom := false
		for _, existing := range []string{filepath.Join(destination, "SKILL.md"), filepath.Join(m.skillDir, name+".md")} {
			data, err := os.ReadFile(existing)
			if err == nil && !bytes.Equal(data, body) {
				custom = true
			}
			if err != nil && !os.IsNotExist(err) {
				return count, err
			}
		}
		if custom {
			continue
		}
		// 对已经存在的资源也先逐一核对；有自定义内容时不部分覆盖另一份包。
		for relative, data := range files {
			actual, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(relative)))
			if err != nil && !os.IsNotExist(err) {
				return count, err
			}
			if err == nil && !bytes.Equal(actual, data) {
				if relative == "bundle.json" {
					var current BundleManifest
					if json.Unmarshal(actual, &current) == nil {
						a, aErr := current.SemanticDigest()
						b, bErr := manifest.SemanticDigest()
						if aErr == nil && bErr == nil && a == b {
							continue
						}
					}
				}
				custom = true
				break
			}
		}
		if custom {
			continue
		}
		stage, err := os.MkdirTemp("", "hexclaw-bundled-source-*")
		if err != nil {
			return count, err
		}
		seedErr := func() error {
			defer os.RemoveAll(stage)
			if err := writeSeedBundle(stage, files); err != nil {
				return err
			}
			if _, err := m.validateBundleStage(stage); err != nil {
				return err
			}
			// 首装补齐仍使用不覆盖发布，保留目录中未列入官方清单的用户文件。
			for relative, data := range files {
				target := filepath.Join(destination, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return err
				}
				if _, err := publishSeedNoReplace(target, data); err != nil {
					return err
				}
			}
			_, err := ReadBundleManifest(destination)
			return err
		}()
		if seedErr != nil {
			return count, seedErr
		}
		if seedBundleMembersMatch(destination, manifest) {
			if err := m.recordBundleSeed(name, hashSeedContent(manifestBytes)); err != nil {
				return count, err
			}
		}
		count++
	}
	return count, nil
}

func writeSeedBundle(stage string, files map[string][]byte) error {
	for relative, data := range files {
		target := filepath.Join(stage, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (m *Marketplace) recordBundleSeed(name, digest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	manifest := loadSeedManifest(m.skillDir)
	key := name + "/bundle.json"
	if manifest[key] == digest {
		return nil
	}
	manifest[key] = digest
	return saveSeedManifest(m.skillDir, manifest)
}

func (m *Marketplace) upgradeSeedBundle(destination string, current, next BundleManifest, files map[string][]byte, predecessors map[string]string) (bool, error) {
	manifestPath := filepath.Join(destination, "bundle.json")
	oldManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return false, err
	}
	// 原始清单摘要也锁住额外字段与用户排版；ReadBundleManifest 已核对全部声明资源。
	oldDigest := hashSeedContent(oldManifest)
	incoming := hashSeedContent(files["bundle.json"])
	if oldDigest == incoming {
		if !seedBundleMembersMatch(destination, current) {
			return false, nil
		}
		return false, m.recordBundleSeed(next.SkillID, incoming)
	}
	if current.SkillID != next.SkillID || compareSkillVersions(next.Version, current.Version) <= 0 {
		return false, nil
	}
	if predecessors[current.Version] != oldDigest && loadSeedManifest(m.skillDir)[next.SkillID+"/bundle.json"] != oldDigest {
		return false, nil
	}
	stage, err := os.MkdirTemp("", "hexclaw-bundled-source-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(stage)
	if err := writeSeedBundle(stage, files); err != nil {
		return false, err
	}
	installed, err := m.installBundleIf(stage, func() bool {
		disabled := m.getDisabledLocked()
		_, flatErr := os.Lstat(filepath.Join(m.skillDir, next.SkillID+".md"))
		if disabled[next.SkillID] || disabled[uninstalledSkillKey(next.SkillID)] || !os.IsNotExist(flatErr) {
			return false
		}
		verified, err := ReadBundleManifest(destination)
		if err != nil {
			return false
		}
		actual, err := os.ReadFile(manifestPath)
		return err == nil && bytes.Equal(actual, oldManifest) && seedBundleMembersMatch(destination, verified)
	})
	if err != nil || installed == nil {
		return false, err
	}
	return true, m.recordBundleSeed(next.SkillID, incoming)
}

// ReadBundleManifest 校验声明内的资源；目录交换还须保留未声明的文件、目录和符号链接。
func seedBundleMembersMatch(root string, manifest BundleManifest) bool {
	members := map[string]bool{".": true, "bundle.json": false}
	for _, entry := range manifest.Files {
		members[entry.Path] = false
		for dir := path.Dir(entry.Path); dir != "."; dir = path.Dir(dir) {
			members[dir] = true
		}
	}
	err := filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		dir, known := members[filepath.ToSlash(relative)]
		if !known || dir != d.IsDir() || (!dir && !d.Type().IsRegular()) {
			return fmt.Errorf("unmanaged bundle member")
		}
		return nil
	})
	return err == nil
}
