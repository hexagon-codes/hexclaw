package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReportBundleRef 标识已安装资源的精确版本，不表示安装、启用或授权状态。
type ReportBundleRef struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type reportProfile struct {
	ID               string              `json:"profile_id"`
	API              int                 `json:"render_profile_api"`
	InputSchema      string              `json:"input_schema"`
	Entrypoint       string              `json:"entrypoint"`
	Margins          map[string]float64  `json:"margin_mm"`
	RequestedFonts   map[string][]string `json:"requested_fonts"`
	RequiredFeatures []string            `json:"required_typst_features"`
}

// ReportBundle 保存已经核对字节的只读资源快照，不包含安装器或目录注册。
type ReportBundle struct {
	ref     ReportBundleRef
	profile reportProfile
	files   map[string][]byte
}

func (b ReportBundle) Ref() ReportBundleRef { return b.ref }
func (b ReportBundle) ProfileID() string    { return b.profile.ID }
func (b ReportBundle) InputSchema() string  { return b.profile.InputSchema }

// ValidateHostCompatibility 由报告宿主在安装提交前核对可消费合同，不改变历史快照读取。
func (b ReportBundle) ValidateHostCompatibility() error {
	if b.profile.ID == "" {
		return fmt.Errorf("report profile identity is unavailable")
	}
	if b.profile.InputSchema != "homework-report-render-v1" && b.profile.InputSchema != "homework-report-render-v2" {
		return fmt.Errorf("unsupported report input schema: %s", b.profile.InputSchema)
	}
	for _, feature := range b.profile.RequiredFeatures {
		switch feature {
		case "json", "grid", "table.header", "math.display", "image.fit-contain":
		default:
			return fmt.Errorf("unsupported report compiler feature: %s", feature)
		}
	}
	return nil
}

// LoadReportBundle 读取宿主已确定的本地版本目录；调用者负责安装与有效设置。
// 摘要来自冻结引用，内嵌副本和安装目录使用同一规范化清单身份。
func LoadReportBundle(directory, expectedDigest string) (ReportBundle, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "bundle.json"))
	if err != nil {
		return ReportBundle{}, fmt.Errorf("read report bundle: %w", err)
	}
	return loadReportBundle(raw, expectedDigest, func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(directory, filepath.FromSlash(path)))
	})
}

// LoadReportBundleSnapshot 读取任务已经冻结的资源，不重读当前安装目录或启用状态。
// 返回包拥有独立字节，调用方在加载结束后修改输入映射不会改变已加载模板。
func LoadReportBundleSnapshot(manifest []byte, resources map[string][]byte, expectedDigest string) (ReportBundle, error) {
	return loadReportBundle(manifest, expectedDigest, func(path string) ([]byte, error) {
		content, ok := resources[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return content, nil
	})
}

func loadReportBundle(raw []byte, expectedDigest string, read func(string) ([]byte, error)) (ReportBundle, error) {
	var canonical map[string]any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return ReportBundle{}, fmt.Errorf("decode report bundle: %w", err)
	}
	data, err := reportCanonicalJSON(canonical)
	if err != nil {
		return ReportBundle{}, err
	}
	digest := reportDigest(data)
	if expectedDigest == "" || digest != expectedDigest {
		return ReportBundle{}, fmt.Errorf("report bundle manifest digest mismatch")
	}
	var manifest struct {
		Contract int    `json:"bundle_contract_version"`
		SkillID  string `json:"skill_id"`
		Version  string `json:"version"`
		Files    []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Contract != 1 || manifest.SkillID == "" || manifest.Version == "" {
		return ReportBundle{}, fmt.Errorf("unsupported report bundle contract")
	}
	bundle := ReportBundle{ref: ReportBundleRef{manifest.SkillID, manifest.Version, digest}, files: make(map[string][]byte)}
	previous := ""
	for _, file := range manifest.Files {
		if !reportRelativePath(file.Path) || file.Path == "bundle.json" || file.Path <= previous {
			return ReportBundle{}, fmt.Errorf("report bundle paths must be unique, relative and ordered")
		}
		previous = file.Path
		content, err := read(file.Path)
		if err != nil {
			return ReportBundle{}, fmt.Errorf("read report resource %s: %w", file.Path, err)
		}
		if int64(len(content)) != file.Size || reportDigest(content) != file.SHA256 {
			return ReportBundle{}, fmt.Errorf("report resource digest mismatch: %s", file.Path)
		}
		bundle.files[file.Path] = bytes.Clone(content)
	}
	if err := json.Unmarshal(bundle.files["assets/profile.json"], &bundle.profile); err != nil || (bundle.profile.API != 1 && bundle.profile.API != 2) {
		return ReportBundle{}, fmt.Errorf("unsupported report profile")
	}
	if !reportRelativePath(bundle.profile.Entrypoint) || len(bundle.files[bundle.profile.Entrypoint]) == 0 || bundle.profile.InputSchema == "" {
		return ReportBundle{}, fmt.Errorf("report profile entrypoint or schema is unavailable")
	}
	return bundle, nil
}

// ReportFonts 由宿主实际字体解析得到；仅声明字体名称时不启用跨环境缓存。
type ReportFonts struct {
	Sans           string `json:"sans"`
	Serif          string `json:"serif"`
	Math           string `json:"math"`
	ManifestDigest string `json:"manifest_digest"`
}

// ReportRenderInput 是渲染边界数据，Document遵循资源包的JSON合同。
// Resources以包入口目录为基准，字节由原资产服务读回；不从文本下载图片。
type ReportRenderInput struct {
	Document  json.RawMessage   `json:"document"`
	Formulas  map[string]string `json:"formulas"`
	Resources map[string][]byte `json:"-"`
	Fonts     ReportFonts       `json:"fonts"`
	Spec      *ReportRenderSpec `json:"-"`
}

func reportRelativePath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.Contains(path, "\\")
}

func reportDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func reportCanonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, fmt.Errorf("encode report data: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func validateReportRenderInput(bundle ReportBundle, input ReportRenderInput) error {
	if bundle.ref.Digest == "" || bundle.profile.ID == "" {
		return fmt.Errorf("a resolved report bundle is required")
	}
	var document map[string]any
	if err := json.Unmarshal(input.Document, &document); err != nil || document["schema_version"] != bundle.profile.InputSchema {
		return fmt.Errorf("report document does not match the selected schema")
	}
	for path := range input.Resources {
		if !reportRelativePath(path) || !strings.HasPrefix(path, "resources/") {
			return fmt.Errorf("report resources require relative resources/ paths")
		}
	}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		case map[string]any:
			if ref, ok := value["math_ref"].(string); ok {
				if strings.TrimSpace(input.Formulas[ref]) == "" {
					return fmt.Errorf("report formula is unavailable: %s", ref)
				}
			}
			for _, field := range []string{"path", "logo_path"} {
				if path, ok := value[field].(string); ok && path != "" && len(input.Resources[path]) == 0 {
					return fmt.Errorf("report image resource is unavailable: %s", path)
				}
			}
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(document)
}
