package dingtalk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
)

func generatedAttachment(raw []byte, name, mime string) adapter.Attachment {
	sum := sha256.Sum256(raw)
	return adapter.Attachment{ID: "asset-fixture", Type: "audio", Name: name, Mime: mime, ContentDigest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(raw)), Source: &adapter.AttachmentSource{OpenReader: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }}}
}

func TestGeneratedImagePartsKeepAssetIdentityAndCanonicalDigest(t *testing.T) {
	raw := testPNGBytes(t)
	attachment := generatedAttachment(raw, "generated.png", "image/png")
	attachment.Type = "image"
	reply := &adapter.Reply{Content: "Generated image.", Attachments: []adapter.Attachment{attachment}}
	parts, err := ProjectDeliveryParts(reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts=%d", len(parts))
	}
	for _, part := range parts {
		if err := validateDingTalkDeliveryPartCanonicalEvidence(part, true); err != nil {
			t.Fatal(err)
		}
	}
	if parts[1].MessageContent.Attachments[0].AssetID != attachment.ID || parts[1].Digest != attachment.ContentDigest {
		t.Fatal("stable media identity was replaced")
	}
}

func TestGeneratedMediaRecoveryStatusCanBeDeliveredWithoutAnAsset(t *testing.T) {
	parts, err := ProjectDeliveryParts(&adapter.Reply{Content: "Generation result is unknown. The original operation is retained."})
	if err != nil || len(parts) != 1 {
		t.Fatalf("recovery projection: %v, parts=%d", err, len(parts))
	}
	if err := validateDingTalkDeliveryPartCanonicalEvidence(parts[0], true); err != nil {
		t.Fatal(err)
	}
	message, err := dingTalkPreparedPartMessage(parts[0])
	if err != nil || message.MsgKey != "sampleText" {
		t.Fatalf("recovery delivery: %+v, %v", message, err)
	}
}

func TestGeneratedAudioUsesFileProtocolWithoutWeakeningPDF(t *testing.T) {
	raw := make([]byte, 44+1600)
	copy(raw, "RIFF")
	binary.LittleEndian.PutUint32(raw[4:], uint32(len(raw)-8))
	copy(raw[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(raw[16:], 16)
	binary.LittleEndian.PutUint16(raw[20:], 1)
	binary.LittleEndian.PutUint16(raw[22:], 1)
	binary.LittleEndian.PutUint32(raw[24:], 8000)
	binary.LittleEndian.PutUint32(raw[28:], 16000)
	binary.LittleEndian.PutUint16(raw[32:], 2)
	binary.LittleEndian.PutUint16(raw[34:], 16)
	copy(raw[36:], "data")
	binary.LittleEndian.PutUint32(raw[40:], 1600)
	attachment := generatedAttachment(raw, "generated.wav", "audio/wav")
	for _, status := range []int{200, 400, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("type") != "file" {
					t.Error("audio file was mislabeled as PDF or voice")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer r.MultipartForm.RemoveAll()
				file, header, err := r.FormFile("media")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				data, err := io.ReadAll(file)
				if err != nil {
					t.Error(err)
				}
				if header.Filename != "generated.wav" || header.Header.Get("Content-Type") != "audio/wav" || !bytes.Equal(data, raw) {
					t.Error("media file bytes or format changed")
				}
				w.WriteHeader(status)
				if status == 200 {
					_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "media_id": "@lAjGeneratedAudioFixture"})
				}
			}))
			defer server.Close()
			api := &officialDingtalkOpenAPI{http: server.Client(), mediaURL: server.URL + "/media/upload"}
			mediaID, err := api.UploadGeneratedFile(t.Context(), "fixture-token", attachment)
			if status == 200 {
				if err != nil || mediaID != "@lAjGeneratedAudioFixture" {
					t.Fatalf("upload=%s err=%v", mediaID, err)
				}
				message := generatedDingTalkFileMessage(mediaID, attachment)
				if message.MsgKey != "sampleFile" {
					t.Fatal("not a file delivery")
				}
				var payload map[string]string
				if err := json.Unmarshal([]byte(message.MsgParam), &payload); err != nil {
					t.Fatal(err)
				}
				if payload["fileType"] != "wav" || payload["fileName"] != "generated.wav" {
					t.Fatal("audio was mislabeled as PDF")
				}
			} else {
				var classified interface{ ResourcePreparationKnown() bool }
				if err == nil || !errors.As(err, &classified) || classified.ResourcePreparationKnown() != (status == 400) {
					t.Fatalf("wrong upload certainty for %d: %v", status, err)
				}
			}
		})
	}
}
