package usecase

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

// withPhotoSolveSourceImages 只消费当前题及其冻结公共材料的来源像素，不继承其它请求图片。
func (d Deps) withPhotoSolveSourceImages(ctx context.Context, owner string, q RecognizedQuestion) (context.Context, error) {
	ctx = skill.WithInputImages(ctx, nil)
	if d.Records == nil || q.PageAssetID == "" || q.SourceRegion == nil || q.SourceWidth <= 0 || q.SourceHeight <= 0 {
		return ctx, nil
	}
	if owner == "" {
		return ctx, fmt.Errorf("%w: solve source owner is unavailable", ErrInvalidInput)
	}
	repository := PageAssetRepository{Records: d.Records}
	ready, err := repository.OpenReady(ctx, owner, skill.RoutedAgentName(ctx), q.PageAssetID)
	if err != nil {
		return ctx, fmt.Errorf("read solve source image: %w", err)
	}
	if ready.Metadata.PixelWidth != q.SourceWidth || ready.Metadata.PixelHeight != q.SourceHeight {
		return ctx, fmt.Errorf("%w: solve source geometry does not match frozen input", ErrProblemSourceImageInvalid)
	}
	regions := []k12.SourcePixelRegion{*q.SourceRegion}
	scope := ""
	if source := q.ImageSourceContext; source != nil {
		scope = source.ScopeDigest
		for i, material := range source.Materials {
			broaderDuplicate := false
			for j, other := range source.Materials {
				if i != j && other.Region.Width*other.Region.Height < material.Region.Width*material.Region.Height &&
					containsSolveSourceRegion(material.Region, other.Region) && strings.TrimSpace(material.Text) != "" &&
					strings.Contains(strings.TrimSpace(other.Text), strings.TrimSpace(material.Text)) {
					broaderDuplicate = true
					break
				}
			}
			if !broaderDuplicate {
				regions = append(regions, material.Region)
			}
		}
	}
	seen := map[k12.SourcePixelRegion]bool{}
	images := make([]skill.InputImage, 0, len(regions))
	for _, region := range regions {
		if seen[region] {
			continue
		}
		seen[region] = true
		image, err := normalizeProblemSourceOCRImage(ready, &region)
		if err != nil {
			return ctx, fmt.Errorf("prepare solve source pixels: %w", err)
		}
		images = append(images, skill.InputImage{
			ID:   fmt.Sprintf("%s#region=%d,%d,%d,%d&scope=%s", q.PageAssetID, region.X, region.Y, region.Width, region.Height, scope),
			Name: fmt.Sprintf("problem-source-%d.png", len(images)+1), MIME: "image/png", Data: base64.StdEncoding.EncodeToString(image.Data),
		})
	}
	return skill.WithInputImages(ctx, images), nil
}

func containsSolveSourceRegion(outer, inner k12.SourcePixelRegion) bool {
	return outer.X <= inner.X && outer.Y <= inner.Y && outer.X+outer.Width >= inner.X+inner.Width && outer.Y+outer.Height >= inner.Y+inner.Height
}

func (o *GradingOrchestrator) withGradingSolveSourceImages(ctx context.Context, job GradingJobView, q RecognizedQuestion) (context.Context, error) {
	ctx = skill.WithInputImages(ctx, nil)
	if job.Fields.SourceKind != "image_task" || q.PageAssetID == "" || q.SourceRegion == nil || q.SourceWidth <= 0 || q.SourceHeight <= 0 {
		return ctx, nil
	}
	owner, err := resolveGradingGroundingTextbookOwner(ctx, o.deps, job)
	if err != nil {
		return ctx, err
	}
	ctx = skill.WithRoutedAgent(ctx, job.Record.AgentName)
	return o.deps.withPhotoSolveSourceImages(ctx, owner, q)
}
