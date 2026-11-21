package k12

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"slices"
)

// RecognitionReviewSheet.Crops 按授权题序，给出每个原裁片在合图中的位置。
type RecognitionReviewSheet struct {
	Width, Height int
	Crops         []SourcePixelRegion
}

// RecognitionReviewBatchSize 供选题分批与落库校验使用同一像素尺寸及容量规则。
func RecognitionReviewBatchSize(plan RecognitionLayoutPlanV2, targets []RecognitionLayoutTargetV2) (width, height int, fits bool) {
	if len(targets) == 0 {
		return 0, 0, false
	}
	if len(targets) == 1 {
		return targets[0].Region.Width, targets[0].Region.Height, true
	}
	if layout := SharedRecognitionReviewLayout(plan, targets); layout != nil {
		return layout.Width, layout.Height, layout.Height <= recognitionLayoutCompactHeightV1
	}
	for _, t := range targets {
		width = max(width, t.Region.Width)
		height += t.Region.Height
	}
	width += 2 * recognitionLayoutContactPaddingV2
	height += 2*recognitionLayoutContactPaddingV2 + (len(targets)-1)*recognitionLayoutContactGapV2
	return width, height, height <= recognitionLayoutCompactHeightV1
}

// SharedRecognitionReviewLayout 只把共同引用同一冻结材料的裁片放入同一坐标系。
// 其余题仍按顺序排列。旧计划和没有共享材料的批次沿原合图合同。
func SharedRecognitionReviewLayout(plan RecognitionLayoutPlanV2, targets []RecognitionLayoutTargetV2) *RecognitionReviewSheet {
	if len(targets) < 2 || plan.ScopeInput == nil || plan.ScopeInput.SharedReviewPolicy != RecognitionSharedReviewSingleView ||
		!plan.ScopeInput.IncludeSharedReviewRegions || plan.Scope == nil || len(plan.Scope.Excluded) != 0 ||
		len(plan.Scope.SelectedOrders) != len(plan.ObservedTargets) {
		return nil
	}
	group := make([]int, len(targets))
	for i := range group {
		group[i] = i
	}
	shared := false
	for _, dep := range plan.Scope.Dependencies {
		first := -1
		for i, target := range targets {
			if !slices.Contains(dep.ForOrders, target.ManifestOrder) {
				continue
			}
			if first < 0 {
				first = i
				continue
			}
			from, to := group[i], group[first]
			if from != to {
				for j := range group {
					if group[j] == from {
						group[j] = to
					}
				}
				shared = true
			}
		}
	}
	if !shared {
		return nil
	}
	panels := make(map[int]image.Rectangle)
	for i, t := range targets {
		r := t.Region
		panels[group[i]] = panels[group[i]].Union(image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height))
	}
	layout := &RecognitionReviewSheet{Crops: make([]SourcePixelRegion, len(targets))}
	tops := make(map[int]int)
	next := recognitionLayoutContactPaddingV2
	for i, t := range targets {
		panel := panels[group[i]]
		top, exists := tops[group[i]]
		if !exists {
			top = next
			tops[group[i]] = top
			next += panel.Dy() + recognitionLayoutContactGapV2
			layout.Width = max(layout.Width, panel.Dx()+2*recognitionLayoutContactPaddingV2)
			layout.Height = top + panel.Dy() + recognitionLayoutContactPaddingV2
		}
		r := t.Region
		r.X += recognitionLayoutContactPaddingV2 - panel.Min.X
		r.Y += top - panel.Min.Y
		layout.Crops[i] = r
	}
	return layout
}

func (s *RecognitionReviewSheet) render(page image.Image, targets []RecognitionLayoutTargetV2) ([]byte, error) {
	sheet := image.NewRGBA(image.Rect(0, 0, s.Width, s.Height))
	draw.Draw(sheet, sheet.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	// 仅复制已授权裁片的并集，外接矩形中的其它原图像素保持空白。
	for i, r := range s.Crops {
		src := targets[i].Region
		draw.Draw(sheet, image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height), page, image.Pt(src.X, src.Y), draw.Src)
	}
	var result bytes.Buffer
	if err := png.Encode(&result, sheet); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}
