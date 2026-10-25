package k12storage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestPrintArtifactRenderSpecAndLayoutSurviveReadback(t *testing.T) {
	store, _ := setup(t)
	ctx := context.Background()
	for _, state := range []string{"passed", "not_run"} {
		spec := `{"version":"report-render-spec-v1","profile_id":"k12-homework-report/v2","engine":"test-engine","fonts":{"source":"test-fixture"}}`
		specHash := sha256.Sum256([]byte(spec))
		digest := hex.EncodeToString(specHash[:])
		artifact := k12.PrintArtifact{ArtifactID: "part-render-" + state, AgentName: "mingming", SourceKind: k12.PrintSourceTutoringTips, SourceRef: "report:" + state, Title: "报告", CanonicalMarkdown: "# 报告", SourceDigest: strings.Repeat("a", 64), CreatedAt: 100}
		if err := json.Unmarshal([]byte(`{"render_spec_digest":"`+digest+`"}`), &artifact); err != nil {
			t.Fatal(err)
		}
		payload := []byte("%PDF-1.7\nimmutable-" + state)
		pdfHash := sha256.Sum256(payload)
		file := k12.PrintArtifactRender{ArtifactID: artifact.ArtifactID, Format: "pdf", RenderContractVersion: "report-spec-v1", ContentType: "application/pdf", ByteDigest: hex.EncodeToString(pdfHash[:]), ByteSize: int64(len(payload)), Payload: payload, CreatedAt: 100}
		extra, _ := json.Marshal(map[string]string{"render_spec_digest": digest, "render_spec_json": spec, "layout_check": state})
		if err := json.Unmarshal(extra, &file); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := store.FreezePrintArtifact(ctx, artifact, file); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetPrintArtifact(ctx, "mingming", artifact.ArtifactID)
		if err != nil {
			t.Fatal(err)
		}
		read, err := store.GetPrintArtifactRender(ctx, "mingming", artifact.ArtifactID)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []any{got, read} {
			raw, _ := json.Marshal(value)
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			if fields["render_spec_digest"] != digest {
				t.Fatalf("resolved spec lost after storage read: %s", raw)
			}
		}
		raw, _ := json.Marshal(read)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		if fields["layout_check"] != state || fields["render_spec_json"] != spec {
			t.Fatalf("render check/spec lost after read: %s", raw)
		}
		if !bytes.Equal(read.Payload, payload) {
			t.Fatal("readback changed frozen PDF bytes")
		}
	}
}
