package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ResolvePrintReplyArtifact 复核当前 Agent 的冻结 PDF，返回独立只读附件。
func (d Deps) ResolvePrintReplyArtifact(ctx context.Context, agent, id string) (k12.PrintReplyArtifact, FrozenReplyAttachment, error) {
	v, err := d.GetPrintableArtifactPDF(ctx, agent, id)
	if err != nil {
		return k12.PrintReplyArtifact{}, FrozenReplyAttachment{}, err
	}
	raw := bytes.Clone(v.Render.Payload)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if v.Artifact.AgentName != agent || v.Artifact.ArtifactID != id || v.Render.ArtifactID != id || v.Render.Format != "pdf" || v.Render.ContentType != "application/pdf" || len(raw) == 0 || int64(len(raw)) != v.Render.ByteSize || digest != v.Render.ByteDigest {
		return k12.PrintReplyArtifact{}, FrozenReplyAttachment{}, fmt.Errorf("frozen PDF identity or digest mismatch")
	}
	_, name := render.SanitizeFilename(strings.ReplaceAll(v.Artifact.Title, "/", "-"), "pdf")
	ref := k12.PrintReplyArtifact{Kind: k12.PrintReplyArtifactKind, AgentName: agent, ArtifactID: id, SourceDigest: v.Artifact.SourceDigest, ByteDigest: digest, ByteSize: int64(len(raw)), Name: name, URL: "/api/k12/print-artifacts/" + url.PathEscape(id) + "/content?agent=" + url.QueryEscape(agent)}
	a := FrozenReplyAttachment{DeliveryAttachmentIdentity: DeliveryAttachmentIdentity{Name: name, MIME: "application/pdf", ContentDigest: "sha256:" + digest}, Size: int64(len(raw)), OpenReader: func(readCtx context.Context) (io.ReadCloser, error) {
		if err := readCtx.Err(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(raw)), nil
	}}
	return ref, a, nil
}
