package usecase

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"log/slog"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 只对同源的整张合成图派生PDF展示副本，原图、批注坐标与冻结资产不变。
func (d Deps) gradingFinalPDFMedia(ctx context.Context, artifact k12.GradingFinalArtifact, source, media DeliveryAttachment) (DeliveryAttachment, error) {
	invocations, err := d.Records.ListModelInvocations(ctx, artifact.AgentName, artifact.JobID)
	if err != nil {
		return media, nil
	}
	var recognition *k12.ModelInvocation
	for i := range invocations {
		candidate := &invocations[i]
		if candidate.Stage == k12.GradingStageRecognizing && candidate.Status == k12.ModelInvocationSucceeded &&
			(recognition == nil || candidate.Attempt > recognition.Attempt) {
			recognition = candidate
		}
	}
	if recognition == nil {
		return media, nil
	}
	runtime, err := d.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, artifact.AgentName, recognition.InvocationID)
	if err != nil || runtime.AuthorizedPlan == nil {
		return media, nil
	}
	orientation := runtime.AuthorizedPlan.PageOrientation
	if orientation.Rotation() == 0 {
		return media, nil
	}
	page, err := k12.CanonicalizeRecognitionPageV2(source.Data)
	if err != nil || page.Digest != orientation.PageDigest {
		slog.WarnContext(ctx, "PDF page orientation source does not match", "artifact_id", artifact.ArtifactID)
		return media, nil
	}
	data, err := rotatePDFImage(media.Data, orientation.Rotation())
	if err != nil {
		return DeliveryAttachment{}, err
	}
	media.Data, media.MIME = data, "image/png"
	slog.InfoContext(ctx, "PDF page image oriented", "artifact_id", artifact.ArtifactID, "clockwise_degrees", orientation.Rotation())
	return media, nil
}

func rotatePDFImage(data []byte, clockwise int) ([]byte, error) {
	if clockwise == 0 {
		return data, nil
	}
	if clockwise != 90 && clockwise != 180 && clockwise != 270 {
		return nil, fmt.Errorf("unsupported PDF image rotation: %d", clockwise)
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode PDF image: %w", err)
	}
	bounds := source.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	wOut, hOut := w, h
	if clockwise != 180 {
		wOut, hOut = h, w
	}
	rotated := image.NewNRGBA(image.Rect(0, 0, wOut, hOut))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := x, y
			switch clockwise {
			case 90:
				dx, dy = h-1-y, x
			case 180:
				dx, dy = w-1-x, h-1-y
			case 270:
				dx, dy = y, w-1-x
			}
			rotated.Set(dx, dy, source.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, rotated); err != nil {
		return nil, fmt.Errorf("encode PDF image: %w", err)
	}
	return encoded.Bytes(), nil
}
