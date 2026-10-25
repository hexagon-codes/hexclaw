package main

import (
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// validateReportSkillBundle 在共享安装提交前核对报告宿主可消费的资源合同。
// 普通技能及其他资源包继续使用各自既有合同。
func validateReportSkillBundle(directory string, manifest marketplace.BundleManifest) error {
	if manifest.SkillID != "homework-pdf" {
		return nil
	}
	digest, err := manifest.SemanticDigest()
	if err != nil {
		return err
	}
	bundle, err := render.LoadReportBundle(directory, digest)
	if err != nil {
		return err
	}
	return bundle.ValidateHostCompatibility()
}
