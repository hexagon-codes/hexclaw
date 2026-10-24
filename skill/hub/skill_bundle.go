package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// PackageSource 下载目录声明的完整资源包，返回临时源；调用者负责清理并经 Marketplace 安装。
// 单文件目录项沿原有完整性读取，不把缺失资源包降成只有说明文件的成功安装。
func (h *Hub) PackageSource(ctx context.Context, name string) (string, func(), error) {
	h.EnsureCatalog()
	meta, ok := h.findSkill(name)
	if !ok {
		return "", nil, fmt.Errorf("skill %q is not in the catalog", name)
	}
	if strings.EqualFold(meta.Type, "mcp") {
		return "", nil, fmt.Errorf("catalog entry is an MCP server, not a skill")
	}
	content, err := h.Content(ctx, name)
	if err != nil {
		return "", nil, err
	}
	stage, err := os.MkdirTemp("", "hexclaw-skill-source-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	okResult := false
	defer func() {
		if !okResult {
			cleanup()
		}
	}()
	if !strings.HasSuffix(meta.File, "/SKILL.md") {
		file := filepath.Join(stage, name+".md")
		if err := os.WriteFile(file, content, 0644); err != nil {
			return "", nil, err
		}
		okResult = true
		return file, cleanup, nil
	}
	source, err := h.resolveDownloadURL(meta, name)
	if err != nil {
		return "", nil, err
	}
	resolve := func(relative string) string {
		if u, err := url.Parse(source); err == nil && u.Scheme != "" {
			u.Path = strings.TrimSuffix(u.Path, "SKILL.md") + relative
			return u.String()
		}
		return filepath.Join(filepath.Dir(source), filepath.FromSlash(relative))
	}
	manifestData, err := h.readSkillContent(ctx, resolve("bundle.json"))
	if err != nil {
		return "", nil, fmt.Errorf("read skill bundle: %w", err)
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", nil, err
	}
	if manifest.SkillID != name || manifest.Version != meta.Version || manifest.ContractVersion != 1 {
		return "", nil, fmt.Errorf("skill bundle catalog identity mismatch")
	}
	if meta.BundleSHA256 != "" {
		digest, err := manifest.SemanticDigest()
		if err != nil {
			return "", nil, err
		}
		if !strings.EqualFold(digest, meta.BundleSHA256) {
			return "", nil, fmt.Errorf("skill bundle semantic digest mismatch")
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "bundle.json"), manifestData, 0644); err != nil {
		return "", nil, err
	}
	for _, entry := range manifest.Files {
		if !filepath.IsLocal(filepath.FromSlash(entry.Path)) || strings.Contains(entry.Path, "\\") {
			return "", nil, fmt.Errorf("invalid bundle resource path")
		}
		var data []byte
		if entry.Path == "SKILL.md" {
			data = content
		} else {
			data, err = h.readPackageResource(ctx, resolve(entry.Path), entry.Size)
		}
		if err != nil {
			return "", nil, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != entry.Size || !strings.EqualFold(hex.EncodeToString(sum[:]), entry.SHA256) {
			return "", nil, fmt.Errorf("skill bundle resource digest mismatch: %s", entry.Path)
		}
		target := filepath.Join(stage, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(target, data, 0644); err != nil {
			return "", nil, err
		}
	}
	if _, err := marketplace.ReadBundleManifest(stage); err != nil {
		return "", nil, err
	}
	okResult = true
	return stage, cleanup, nil
}

func (h *Hub) readPackageResource(ctx context.Context, source string, size int64) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("invalid bundle resource size")
	}
	if dir, local := h.localRepoDir(); local {
		if !isLocalSkillSource(source, dir) {
			return nil, fmt.Errorf("skill resource is outside the configured Hub")
		}
		return os.ReadFile(source)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skill resource returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, size+1))
}
