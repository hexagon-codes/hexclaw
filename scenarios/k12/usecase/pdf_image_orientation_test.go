package usecase

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestPDFImageRotationKeepsWholeAnnotatedPixels(t *testing.T) {
	original := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			original.SetNRGBA(x, y, color.NRGBA{R: uint8(10 + y*3 + x), A: 255})
		}
	}
	original.SetNRGBA(2, 1, color.NRGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, original); err != nil {
		t.Fatal(err)
	}
	for _, angle := range []int{0, 90, 180, 270} {
		data, err := rotatePDFImage(encoded.Bytes(), angle)
		if err != nil {
			t.Fatal(err)
		}
		got, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < 2; y++ {
			for x := 0; x < 3; x++ {
				dx, dy := x, y
				switch angle {
				case 90:
					dx, dy = 1-y, x
				case 180:
					dx, dy = 2-x, 1-y
				case 270:
					dx, dy = y, 2-x
				}
				if color.NRGBAModel.Convert(got.At(dx, dy)) != original.NRGBAAt(x, y) {
					t.Fatalf("%d degrees lost original/annotation pixel (%d,%d)", angle, x, y)
				}
			}
		}
	}
}
