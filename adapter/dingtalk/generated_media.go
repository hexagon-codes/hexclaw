package dingtalk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
)

func isDingTalkGeneratedFile(a adapter.Attachment) bool {
	return a.Source != nil && (strings.HasPrefix(a.Mime, "audio/") || strings.HasPrefix(a.Mime, "video/"))
}

// ProjectDeliveryParts 复用原渠道投影，冻结逐段准备和回执所需的同一规范内容。
func ProjectDeliveryParts(reply *adapter.Reply) ([]adapter.DeliveryPart, error) {
	if err := ensureDingTalkRenderEvidence(reply); err != nil {
		return nil, err
	}
	var parts []adapter.DeliveryPart
	for index, render := range reply.RenderManifest.Parts {
		part := adapter.DeliveryPart{Kind: render.Kind, Ordinal: index + 1, MessageContent: reply.MessageContent, RenderManifest: reply.RenderManifest, Text: render.Text, Digest: render.ArtifactDigest}
		if render.Kind == messagecontent.PartMarkdown || render.Kind == messagecontent.PartText {
			sum := sha256.Sum256([]byte(reply.MessageContent.Markdown))
			part.Digest = "sha256:" + hex.EncodeToString(sum[:])
		}
		if render.Kind == messagecontent.PartArtifact {
			found := false
			for i, ref := range reply.MessageContent.Attachments {
				if ref.AssetID == render.ArtifactRef {
					copyAttachment := reply.Attachments[i]
					part.Attachment = &copyAttachment
					part.MIME = copyAttachment.Mime
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("generated media part is missing its attachment")
			}
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func generatedMediaIdentity(a adapter.Attachment) (string, string, error) {
	if a.Source == nil || a.Source.OpenReader == nil || a.ID == "" {
		return "", "", fmt.Errorf("generated media source is unavailable")
	}
	r, err := a.Source.OpenReader(context.Background())
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, r)
	err = errors.Join(err, r.Close())
	if err != nil {
		return "", "", err
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if digest != a.ContentDigest {
		return "", "", fmt.Errorf("generated media digest mismatch")
	}
	return digest, a.ID, nil
}

type dingtalkGeneratedMediaOpenAPI interface {
	UploadGeneratedFile(context.Context, string, adapter.Attachment) (string, error)
}

func (c *officialDingtalkOpenAPI) UploadGeneratedFile(ctx context.Context, token string, attachment adapter.Attachment) (mediaID string, resultErr error) {
	started, known := false, false
	defer func() {
		if resultErr != nil {
			resultErr = &adapter.ResourcePreparationError{Known: !started || known, Cause: errors.New("DingTalk generated media upload failed")}
		}
	}()
	if !isDingTalkGeneratedFile(attachment) {
		return "", fmt.Errorf("generated media file source is required")
	}
	if err := validateDingTalkAttachmentName(attachment.Name, false); err != nil {
		return "", err
	}
	if _, _, err := generatedMediaIdentity(attachment); err != nil {
		return "", err
	}
	reader, err := attachment.Source.OpenReader(ctx)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	// 使用可回收临时文件组装multipart，避免整段视频驻留内存。
	body, err := os.CreateTemp("", "hexclaw-media-upload-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close(); _ = os.Remove(body.Name()) }()
	writer := multipart.NewWriter(body)
	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="media"; filename="%s"`, attachment.Name))
	headers.Set("Content-Type", attachment.Mime)
	part, err := writer.CreatePart(headers)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, reader); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	info, err := body.Stat()
	if err != nil {
		return "", err
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	endpoint, err := url.Parse(c.mediaURL)
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("access_token", token)
	query.Set("type", "file")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return "", err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	started = true
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		known = isDingTalkDefiniteProviderRejectionStatus(response.StatusCode)
		return "", fmt.Errorf("DingTalk media upload returned HTTP %d", response.StatusCode)
	}
	var result dingtalkMediaUploadResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if result.ErrCode != 0 {
		known = true
		return "", fmt.Errorf("DingTalk media upload was rejected")
	}
	if !validDingTalkFileMediaID(result.MediaID) {
		return "", fmt.Errorf("DingTalk media upload returned no valid media id")
	}
	return result.MediaID, nil
}

func generatedDingTalkFileMessage(mediaID string, a adapter.Attachment) dingtalkOutboundMessage {
	encoded, _ := json.Marshal(map[string]string{"mediaId": mediaID, "fileName": a.Name, "fileType": strings.TrimPrefix(strings.ToLower(filepath.Ext(a.Name)), ".")})
	return dingtalkOutboundMessage{MsgKey: "sampleFile", MsgParam: string(encoded)}
}
