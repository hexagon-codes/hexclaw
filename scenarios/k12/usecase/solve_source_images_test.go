package usecase

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestPhotoSolveSourceImagesKeepSharedDiagramWithinFrozenScope(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	d, store := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
	source := image.NewNRGBA(image.Rect(0, 0, 12, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 12; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 10), G: uint8(y * 10), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	ready, err := (&PageAssetRepository{Records: store}).Persist(t.Context(), "source-owner", "mingming", encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	questionRegion := k12.SourcePixelRegion{X: 1, Y: 7, Width: 10, Height: 2}
	diagramRegion := k12.SourcePixelRegion{X: 1, Y: 1, Width: 10, Height: 4}
	q := RecognizedQuestion{PageAssetID: ready.Metadata.PageAssetID, SourceWidth: 12, SourceHeight: 10,
		SourceRegion: &questionRegion, InputDigest: strings.Repeat("a", 64), RawTranscription: "将两圆按图②放置，求面积。",
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{ScopeDigest: "scope-frozen",
			Materials: []k12.RecognitionImageScopeDependencyV1{
				{Region: diagramRegion, Text: "1. 如图，大圆半径3，小圆直径4。"},
				{Region: k12.SourcePixelRegion{Width: 12, Height: 10}, Text: "如图，大圆半径3，小圆直径4。"},
			}},
	}
	before := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	ctx := skill.WithInputImages(skill.WithRoutedAgent(t.Context(), "mingming"), []skill.InputImage{{ID: "unrelated"}})
	ctx, err = d.withPhotoSolveSourceImages(ctx, "source-owner", q)
	if err != nil {
		t.Fatal(err)
	}
	images := skill.InputImages(ctx)
	if len(images) != 2 {
		t.Fatalf("current target and shared diagram images=%d, want 2", len(images))
	}
	for i, region := range []k12.SourcePixelRegion{questionRegion, diagramRegion} {
		raw, err := base64.StdEncoding.DecodeString(images[i].Data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got.Bounds().Dx() != region.Width || got.Bounds().Dy() != region.Height ||
			color.NRGBAModel.Convert(got.At(0, 0)) != color.NRGBAModel.Convert(source.At(region.X, region.Y)) {
			t.Fatalf("image %d lost authorized source pixels: bounds=%v region=%+v", i, got.Bounds(), region)
		}
		if !strings.HasPrefix(images[i].ID, q.PageAssetID+"#region=") || !strings.HasSuffix(images[i].ID, "&scope=scope-frozen") {
			t.Fatalf("image %d lost source identity: %s", i, images[i].ID)
		}
	}
	if !reflect.DeepEqual(q, before) {
		t.Fatal("visual transport changed frozen source or input digest")
	}
	legacy, err := d.withPhotoSolveSourceImages(ctx, "source-owner", RecognizedQuestion{Question: "纯文字题"})
	if err != nil || len(skill.InputImages(legacy)) != 0 {
		t.Fatalf("text request inherited unrelated images: %v", err)
	}
}
