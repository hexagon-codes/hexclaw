package usecase

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func learningArchiveTestPNG(t *testing.T, pixel color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.SetRGBA(1, 1, pixel)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func seedLearningArchiveImageWork(t *testing.T, d Deps, command, workType, assetID string) {
	t.Helper()
	rec, err := k12.NewCreativeWorkRecord("mingming", "", k12.CreativeWorkFields{
		GradeTerm: "五年级上", WorkType: workType, WorkTitle: "图片作品",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := d.Records.CreateCreativeWorkWithInitialGeneration(t.Context(), rec, command, command,
		k12.CreativeWorkSourceSnapshot{WorkType: workType, SourceAssetID: assetID, ContentMarkdown: "原稿正文"})
	if err != nil || !created {
		t.Fatalf("seed current image work: created=%v err=%v", created, err)
	}
}

func TestLearningArchiveAttachmentsKeepOriginalBytesAndFrozenIdentity(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, &fakeInsights{})
	data := learningArchiveTestPNG(t, color.RGBA{R: 200, G: 40, B: 10, A: 255})
	id, _, err := assetstore.Ensure("mingming", data)
	if err != nil {
		t.Fatal(err)
	}
	seedLearningArchiveImageWork(t, d, "archive-art", k12.WorkTypeArt, id)
	seedLearningArchiveImageWork(t, d, "archive-writing", k12.WorkTypeWriting, id)

	first, err := d.ExportLearningArchiveMarkdown(t.Context(), "mingming")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Attachments) != 1 || first.ObjectCounts.CreativeWorks != 2 {
		t.Fatalf("shared image was not packed once: attachments=%d counts=%+v", len(first.Attachments), first.ObjectCounts)
	}
	attachment := first.Attachments[0]
	sum := sha256.Sum256(data)
	wantSHA := hex.EncodeToString(sum[:])
	wantPath := "attachments/" + wantSHA + ".png"
	if attachment.RelativePath != wantPath || attachment.SHA256 != wantSHA ||
		attachment.MediaType != "image/png" || attachment.ByteSize != int64(len(data)) {
		t.Fatalf("original attachment metadata=%+v", attachment)
	}
	decoded, err := base64.StdEncoding.DecodeString(attachment.DataBase64)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("original attachment bytes changed: %v", err)
	}
	if strings.Count(first.CanonicalMarkdown, "![原图]("+wantPath+")") != 2 ||
		strings.Contains(first.CanonicalMarkdown, "asset:") || strings.Contains(first.CanonicalMarkdown, "data:image") {
		t.Fatalf("canonical relative references changed: %q", first.CanonicalMarkdown)
	}
	canonical := first.CanonicalMarkdown
	projected := LearningArchiveRenderMarkdown(first)
	if strings.Count(projected, "](data:image/png;base64,"+attachment.DataBase64+")") != 2 ||
		first.CanonicalMarkdown != canonical {
		t.Fatal("render projection did not use the same original bytes or mutated the frozen text")
	}
	second, err := d.ExportLearningArchiveMarkdown(t.Context(), "mingming")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("same-source replay changed archive: %v", err)
	}
	var count int
	if err := d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_print_artifacts WHERE agent_name=? AND source_kind=?`,
		"mingming", k12.PrintSourceLearningArchive).Scan(&count); err != nil || count != 1 {
		t.Fatalf("frozen archive count=%d want=1: %v", count, err)
	}
}

func TestLearningArchiveRejectsIncompleteOriginalAttachmentsBeforeFreezing(t *testing.T) {
	for _, failure := range []string{"missing", "wrong-owner", "changed-bytes"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
			d, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, &fakeInsights{})
			owner := "mingming"
			if failure == "wrong-owner" {
				owner = "eval-agent"
			}
			data := learningArchiveTestPNG(t, color.RGBA{R: 200, A: 255})
			id, _, err := assetstore.Ensure(owner, data)
			if err != nil {
				t.Fatal(err)
			}
			seedLearningArchiveImageWork(t, d, "archive-invalid-image", k12.WorkTypeArt, id)
			path, err := assetstore.PathFromID(id)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing":
				err = os.Remove(path)
			case "changed-bytes":
				err = os.WriteFile(path, learningArchiveTestPNG(t, color.RGBA{B: 180, A: 255}), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.ExportLearningArchiveMarkdown(t.Context(), "mingming"); err == nil {
				t.Fatalf("%s attachment was silently omitted", failure)
			}
			var count int
			if err := d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_print_artifacts WHERE source_kind=?`,
				k12.PrintSourceLearningArchive).Scan(&count); err != nil || count != 0 {
				t.Fatalf("incomplete source froze %d artifacts: %v", count, err)
			}
		})
	}
}

func TestLearningArchiveFeedbackHeadingIsSeparateFromMetadata(t *testing.T) {
	body := "## 可见证据\n\n- 天空有暖色云朵。\n\n## 建议\n\n保留主体。\n"
	snapshot := k12storage.LearningArchiveSourceSnapshot{
		CreativeWorks: []k12storage.LearningArchiveCreativeWork{{
			Record: &records.AgentRecord{Status: k12.WorkStatusFeedbackReady},
			Fields: k12.CreativeWorkFields{WorkTitle: "我的作品"},
			Initial: &k12.WorkFeedbackGeneration{Source: k12.CreativeWorkSourceSnapshot{
				WorkType: k12.WorkTypeWriting, ContentMarkdown: "作品正文",
			}},
			Latest: &k12.WorkFeedbackGeneration{Feedback: &k12.WorkFeedback{ProjectionMarkdown: body}},
		}},
	}
	markdown, err := renderLearningArchiveMarkdown(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown, "\n\n"+body) || !strings.Contains(markdown, "- 标题：我的作品\n\n作品正文") {
		t.Fatalf("first feedback heading attached to metadata or body changed: %q", markdown)
	}
}
