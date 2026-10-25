package usecase

import (
	"encoding/json"
	"strings"
	"testing"
)

// 渲染环境变化只建立新的打印身份，旧请求的身份不随时间变化。
func TestPrintArtifactRenderSpecSeparatesNewIdentityAndPreservesLegacy(t *testing.T) {
	legacy := PreparePrintableArtifactRequest{AgentName: "mingming", SourceKind: "grading_final_artifact", SourceRef: "final_artifact:final:source:teaching-report:bundle", Title: "报告", CanonicalMarkdown: "# 报告\n答案：2"}
	old := buildPrintArtifact(legacy, 100)
	if old.ArtifactID != "part-1a10829aa2a3e6248c320f74" {
		t.Fatalf("pre-spec fixture identity changed: %s", old.ArtifactID)
	}
	if old.ArtifactID != buildPrintArtifact(legacy, 200).ArtifactID {
		t.Fatal("legacy identity changed with rendering time")
	}
	ids := map[string]bool{old.ArtifactID: true}
	for _, digest := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		request := legacy
		if err := json.Unmarshal([]byte(`{"RenderSpecDigest":"`+digest+`"}`), &request); err != nil {
			t.Fatal(err)
		}
		artifact := buildPrintArtifact(request, 100)
		if ids[artifact.ArtifactID] {
			t.Fatalf("different resolved render spec reused artifact %s", artifact.ArtifactID)
		}
		ids[artifact.ArtifactID] = true
		if samePrintArtifact(old, artifact) {
			t.Fatal("legacy and resolved render identities compare equal")
		}
	}
}
