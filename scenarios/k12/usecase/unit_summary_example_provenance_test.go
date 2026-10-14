package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestUnitSummaryExampleProvenanceUsesOriginalPrompt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prompt     string
		source     string
		origin     string
		wantPrompt string
		wantOrigin string
	}{
		{name: "confirmed source", prompt: "【教材题】计算 $3/4 \\div 2$。", source: "原题：【教材题】计算 $3/4 \\div 2$。", origin: "source", wantPrompt: "计算 $3/4 \\div 2$。", wantOrigin: "source"},
		{name: "label removal cannot prove source", prompt: "【教材例题】计算 $3/4 \\div 2$。", source: "计算 $3/4 \\div 2$。", origin: "source", wantPrompt: "计算 $3/4 \\div 2$。", wantOrigin: "ai_created"},
		{name: "source exercise claim", prompt: "【教材练习】解方程 $0.75x=18$。", source: "解方程 $0.75x=18$。", origin: "source", wantPrompt: "解方程 $0.75x=18$。", wantOrigin: "ai_created"},
		{name: "AI claim", prompt: "【AI创作例题】解方程 $0.75x=18$。", source: "【AI创作例题】解方程 $0.75x=18$。", origin: "ai_created", wantPrompt: "解方程 $0.75x=18$。", wantOrigin: "ai_created"},
		{name: "leading spacing", prompt: "\n 【教材题】 \t计算 $3/4$。\n", source: "其他原题。", origin: "source", wantPrompt: "计算 $3/4$。\n", wantOrigin: "ai_created"},
		{name: "plain source", prompt: "计算 $3/4$。", source: "计算 $3/4$。", origin: "source", wantPrompt: "计算 $3/4$。", wantOrigin: "source"},
		{name: "internal source mention", prompt: "比较【教材题】和另一道题的条件。", source: "其他原题。", origin: "ai_created", wantPrompt: "比较【教材题】和另一道题的条件。", wantOrigin: "ai_created"},
		{name: "mathematical brackets", prompt: "【3/4+1/4】÷2。", source: "其他原题。", origin: "ai_created", wantPrompt: "【3/4+1/4】÷2。", wantOrigin: "ai_created"},
		{name: "unapproved claim spelling", prompt: "【AI 创作例题】计算 $3/4$。", source: "其他原题。", origin: "ai_created", wantPrompt: "【AI 创作例题】计算 $3/4$。", wantOrigin: "ai_created"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := k12.UnitSummaryContentV1{Examples: []k12.UnitSummaryExample{{ID: "example-1", Origin: tc.origin, PromptMD: tc.prompt, DemonstrationMD: "保持原讲解 $x=24$。", SourceRefs: []string{"original"}}}}
			before, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			coordinator := &UnitSummaryCoordinator{}
			frozen := k12.UnitSummaryContext{Sources: []k12.UnitSummaryFrozenSource{{UnitSummarySource: k12.UnitSummarySource{Ref: "original"}, Text: tc.source}}}
			got, err := coordinator.normalizeContentReferences(context.Background(), "owner", input, frozen)
			if err != nil {
				t.Fatal(err)
			}
			if got.Examples[0].PromptMD != tc.wantPrompt || got.Examples[0].Origin != tc.wantOrigin {
				t.Fatalf("unexpected provenance: prompt=%q, origin=%q", got.Examples[0].PromptMD, got.Examples[0].Origin)
			}
			if got.Examples[0].DemonstrationMD != input.Examples[0].DemonstrationMD || !reflect.DeepEqual(got.Examples[0].SourceRefs, input.Examples[0].SourceRefs) {
				t.Fatal("mathematical explanation or source references changed")
			}
			after, err := json.Marshal(input)
			if err != nil || string(after) != string(before) {
				t.Fatal("normalization modified the caller's content")
			}
		})
	}
}

func TestUnitSummaryExampleProvenanceRejectsUnknownReference(t *testing.T) {
	input := k12.UnitSummaryContentV1{Examples: []k12.UnitSummaryExample{{Origin: "source", PromptMD: "【教材题】计算 $3/4$。", SourceRefs: []string{"unknown"}}}}
	coordinator := &UnitSummaryCoordinator{}
	if _, err := coordinator.normalizeContentReferences(context.Background(), "owner", input, k12.UnitSummaryContext{}); err == nil {
		t.Fatal("unknown source reference was accepted")
	}
	if input.Examples[0].PromptMD != "【教材题】计算 $3/4$。" || input.Examples[0].Origin != "source" {
		t.Fatal("rejected input changed")
	}
}

// 真实冻结资料通过显式路径注入；仅写派生规范投影，不修改原文件或旧产物。
func TestUnitSummaryExampleProvenanceObservedMaterial(t *testing.T) {
	fixture := os.Getenv("HEXCLAW_UNIT_PROVENANCE_FIXTURE")
	output := os.Getenv("HEXCLAW_UNIT_PROVENANCE_OUTPUT")
	if fixture == "" || output == "" {
		t.Skip("observed material and derived output paths are not configured")
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(raw)
	var input k12.UnitSummaryContentV1
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Examples) != 7 {
		t.Fatalf("expected seven observed examples, got %d", len(input.Examples))
	}
	original := append([]k12.UnitSummaryExample(nil), input.Examples...)
	frozen := k12.UnitSummaryContext{}
	for _, source := range input.Sources {
		// 按真实ref建立别名；空正文不能构造新的教材逐字证据。
		entry := k12.UnitSummaryFrozenSource{UnitSummarySource: k12.UnitSummarySource{Ref: source.Ref, Label: source.Label}}
		if source.Origin == "student_work" {
			frozen.Evidence = append(frozen.Evidence, entry)
		} else {
			frozen.Sources = append(frozen.Sources, entry)
		}
	}
	coordinator := &UnitSummaryCoordinator{}
	got, err := coordinator.normalizeContentReferences(context.Background(), "owner", input, frozen)
	if err != nil {
		t.Fatal(err)
	}
	wantContent := input
	wantContent.Examples = append([]k12.UnitSummaryExample(nil), original...)
	for i, example := range got.Examples {
		want := original[i]
		switch i {
		case 0:
			want.PromptMD = strings.TrimPrefix(want.PromptMD, "【教材题】")
		case 2:
			want.PromptMD = strings.TrimPrefix(want.PromptMD, "【教材练习】")
		default:
			want.PromptMD = strings.TrimPrefix(want.PromptMD, "【教材例题】")
		}
		if !reflect.DeepEqual(example, want) || example.Origin != "ai_created" {
			t.Fatalf("observed example %d changed beyond its redundant leading claim", i+1)
		}
		wantContent.Examples[i] = want
	}
	if !reflect.DeepEqual(got, wantContent) {
		t.Fatal("observed material changed beyond its seven leading example claims")
	}
	if !reflect.DeepEqual(input.Examples, original) {
		t.Fatal("observed input examples changed")
	}
	markdown := UnitSummaryMarkdown(got, "2026-10-14", 1)
	examplesMarkdown := strings.SplitN(markdown, "## 迁移检查", 2)[0]
	if strings.Count(examplesMarkdown, " · AI 创作例题\n") != 7 || strings.Contains(examplesMarkdown, "【教材题】") || strings.Contains(examplesMarkdown, "【教材例题】") || strings.Contains(examplesMarkdown, "【教材练习】") {
		t.Fatal("derived canonical Markdown has contradictory example provenance")
	}
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "derived-canonical.md"), []byte(markdown), 0600); err != nil {
		t.Fatal(err)
	}
	derived, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "derived-content.json"), derived, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(fixture)
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("observed frozen source file changed")
	}
	t.Logf("observed examples=7, original_sha256=%s, canonical_sha256=%s", hex.EncodeToString(before[:]), k12.UnitSummaryDigest(markdown))
}
