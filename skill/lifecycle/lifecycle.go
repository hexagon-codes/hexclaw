// Package lifecycle 统一已安装 Skill 的持久状态、资源与运行态刷新。
package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hexagon-codes/hexclaw/skill/hub"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// ErrContentConflict 表示片段修改读取后，已安装正文已被其他写入更新。
var ErrContentConflict = errors.New("skill patch conflict: installed content changed")

// Runtime 由宿主提供现有注册表热加载能力，不包含安装或 HTTP 协议。
type Runtime interface {
	SyncMarkdownSkillsFromMarketplace(*marketplace.Marketplace) error
	SkillEnabled(string) (bool, bool)
}

// Installed 同时返回持久设置及当前运行事实，不能把落盘当成生效。
type Installed struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	Description      string `json:"description"`
	Enabled          bool   `json:"enabled"`
	Registered       bool   `json:"runtime_registered"`
	EffectiveEnabled bool   `json:"effective_enabled"`
	RequiresRestart  bool   `json:"requires_restart"`
}

type Service struct {
	mu        sync.Mutex
	mp        *marketplace.Marketplace
	hub       *hub.Hub
	runtime   Runtime
	bundled   fs.FS
	bundleDir string
}

func New(mp *marketplace.Marketplace, h *hub.Hub, runtime Runtime) *Service {
	return &Service{mp: mp, hub: h, runtime: runtime}
}
func (s *Service) Dir() string   { return s.mp.Dir() }
func (s *Service) Hub() *hub.Hub { return s.hub }

// SetBundledSource 仅由宿主启动装配调用，发布后源保持只读，不依赖运行时邻仓路径。
func (s *Service) SetBundledSource(source fs.FS, subdir string) {
	s.bundled = source
	s.bundleDir = subdir
}

func (s *Service) bundledMeta(name string) (hub.SkillMeta, error) {
	if s.bundled == nil || filepath.Base(name) != name {
		return hub.SkillMeta{}, fmt.Errorf("bundled skill is unavailable")
	}
	data, err := fs.ReadFile(s.bundled, path.Join(s.bundleDir, name, "bundle.json"))
	if err != nil {
		return hub.SkillMeta{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return hub.SkillMeta{}, err
	}
	if manifest.SkillID != name {
		return hub.SkillMeta{}, fmt.Errorf("bundled skill identity mismatch")
	}
	body, err := fs.ReadFile(s.bundled, path.Join(s.bundleDir, name, "SKILL.md"))
	if err != nil {
		return hub.SkillMeta{}, err
	}
	digest, err := manifest.SemanticDigest()
	if err != nil {
		return hub.SkillMeta{}, err
	}
	sum := sha256.Sum256(body)
	meta := marketplace.ParseMetadata(string(body))
	if meta.Name != name || meta.Version != manifest.Version {
		return hub.SkillMeta{}, fmt.Errorf("bundled skill metadata mismatch")
	}
	return hub.SkillMeta{Name: name, Version: manifest.Version, DisplayName: meta.DisplayName, DisplayNames: meta.DisplayNames, Description: meta.Description, Author: meta.Author, Category: meta.Category, Tags: meta.Tags, Requires: meta.Requires, Type: "skill", Source: "bundled", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)), BundleSHA256: digest}, nil
}

// Catalog 把真实 Hub 目录和未覆盖的只读出厂包同时列出，来源明确且同名不混合版本。
func (s *Service) Catalog() []hub.SkillMeta {
	var entries []hub.SkillMeta
	seen := map[string]bool{}
	if s.hub != nil {
		s.hub.EnsureCatalog()
		if catalog := s.hub.GetCatalog(); catalog != nil {
			for _, entry := range catalog.Skills {
				if !strings.EqualFold(entry.Type, "mcp") {
					entries = append(entries, entry)
					seen[entry.Name] = true
				}
			}
		}
	}
	if s.bundled != nil {
		if dirs, err := fs.ReadDir(s.bundled, s.bundleDir); err == nil {
			for _, dir := range dirs {
				if !dir.IsDir() || seen[dir.Name()] {
					continue
				}
				if meta, err := s.bundledMeta(dir.Name()); err == nil {
					entries = append(entries, meta)
				}
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (s *Service) HasBundled(name string) bool { _, err := s.bundledMeta(name); return err == nil }

func (s *Service) preferBundled(name string) bool {
	bundled, err := s.bundledMeta(name)
	if err != nil {
		return false
	}
	if s.hub != nil {
		s.hub.EnsureCatalog()
		if catalog := s.hub.GetCatalog(); catalog != nil {
			for _, entry := range catalog.Skills {
				if entry.Name == name {
					return !strings.EqualFold(entry.Type, "mcp") && entry.Version == bundled.Version && entry.SHA256 == bundled.SHA256 && (entry.BundleSHA256 == "" || entry.BundleSHA256 == bundled.BundleSHA256)
				}
			}
		}
	}
	return true
}

// CatalogContent 只预览选定来源的说明文件，不安装资源也不改变启停状态。
func (s *Service) CatalogContent(ctx context.Context, name string) ([]byte, string, error) {
	if s.preferBundled(name) {
		data, err := fs.ReadFile(s.bundled, path.Join(s.bundleDir, name, "SKILL.md"))
		return data, "bundled", err
	}
	if s.hub == nil {
		return nil, "", fmt.Errorf("skill Hub is unavailable")
	}
	data, err := s.hub.Content(ctx, name)
	return data, "hub", err
}

type Installation struct {
	Skill  *marketplace.MarkdownSkill
	Source string
}

func (s *Service) installBundled(ctx context.Context, name string) (Installation, error) {
	data, err := fs.ReadFile(s.bundled, path.Join(s.bundleDir, name, "bundle.json"))
	if err != nil {
		return Installation{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Installation{}, err
	}
	stage, err := os.MkdirTemp("", "hexclaw-bundled-source-*")
	if err != nil {
		return Installation{}, err
	}
	defer os.RemoveAll(stage)
	files := []string{"bundle.json"}
	for _, entry := range manifest.Files {
		if !fs.ValidPath(entry.Path) {
			return Installation{}, fmt.Errorf("invalid bundled skill path")
		}
		files = append(files, entry.Path)
	}
	for _, file := range files {
		content, err := fs.ReadFile(s.bundled, path.Join(s.bundleDir, name, file))
		if err != nil {
			return Installation{}, err
		}
		target := filepath.Join(stage, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return Installation{}, err
		}
		if err := os.WriteFile(target, content, 0644); err != nil {
			return Installation{}, err
		}
	}
	sk, err := s.InstallFile(ctx, stage)
	return Installation{Skill: sk, Source: "bundled"}, err
}

func (s *Service) refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.runtime != nil {
		return s.runtime.SyncMarkdownSkillsFromMarketplace(s.mp)
	}
	return s.mp.Init()
}

// Refresh 原子更新加载器快照，保留 disabled.json 中的用户选择。
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh(ctx)
}

func (s *Service) State(name string) (Installed, error) {
	sk, ok := s.mp.Get(name)
	if !ok {
		return Installed{}, marketplace.ErrSkillNotInstalled
	}
	entry := Installed{Name: name, Version: sk.Meta.Version, Description: sk.Meta.Description, Enabled: s.mp.IsEnabled(name)}
	if s.runtime != nil {
		entry.EffectiveEnabled, entry.Registered = s.runtime.SkillEnabled(name)
	}
	entry.RequiresRestart = s.runtime == nil && entry.Enabled
	return entry, nil
}

func (s *Service) List() []Installed {
	list := make([]Installed, 0)
	for _, sk := range s.mp.List() {
		entry, err := s.State(sk.Name())
		if err == nil {
			list = append(list, entry)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

func (s *Service) InstallFile(ctx context.Context, source string) (*marketplace.MarkdownSkill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installFile(ctx, source)
}

func (s *Service) installFile(ctx context.Context, source string) (*marketplace.MarkdownSkill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sk, err := s.mp.Install(source)
	if err != nil {
		return nil, err
	}
	if err := s.refresh(ctx); err != nil {
		return sk, fmt.Errorf("skill persisted but runtime refresh failed: %w", err)
	}
	return sk, nil
}

func (s *Service) InstallHub(ctx context.Context, name string) (*marketplace.MarkdownSkill, error) {
	installed, err := s.InstallFromCatalog(ctx, name)
	return installed.Skill, err
}

// InstallFromCatalog 同一版本且全部声明相符时可使用出厂字节，不把下载失败解释成版本回退。
func (s *Service) InstallFromCatalog(ctx context.Context, name string) (Installation, error) {
	if s.preferBundled(name) {
		return s.installBundled(ctx, name)
	}
	if s.hub == nil {
		return Installation{}, fmt.Errorf("skill Hub is unavailable")
	}
	source, cleanup, err := s.hub.PackageSource(ctx, name)
	if err != nil {
		return Installation{}, err
	}
	defer cleanup()
	sk, err := s.InstallFile(ctx, source)
	return Installation{Skill: sk, Source: "hub"}, err
}

func (s *Service) SetEnabled(ctx context.Context, name string, enabled bool) (Installed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mp.Get(name); !ok {
		return Installed{}, marketplace.ErrSkillNotInstalled
	}
	if err := ctx.Err(); err != nil {
		return Installed{}, err
	}
	if err := s.mp.SetEnabled(name, enabled); err != nil {
		return Installed{}, err
	}
	if err := s.refresh(ctx); err != nil {
		return Installed{}, fmt.Errorf("skill status persisted but runtime refresh failed: %w", err)
	}
	return s.State(name)
}

func (s *Service) Uninstall(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.mp.Uninstall(name); err != nil {
		return err
	}
	return s.refresh(ctx)
}

func (s *Service) Resources(name string) ([]marketplace.BundleFile, error) {
	return s.mp.Resources(name)
}
func (s *Service) ReadResource(name, path string) (marketplace.Resource, error) {
	return s.mp.ReadResource(name, path)
}

// ApplyContent 将作者产生的正文经同一安装和刷新用例激活，不另建运行态写入路径。
func (s *Service) ApplyContent(ctx context.Context, name, content string) (Installed, error) {
	return s.applyContent(ctx, name, content, nil)
}

// ApplyContentIfUnchanged 只提交仍基于当前正文的片段修改，冲突不覆盖已完成的其他修改。
func (s *Service) ApplyContentIfUnchanged(ctx context.Context, name, content string, expected [sha256.Size]byte) (Installed, error) {
	return s.applyContent(ctx, name, content, &expected)
}

func (s *Service) applyContent(ctx context.Context, name, content string, expected *[sha256.Size]byte) (Installed, error) {
	file, err := os.CreateTemp("", "hexclaw-skill-author-*.md")
	if err != nil {
		return Installed{}, err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return Installed{}, err
	}
	if err := file.Close(); err != nil {
		return Installed{}, err
	}
	parsed, err := marketplace.LoadSkillFromFile(file.Name())
	if err != nil {
		return Installed{}, err
	}
	if parsed.Name() != name {
		return Installed{}, fmt.Errorf("authored skill identity does not match its target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != nil {
		current, ok := s.mp.Get(name)
		if !ok {
			return Installed{}, ErrContentConflict
		}
		body, err := os.ReadFile(current.FilePath)
		if err != nil {
			return Installed{}, err
		}
		if sha256.Sum256(body) != *expected {
			return Installed{}, ErrContentConflict
		}
	}
	if _, err := s.installFile(ctx, file.Name()); err != nil {
		return Installed{}, err
	}
	if err := os.Remove(filepath.Join(s.Dir(), name, "SKILL.md.pending")); err != nil && !os.IsNotExist(err) {
		return Installed{}, err
	}
	_ = os.Remove(filepath.Join(s.Dir(), name))
	return s.State(name)
}
