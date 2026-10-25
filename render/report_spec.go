package render

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/font/sfnt"
)

// ReportFontFace 记录实际文件中的字体面；内嵌字体以引擎字节身份标识，不伪造字体文件摘要。
type ReportFontFace struct {
	Family     string `json:"family"`
	PostScript string `json:"postscript,omitempty"`
	Version    string `json:"version,omitempty"`
	Index      *int   `json:"index,omitempty"`
	FileDigest string `json:"file_digest,omitempty"`
	Source     string `json:"source"`
}

type ReportFontManifest struct {
	Requested        map[string][]string `json:"requested"`
	Selected         map[string]string   `json:"selected"`
	Fallback         map[string]bool     `json:"fallback"`
	Faces            []ReportFontFace    `json:"faces"`
	CatalogDigest    string              `json:"catalog_digest"`
	SearchPathDigest string              `json:"search_path_digest"`
	TypstDigest      string              `json:"typst_digest"`
	Complete         bool                `json:"complete"`
	Notes            []string            `json:"notes,omitempty"`
}

// ReportRenderSpec 在编译前冻结实际环境与模板身份；检查结果和PDF摘要不进入规格。
type ReportRenderSpec struct {
	Version       string             `json:"version"`
	Bundle        ReportBundleRef    `json:"bundle"`
	ProfileID     string             `json:"profile_id"`
	ProfileAPI    int                `json:"profile_api"`
	InputSchema   string             `json:"input_schema"`
	Engine        string             `json:"engine"`
	EngineDigests map[string]string  `json:"engine_digests"`
	Fonts         ReportFonts        `json:"fonts"`
	FontManifest  ReportFontManifest `json:"font_manifest"`
}

func (s ReportRenderSpec) Identity() (string, string, error) {
	data, err := reportCanonicalJSON(s)
	if err != nil {
		return "", "", err
	}
	return reportDigest(data), string(data), nil
}

// ResolveReportSpec 只读取当前模板与引擎/字体，不编译文件，不改变系统字体选择。
func (s *Service) ResolveReportSpec(ctx context.Context, bundle ReportBundle) (ReportRenderSpec, error) {
	r, ok := s.renderer.(*PandocRenderer)
	if !ok {
		return ReportRenderSpec{}, fmt.Errorf("report renderer is unavailable")
	}
	fonts, err := r.resolveReportFonts(ctx, bundle.profile)
	if err != nil {
		return ReportRenderSpec{}, err
	}
	engine, err := r.reportEngineIdentity(ctx)
	if err != nil {
		return ReportRenderSpec{}, err
	}
	pandoc, err := r.lookPandoc()
	if err != nil {
		return ReportRenderSpec{}, err
	}
	typst, err := r.lookTypst()
	if err != nil {
		return ReportRenderSpec{}, err
	}
	digests := map[string]string{}
	for name, file := range map[string]string{"pandoc": pandoc, "typst": typst} {
		data, err := os.ReadFile(file)
		if err != nil {
			return ReportRenderSpec{}, err
		}
		digests[name] = reportDigest(data)
	}
	manifest := resolveReportFontManifest(ctx, r, typst, digests["typst"], bundle.profile.RequestedFonts, fonts)
	if err := ctx.Err(); err != nil {
		return ReportRenderSpec{}, err
	}
	raw, _ := reportCanonicalJSON(manifest)
	if manifest.Complete {
		fonts.ManifestDigest = reportDigest(raw)
	}
	return ReportRenderSpec{Version: "report-render-spec-v1", Bundle: bundle.ref, ProfileID: bundle.profile.ID, ProfileAPI: bundle.profile.API, InputSchema: bundle.profile.InputSchema, Engine: engine, EngineDigests: digests, Fonts: fonts, FontManifest: manifest}, nil
}

func resolveReportFontManifest(ctx context.Context, r *PandocRenderer, typst, typstDigest string, requested map[string][]string, fonts ReportFonts) ReportFontManifest {
	manifest := ReportFontManifest{Requested: requested, Selected: map[string]string{"sans": fonts.Sans, "serif": fonts.Serif, "math": fonts.Math}, Fallback: map[string]bool{}, TypstDigest: typstDigest, Complete: true}
	for role, family := range manifest.Selected {
		manifest.Fallback[role] = len(requested[role]) > 0 && requested[role][0] != family
	}
	// 用实际CLI列出内嵌字体，排除自定义路径与系统字体；不由字体名称推断来源。
	cmd := exec.CommandContext(ctx, typst, "fonts", "--ignore-system-fonts")
	for _, value := range r.subprocessEnv() {
		if !strings.HasPrefix(value, "TYPST_FONT_PATHS=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	embedded := map[string]bool{}
	if out, err := cmd.Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			embedded[strings.TrimSpace(line)] = true
		}
	}
	dirs := filepath.SplitList(os.Getenv("TYPST_FONT_PATHS"))
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs, "/System/Library/Fonts", "/Library/Fonts", filepath.Join(home, "Library", "Fonts"))
	case "windows":
		dirs = append(dirs, filepath.Join(os.Getenv("WINDIR"), "Fonts"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Windows", "Fonts"))
	default:
		dirs = append(dirs, "/usr/share/fonts", "/usr/local/share/fonts", filepath.Join(home, ".fonts"), filepath.Join(home, ".local", "share", "fonts"))
	}
	seen := map[string]bool{}
	pathOrder, _ := json.Marshal(dirs)
	manifest.SearchPathDigest = reportDigest(pathOrder)
	catalog := map[string]string{}
	found := map[string]bool{}
	// fontconfig能读到系统登记但位于系统字体目录之外的字体（如macOS按需字体）。
	// 它是可选来源；不存在时继续直接读取已有字体目录，不改变渲染可用性。
	if fc, err := exec.LookPath("fc-list"); err == nil {
		command := exec.CommandContext(ctx, fc, "--format", "%{file}\t%{index}\t%{family}\t%{postscriptname}\t%{fontversion}\n")
		command.Env = r.subprocessEnv()
		if raw, err := command.Output(); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Split(line, "\t")
				if len(fields) != 5 {
					continue
				}
				file := fields[0]
				digest := catalog[file]
				if digest == "" {
					data, err := os.ReadFile(file)
					if err != nil {
						manifest.Complete = false
						continue
					}
					digest = reportDigest(data)
					catalog[file] = digest
				}
				for _, family := range manifest.Selected {
					for _, alias := range strings.Split(fields[2], ",") {
						if !strings.EqualFold(strings.TrimSpace(alias), family) {
							continue
						}
						index, _ := strconv.Atoi(fields[1])
						found[family] = true
						manifest.Faces = append(manifest.Faces, ReportFontFace{Family: family, PostScript: fields[3], Version: fields[4], Index: &index, FileDigest: digest, Source: "font_file"})
						break
					}
				}
			}
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				if !os.IsNotExist(walkErr) {
					manifest.Complete = false
					manifest.Notes = append(manifest.Notes, "Font directory could not be completely inspected")
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".ttf" && ext != ".ttc" && ext != ".otf" && ext != ".otc" {
				return nil
			}
			actual, err := filepath.EvalSymlinks(path)
			if err != nil || seen[actual] {
				return nil
			}
			seen[actual] = true
			data, err := os.ReadFile(actual)
			if err != nil {
				manifest.Complete = false
				return nil
			}
			digest := reportDigest(data)
			catalog[actual] = digest
			collection, err := sfnt.ParseCollection(data)
			if err != nil {
				return nil
			}
			for index := 0; index < collection.NumFonts(); index++ {
				font, err := collection.Font(index)
				if err != nil {
					continue
				}
				family, _ := font.Name(nil, sfnt.NameIDTypographicFamily)
				if family == "" {
					family, _ = font.Name(nil, sfnt.NameIDFamily)
				}
				selected := false
				for _, want := range manifest.Selected {
					if strings.EqualFold(family, want) && !found[want] {
						selected = true
						found[want] = true
					}
				}
				if !selected {
					continue
				}
				postscript, _ := font.Name(nil, sfnt.NameIDPostScript)
				version, _ := font.Name(nil, sfnt.NameIDVersion)
				manifest.Faces = append(manifest.Faces, ReportFontFace{Family: family, PostScript: postscript, Version: version, Index: &index, FileDigest: digest, Source: "font_file"})
			}
			return nil
		})
	}
	for _, family := range manifest.Selected {
		if embedded[family] {
			manifest.Faces = append(manifest.Faces, ReportFontFace{Family: family, Source: "typst_embedded"})
			found[family] = true
		}
		if !found[family] {
			manifest.Complete = false
			manifest.Notes = append(manifest.Notes, "Font source unresolved: "+family)
		}
	}
	// 全目录字节摘要覆盖字形回退；路径只参与本机检索，持久记录不泄露宿主目录。
	digests := make([]string, 0, len(catalog))
	for _, digest := range catalog {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	raw, _ := json.Marshal(digests)
	manifest.CatalogDigest = reportDigest(raw)
	sort.Slice(manifest.Faces, func(i, j int) bool {
		a, _ := json.Marshal(manifest.Faces[i])
		b, _ := json.Marshal(manifest.Faces[j])
		return string(a) < string(b)
	})
	sort.Strings(manifest.Notes)
	return manifest
}
