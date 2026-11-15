package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// GradingTeachingReportOriginalImage 校验同一冻结报告的真实原图，不赋予其批注图身份。
func GradingTeachingReportOriginalImage(artifact k12.GradingFinalArtifact, source DeliveryAttachment) (DeliveryAttachment, error) {
	if artifact.HasAnnotatedAsset() || artifact.ReportSnapshotJSON == "" {
		return DeliveryAttachment{}, fmt.Errorf("grading report has no original-image fallback")
	}
	if err := artifact.Validate(); err != nil {
		return DeliveryAttachment{}, err
	}
	var snapshot TeachingReportFrozenSnapshot
	if err := json.Unmarshal([]byte(artifact.ReportSnapshotJSON), &snapshot); err != nil {
		return DeliveryAttachment{}, err
	}
	canonical, err := TeachingReportMarkdown(snapshot.Report)
	if err != nil || canonical != artifact.CanonicalMarkdown || snapshot.Report.Source.Kind != k12.TeachingReportSourceGrading ||
		snapshot.Report.Source.SourceID != artifact.JobID || len(snapshot.Report.SourcePages) != 1 {
		return DeliveryAttachment{}, fmt.Errorf("grading report original source is invalid")
	}
	page := snapshot.Report.SourcePages[0]
	if page.AssetDigest != page.SourceDigest || len(source.Data) == 0 {
		return DeliveryAttachment{}, fmt.Errorf("grading report original image identity mismatch")
	}
	decoded, format, err := image.Decode(bytes.NewReader(source.Data))
	if err != nil {
		return DeliveryAttachment{}, fmt.Errorf("decode grading report original image: %w", err)
	}
	mimeByFormat := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}
	mime := mimeByFormat[format]
	if mime == "" || (source.MIME != "" && strings.ToLower(strings.TrimSpace(source.MIME)) != mime) {
		return DeliveryAttachment{}, fmt.Errorf("grading report original image MIME mismatch")
	}
	if teachingReportResourceDigest(source.Data) != page.AssetDigest {
		return DeliveryAttachment{}, fmt.Errorf("grading report original image digest mismatch")
	}
	if mime != page.MIME || decoded.Bounds().Dx() != page.Width || decoded.Bounds().Dy() != page.Height {
		return DeliveryAttachment{}, fmt.Errorf("grading report original image geometry mismatch")
	}
	extension := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mime]
	return DeliveryAttachment{Name: "原图" + extension, MIME: mime, Data: append([]byte(nil), source.Data...)}, nil
}
