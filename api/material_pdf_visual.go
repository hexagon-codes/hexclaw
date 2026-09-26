package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"

	"github.com/hexagon-codes/hexclaw/knowledge"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// NewMaterialPDFSourcePreparer 复用当前原文件、页渲染和内容寻址对象，不修改解析正文。
func NewMaterialPDFSourcePreparer(service *knowledge.SemanticIndexService, records *k12storage.Store) func(context.Context, k12storage.MaterialPreparation) ([]byte, string, error) {
	return func(ctx context.Context, p k12storage.MaterialPreparation) ([]byte, string, error) {
		if data, digest, found, err := records.LoadMaterialPDFSourceImage(ctx, p); found || err != nil {
			return data, digest, err
		}
		corpus, digest, err := records.MaterialPDFSourceIdentity(ctx, p)
		if err != nil {
			return nil, "", err
		}
		file, source, err := service.OpenDocumentSource(ctx, p.OwnerID, corpus, p.DocumentID)
		if err != nil {
			return nil, "", err
		}
		defer file.Close()
		if source.ContentGeneration != p.SourceRevision || source.SHA256 != digest || source.Extension != ".pdf" {
			return nil, "", k12storage.ErrMaterialPreparationFenced
		}
		hash := sha256.New()
		if _, err = io.Copy(hash, file); err != nil {
			return nil, "", err
		}
		if hex.EncodeToString(hash.Sum(nil)) != digest {
			return nil, "", k12storage.ErrProblemAssetEvidence
		}
		receipt := k12storage.MaterialPDFVisualSource{InputDigest: p.InputDigest, SourceDigest: digest, SourceRevision: p.SourceRevision, Pages: append([]int(nil), p.Candidate.VisualPDFPages...), DPI: 150, Layout: "vertical-source-order-v1"}
		var images []image.Image
		var encoded [][]byte
		width, height := 0, 0
		for _, page := range receipt.Pages {
			rendered, e := renderPDFPageBatch(ctx, source.StoragePath, page, page, receipt.DPI, 32<<20)
			if e != nil {
				return nil, "", e
			}
			if len(rendered) != 1 || rendered[0].Page != page || rendered[0].Err != nil {
				return nil, "", errors.New("Source PDF page is unavailable")
			}
			img, e := png.Decode(bytes.NewReader(rendered[0].Data))
			if e != nil {
				return nil, "", e
			}
			images = append(images, img)
			encoded = append(encoded, rendered[0].Data)
			if img.Bounds().Dx() > width {
				width = img.Bounds().Dx()
			}
			height += img.Bounds().Dy()
		}
		if len(images) == 0 {
			return nil, "", k12storage.ErrProblemAssetEvidence
		}
		composite := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.Draw(composite, composite.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		y := 0
		for _, img := range images {
			draw.Draw(composite, image.Rect(0, y, img.Bounds().Dx(), y+img.Bounds().Dy()), img, img.Bounds().Min, draw.Src)
			y += img.Bounds().Dy()
		}
		var combined bytes.Buffer
		if err = png.Encode(&combined, composite); err != nil {
			return nil, "", err
		}
		encoded = append(encoded, combined.Bytes())
		var releases []func()
		defer func() {
			for _, release := range releases {
				release()
			}
		}()
		// 相同字节只持有一次对象锁，避免单页图与合成图相同时重复加锁。
		persisted := map[string]knowledge.IngestBlob{}
		for i, data := range encoded {
			h := sha256.Sum256(data)
			key := hex.EncodeToString(h[:])
			blob, exists := persisted[key]
			if !exists {
				var release func()
				blob, release, err = service.PersistSourceAttachment(ctx, source, "image/png", bytes.NewReader(data))
				if err != nil {
					return nil, "", err
				}
				releases = append(releases, release)
				persisted[key] = blob
			}
			o := k12storage.MaterialPDFVisualObject{Digest: blob.SHA256, StoragePath: blob.StoragePath, SizeBytes: blob.SizeBytes, MediaType: blob.MediaType}
			if i < len(receipt.Pages) {
				o.Page = receipt.Pages[i]
			}
			receipt.Objects = append(receipt.Objects, o)
		}
		receipt.CompositeDigest = receipt.Objects[len(receipt.Objects)-1].Digest
		if err = records.SaveMaterialPDFVisualSource(ctx, p, receipt); err != nil {
			return nil, "", err
		}
		return combined.Bytes(), receipt.CompositeDigest, nil
	}
}
