package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// TeachingReportResourceReader 复用共享资源读取合同，不处理安装、启用或注册。
type TeachingReportResourceReader interface {
	ReadResource(name, path string) (marketplace.Resource, error)
}

// TeachingReportBundleSnapshot 可随原任务快照保存，恢复不读取已变更的安装目录。
// 文件只包含本报告选择的静态资源，不承载学生图片或新的资源注册状态。
type TeachingReportBundleSnapshot struct {
	Reference   render.ReportBundleRef `json:"reference"`
	InputSchema string                 `json:"input_schema"`
	Manifest    []byte                 `json:"manifest"`
	Resources   map[string][]byte      `json:"resources"`
}

// FreezeTeachingReportBundle 核对同一清单的版本与字节，返回独立持有的任务资源。
func FreezeTeachingReportBundle(ctx context.Context, reader TeachingReportResourceReader) (TeachingReportBundleSnapshot, error) {
	if reader == nil {
		return TeachingReportBundleSnapshot{}, fmt.Errorf("%w: report resources are unavailable", ErrRenderUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return TeachingReportBundleSnapshot{}, err
	}
	root, err := reader.ReadResource("homework-pdf", "bundle.json")
	if err != nil {
		return TeachingReportBundleSnapshot{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(root.Content, &manifest); err != nil {
		return TeachingReportBundleSnapshot{}, fmt.Errorf("decode report manifest: %w", err)
	}
	if manifest.SkillID != "homework-pdf" || root.SkillID != manifest.SkillID || root.Version != manifest.Version ||
		root.Path != "bundle.json" || root.Size != int64(len(root.Content)) || root.SHA256 != teachingReportResourceDigest(root.Content) {
		return TeachingReportBundleSnapshot{}, fmt.Errorf("report manifest resource identity mismatch")
	}
	digest, err := manifest.SemanticDigest()
	if err != nil {
		return TeachingReportBundleSnapshot{}, err
	}
	snapshot := TeachingReportBundleSnapshot{
		Reference: render.ReportBundleRef{SkillID: manifest.SkillID, Version: manifest.Version, Digest: digest},
		Manifest:  bytes.Clone(root.Content), Resources: make(map[string][]byte, len(manifest.Files)),
	}
	for _, entry := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return TeachingReportBundleSnapshot{}, err
		}
		resource, err := reader.ReadResource(manifest.SkillID, entry.Path)
		if err != nil {
			return TeachingReportBundleSnapshot{}, err
		}
		if resource.SkillID != manifest.SkillID || resource.Version != manifest.Version || resource.Path != entry.Path ||
			resource.Size != entry.Size || int64(len(resource.Content)) != entry.Size ||
			resource.SHA256 != entry.SHA256 || teachingReportResourceDigest(resource.Content) != entry.SHA256 {
			return TeachingReportBundleSnapshot{}, fmt.Errorf("report resource identity mismatch: %s", entry.Path)
		}
		snapshot.Resources[entry.Path] = bytes.Clone(resource.Content)
	}
	bundle, err := render.LoadReportBundleSnapshot(snapshot.Manifest, snapshot.Resources, snapshot.Reference.Digest)
	if err != nil {
		return TeachingReportBundleSnapshot{}, err
	}
	if bundle.Ref() != snapshot.Reference {
		return TeachingReportBundleSnapshot{}, fmt.Errorf("report bundle reference mismatch")
	}
	snapshot.InputSchema = bundle.InputSchema()
	return snapshot, nil
}

func teachingReportResourceDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Resolve 只校验并加载已保存字节，不刷新资源或提升为当前已启用版本。
func (s TeachingReportBundleSnapshot) Resolve() (render.ReportBundle, error) {
	bundle, err := render.LoadReportBundleSnapshot(s.Manifest, s.Resources, s.Reference.Digest)
	if err != nil {
		return render.ReportBundle{}, err
	}
	if bundle.Ref() != s.Reference || bundle.InputSchema() != s.InputSchema {
		return render.ReportBundle{}, fmt.Errorf("frozen report bundle identity mismatch")
	}
	return bundle, nil
}
