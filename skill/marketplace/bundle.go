package marketplace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// BundleFile 固定内容包中的资源身份，摘要不依赖宿主安装路径。
type BundleFile struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BundleManifest 与现有资源包 bundle.json 使用同一声明。
type BundleManifest struct {
	ContractVersion int          `json:"bundle_contract_version"`
	SkillID         string       `json:"skill_id"`
	Version         string       `json:"version"`
	Files           []BundleFile `json:"files"`
}

// BundleValidator 由宿主核对已暂存包的实际消费契约；其他类型的包应直接返回 nil。
// root 是只读候选目录，校验失败不会替换已安装版本。
type BundleValidator func(root string, manifest BundleManifest) error

// SetBundleValidator 由宿主启动时注入资源兼容检查，不改变普通 Markdown 技能的合同。
func (m *Marketplace) SetBundleValidator(validator BundleValidator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bundleValidator = validator
}

func (m *Marketplace) validateBundleStage(root string) (BundleManifest, error) {
	manifest, err := ReadBundleManifest(root)
	if err != nil {
		return manifest, err
	}
	m.mu.RLock()
	validator := m.bundleValidator
	m.mu.RUnlock()
	if validator != nil {
		if err := validator(root, manifest); err != nil {
			return manifest, fmt.Errorf("skill bundle is incompatible with this host: %w", err)
		}
	}
	return manifest, nil
}

// SemanticDigest 使用既有包合同的四个根字段和确定性 JSON，排除清单自身的传输空白。
func (m BundleManifest) SemanticDigest() (string, error) {
	files := make([]map[string]any, 0, len(m.Files))
	for _, f := range m.Files {
		files = append(files, map[string]any{"path": f.Path, "role": f.Role, "size": f.Size, "sha256": f.SHA256})
	}
	root := map[string]any{"bundle_contract_version": m.ContractVersion, "skill_id": m.SkillID, "version": m.Version, "files": files}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(root); err != nil {
		return "", err
	}
	data := bytes.TrimSuffix(out.Bytes(), []byte("\n"))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Resource 是精确读取出的已安装资源及其字节摘要。
type Resource struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Role    string `json:"role"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Content []byte `json:"-"`
}

func bundlePath(root, relative string) (string, error) {
	if relative == "" || strings.Contains(relative, "\\") || path.Clean(relative) != relative || !filepath.IsLocal(filepath.FromSlash(relative)) {
		return "", fmt.Errorf("invalid skill resource path")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	within, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || !filepath.IsLocal(within) {
		return "", fmt.Errorf("skill resource escapes the installed package")
	}
	return resolved, nil
}

// ReadBundleManifest 校验包声明和每个实际资源，模板不完整不能冒充安装成功。
func ReadBundleManifest(root string) (BundleManifest, error) {
	var manifest BundleManifest
	file, err := bundlePath(root, "bundle.json")
	if err != nil {
		return manifest, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	if manifest.ContractVersion != 1 || manifest.SkillID == "" || manifest.Version == "" || len(manifest.Files) == 0 {
		return manifest, fmt.Errorf("invalid skill bundle identity")
	}
	seen := make(map[string]bool)
	for _, entry := range manifest.Files {
		if seen[entry.Path] || entry.Path == "bundle.json" {
			return manifest, fmt.Errorf("duplicate or recursive skill bundle resource")
		}
		seen[entry.Path] = true
		file, err := bundlePath(root, entry.Path)
		if err != nil {
			return manifest, err
		}
		info, err := os.Stat(file)
		if err != nil {
			return manifest, err
		}
		if !info.Mode().IsRegular() || info.Size() != entry.Size {
			return manifest, fmt.Errorf("skill resource size mismatch: %s", entry.Path)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return manifest, err
		}
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), entry.SHA256) {
			return manifest, fmt.Errorf("skill resource digest mismatch: %s", entry.Path)
		}
	}
	if !seen["SKILL.md"] {
		return manifest, fmt.Errorf("skill bundle is missing SKILL.md")
	}
	return manifest, nil
}

func (m *Marketplace) installBundle(source string) (*MarkdownSkill, error) {
	manifest, err := ReadBundleManifest(source)
	if err != nil {
		return nil, err
	}
	sk, err := LoadSkillFromFile(filepath.Join(source, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	if sk.Meta.Name != manifest.SkillID || sk.Meta.Version != manifest.Version || filepath.Base(sk.Meta.Name) != sk.Meta.Name || strings.Contains(sk.Meta.Name, "..") {
		return nil, fmt.Errorf("skill and bundle identities do not match")
	}
	if err := os.MkdirAll(m.skillDir, 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(m.skillDir, ".hexclaw-bundle-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		// 发布记录尚未收尾时保留候选，恢复过程仍需据此区分未提交与已提交。
		if _, err := os.Lstat(stage + ".pending"); os.IsNotExist(err) {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, entry := range append(append([]BundleFile(nil), manifest.Files...), BundleFile{Path: "bundle.json"}) {
		file, err := bundlePath(source, entry.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(stage, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return nil, err
		}
	}
	manifest, err = m.validateBundleStage(stage)
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(m.skillDir, sk.Meta.Name)
	backup := stage + ".previous"
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.recoverBundleInstallsLocked(); err != nil {
		return nil, err
	}
	if err := writeBundleInstallRecord(stage, manifest); err != nil {
		return nil, err
	}
	_, existingErr := os.Lstat(destination)
	if existingErr == nil {
		if err := os.Rename(destination, backup); err != nil {
			_ = os.Remove(stage + ".pending")
			return nil, err
		}
	} else if !os.IsNotExist(existingErr) {
		_ = os.Remove(stage + ".pending")
		return nil, existingErr
	}
	if err := os.Rename(stage, destination); err != nil {
		if recoveryErr := m.recoverBundleInstallLocked(stage + ".pending"); recoveryErr != nil {
			return nil, fmt.Errorf("publish skill bundle: %w; recovery failed: %v", err, recoveryErr)
		}
		return nil, err
	}
	if err := m.recoverBundleInstallLocked(stage + ".pending"); err != nil {
		return nil, err
	}
	sk, err = LoadSkillFromFile(filepath.Join(destination, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	m.skills[sk.Meta.Name] = sk
	return sk, nil
}

// ReadResource 只读取当前已安装包声明中的精确资源，不承担脚本执行。
func (m *Marketplace) ReadResource(name, relative string) (Resource, error) {
	sk, ok := m.Get(name)
	if !ok {
		return Resource{}, ErrSkillNotInstalled
	}
	root := filepath.Dir(sk.FilePath)
	_, manifestErr := os.Stat(filepath.Join(root, "bundle.json"))
	if filepath.Base(sk.FilePath) != "SKILL.md" || os.IsNotExist(manifestErr) {
		if relative != "SKILL.md" {
			return Resource{}, fmt.Errorf("skill has no resource bundle")
		}
		data, err := os.ReadFile(sk.FilePath)
		if err != nil {
			return Resource{}, err
		}
		sum := sha256.Sum256(data)
		return Resource{SkillID: name, Version: sk.Meta.Version, Path: relative, Role: "skill", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Content: data}, nil
	}
	manifest, err := ReadBundleManifest(root)
	if err != nil {
		return Resource{}, err
	}
	if manifest.SkillID != name || manifest.Version != sk.Meta.Version {
		return Resource{}, fmt.Errorf("installed bundle identity changed; refresh is required")
	}
	if relative == "bundle.json" {
		data, err := os.ReadFile(filepath.Join(root, "bundle.json"))
		if err != nil {
			return Resource{}, err
		}
		sum := sha256.Sum256(data)
		return Resource{SkillID: name, Version: manifest.Version, Path: relative, Role: "manifest", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Content: data}, nil
	}
	for _, entry := range manifest.Files {
		if entry.Path != relative {
			continue
		}
		file, err := bundlePath(root, relative)
		if err != nil {
			return Resource{}, err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return Resource{}, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != entry.Size || !strings.EqualFold(hex.EncodeToString(sum[:]), entry.SHA256) {
			return Resource{}, fmt.Errorf("skill resource changed during read")
		}
		return Resource{SkillID: name, Version: manifest.Version, Path: relative, Role: entry.Role, Size: entry.Size, SHA256: entry.SHA256, Content: data}, nil
	}
	return Resource{}, fmt.Errorf("skill resource is not declared: %s", relative)
}

// Resources 返回有序的包资源身份，不把模板正文灌入模型上下文。
func (m *Marketplace) Resources(name string) ([]BundleFile, error) {
	sk, ok := m.Get(name)
	if !ok {
		return nil, ErrSkillNotInstalled
	}
	_, manifestErr := os.Stat(filepath.Join(filepath.Dir(sk.FilePath), "bundle.json"))
	if filepath.Base(sk.FilePath) != "SKILL.md" || os.IsNotExist(manifestErr) {
		resource, err := m.ReadResource(name, "SKILL.md")
		return []BundleFile{{Path: resource.Path, Role: resource.Role, Size: resource.Size, SHA256: resource.SHA256}}, err
	}
	manifest, err := ReadBundleManifest(filepath.Dir(sk.FilePath))
	if err != nil {
		return nil, err
	}
	files := append([]BundleFile(nil), manifest.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// updateInstalledContent 更新已有目录技能时保留同包资源及目录身份。
func (m *Marketplace) updateInstalledContent(candidate *MarkdownSkill, content []byte) (*MarkdownSkill, bool, error) {
	current, ok := m.Get(candidate.Name())
	if !ok || filepath.Base(current.FilePath) != "SKILL.md" {
		return nil, false, nil
	}
	root := filepath.Dir(current.FilePath)
	_, err := os.Stat(filepath.Join(root, "bundle.json"))
	if os.IsNotExist(err) {
		file, err := os.CreateTemp(root, ".skill-content-*")
		if err != nil {
			return nil, true, err
		}
		defer os.Remove(file.Name())
		if _, err := file.Write(content); err != nil {
			file.Close()
			return nil, true, err
		}
		if err := file.Close(); err != nil {
			return nil, true, err
		}
		if err := os.Rename(file.Name(), current.FilePath); err != nil {
			return nil, true, err
		}
		updated, err := LoadSkillFromFile(current.FilePath)
		if err != nil {
			return nil, true, err
		}
		m.mu.Lock()
		m.skills[updated.Name()] = updated
		m.mu.Unlock()
		return updated, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	manifest, err := ReadBundleManifest(root)
	if err != nil {
		return nil, true, err
	}
	stage, err := os.MkdirTemp("", "hexclaw-skill-edit-*")
	if err != nil {
		return nil, true, err
	}
	defer os.RemoveAll(stage)
	for i, entry := range manifest.Files {
		data := content
		if entry.Path != "SKILL.md" {
			source, err := bundlePath(root, entry.Path)
			if err != nil {
				return nil, true, err
			}
			data, err = os.ReadFile(source)
			if err != nil {
				return nil, true, err
			}
		} else {
			sum := sha256.Sum256(content)
			manifest.Files[i].Size = int64(len(content))
			manifest.Files[i].SHA256 = hex.EncodeToString(sum[:])
		}
		target := filepath.Join(stage, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return nil, true, err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return nil, true, err
		}
	}
	manifest.Version = candidate.Meta.Version
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, true, err
	}
	if err := os.WriteFile(filepath.Join(stage, "bundle.json"), encoded, 0644); err != nil {
		return nil, true, err
	}
	updated, err := m.installBundle(stage)
	return updated, true, err
}
