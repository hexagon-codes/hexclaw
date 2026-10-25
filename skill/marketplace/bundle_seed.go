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

// SeedBundlesFromFS 通过原出厂链首装完整包；只补齐与官方正文逐字相同的存量，不覆盖用户内容。
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
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if m.getDisabled()[uninstalledSkillKey(name)] {
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
		if _, err := ReadBundleManifest(destination); err == nil {
			continue
		}
		stage, err := os.MkdirTemp("", "hexclaw-bundled-source-*")
		if err != nil {
			return count, err
		}
		seedErr := func() error {
			defer os.RemoveAll(stage)
			for relative, data := range files {
				target := filepath.Join(stage, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					return err
				}
				if err := os.WriteFile(target, data, 0644); err != nil {
					return err
				}
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
		count++
	}
	return count, nil
}
