package usecase

import (
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

func TestUnitSummaryBriefProjectionSources(t *testing.T) {
	book := func(ref, page string) k12.UnitSummarySource {
		return k12.UnitSummarySource{Ref: ref, Origin: "textbook", Label: "数学教材", DocumentID: "book-a", ManifestID: "manifest-a", Generation: 2, SourceDigest: "book-digest", Page: page}
	}
	otherDocument := book("other-document", "29")
	otherDocument.DocumentID = "book-b"
	otherManifest := book("other-manifest", "30")
	otherManifest.ManifestID = "manifest-b"
	otherGeneration := book("other-generation", "31")
	otherGeneration.Generation = 3
	otherDigest := book("other-digest", "32")
	otherDigest.SourceDigest = "other-digest"
	for _, tc := range []struct {
		name    string
		sources []k12.UnitSummarySource
		want    string
	}{
		{
			name: "continuous book pages and distinct current work",
			sources: []k12.UnitSummarySource{
				book("page-27", "27"), book("page-28", "28"),
				{Ref: "work-1", Origin: "student_work", Label: "当前孩子的实际作答 · 数学", SourceDigest: "work-digest-1"},
				{Ref: "work-2", Origin: "student_work", Label: "当前孩子的实际作答 · 数学", SourceDigest: "work-digest-2"},
				{Ref: "work-1", Origin: "student_work", Label: "当前孩子的实际作答 · 数学", SourceDigest: "work-digest-1"},
			},
			want: "来源：数学教材 · 第27–28页、当前孩子的实际作答 · 数学（2条）。",
		},
		{
			name:    "equal labels do not join different source identities",
			sources: []k12.UnitSummarySource{book("page-28", "28"), otherDocument, otherManifest, otherGeneration, otherDigest},
			want:    "来源：数学教材 · 第28页、数学教材 · 第29页、数学教材 · 第30页、数学教材 · 第31页、数学教材 · 第32页。",
		},
		{
			name: "missing pages and unproven identity remain explicit",
			sources: []k12.UnitSummarySource{
				book("page-27", "27"), book("page-29", "29"),
				{Ref: "unknown-30", Origin: "textbook", Label: "数学教材", Page: "30"},
				{Ref: "unknown-31", Origin: "textbook", Label: "数学教材", Page: "31"},
			},
			want: "来源：数学教材 · 第27页、数学教材 · 第29页、数学教材 · 第30页、数学教材 · 第31页。",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := k12.UnitSummaryContentV1{Sources: tc.sources}
			before, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			markdown := unitSummaryMarkdown(content, "2026-10-15", 3, true)
			if !strings.Contains(markdown, tc.want) {
				t.Errorf("source projection did not preserve the approved grouping: want %q", tc.want)
			}
			after, err := json.Marshal(content)
			if err != nil || string(after) != string(before) {
				t.Fatal("source presentation changed canonical source identities")
			}
		})
	}
}

func TestUnitSummaryBriefProjectionReferences(t *testing.T) {
	t.Run("current IDs map exactly and unknown IDs remain unchanged", func(t *testing.T) {
		content := k12.UnitSummaryContentV1{
			Examples:       []k12.UnitSummaryExample{{ID: "e3"}, {ID: "e6"}},
			TransferChecks: []k12.UnitSummaryTransferCheck{{ID: "c3", ExampleID: "e3"}, {ID: "c6", ExampleID: "e6"}},
			PersonalGuidance: []k12.UnitSummaryPersonalGuidance{{
				Text: "先看例e6，再联系检查c3；保留例e30、检查c30和普通e3。", EvidenceRefs: []string{"actual-work"},
			}},
		}
		before := append([]k12.UnitSummaryPersonalGuidance(nil), content.PersonalGuidance...)
		markdown := unitSummaryMarkdown(content, "2026-10-15", 3, true)
		if !strings.Contains(markdown, "先看例 2，再联系检查 1；保留例e30、检查c30和普通e3。") {
			t.Error("personal reference projection did not match this revision's visible order")
		}
		if !reflect.DeepEqual(content.PersonalGuidance, before) {
			t.Fatal("reference presentation rewrote frozen personal evidence")
		}
	})
	t.Run("ambiguous IDs are not guessed", func(t *testing.T) {
		content := k12.UnitSummaryContentV1{
			Examples:         []k12.UnitSummaryExample{{ID: "e3"}, {ID: "e3"}},
			TransferChecks:   []k12.UnitSummaryTransferCheck{{ID: "c3"}},
			PersonalGuidance: []k12.UnitSummaryPersonalGuidance{{Text: "例e3与检查c3", EvidenceRefs: []string{"actual-work"}}},
		}
		if markdown := unitSummaryMarkdown(content, "2026-10-15", 3, true); !strings.Contains(markdown, "例e3与检查 1") {
			t.Error("ambiguous example reference was guessed or valid check reference was lost")
		}
	})
	t.Run("render only removes exact redundant leading claims", func(t *testing.T) {
		content := k12.UnitSummaryContentV1{Examples: []k12.UnitSummaryExample{
			{ID: "e3", Origin: "ai_created", PromptMD: "【原资料AI诊断例】计算 $3/4$。"},
			{ID: "e6", Origin: "ai_created", PromptMD: "【原资料教材题整理】比较 $8/15$。"},
			{ID: "e9", Origin: "ai_created", PromptMD: "比较【原资料教材题整理】与【3/4+1/4】。"},
		}}
		original := append([]k12.UnitSummaryExample(nil), content.Examples...)
		markdown := unitSummaryMarkdown(content, "2026-10-15", 3, true)
		for _, required := range []string{"计算 $3/4$。", "比较 $8/15$。", "比较【原资料教材题整理】与【3/4+1/4】。"} {
			if !strings.Contains(markdown, required) {
				t.Errorf("mathematical content or internal mention was lost: %q", required)
			}
		}
		if strings.Contains(markdown, "【原资料AI诊断例】计算") || strings.Contains(markdown, "【原资料教材题整理】比较") {
			t.Error("render-only content still has contradictory leading source claims")
		}
		if !reflect.DeepEqual(content.Examples, original) {
			t.Fatal("render-only projection changed frozen example content")
		}
	})
	t.Run("observed frozen v02 render only", func(t *testing.T) {
		fixture := os.Getenv("HEXCLAW_UNIT_BRIEF_PROJECTION_FIXTURE")
		output := os.Getenv("HEXCLAW_UNIT_BRIEF_PROJECTION_OUTPUT")
		if fixture == "" || output == "" {
			t.Skip("observed material and derived output paths are not configured")
		}
		raw, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		originalDigest := sha256.Sum256(raw)
		var response struct {
			Body struct {
				Material struct {
					Content k12.UnitSummaryContentV1 `json:"content"`
				} `json:"material"`
			} `json:"body"`
		}
		if err = json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		content := response.Body.Material.Content
		if len(content.Sources) != 26 || len(content.Examples) != 2 || len(content.TransferChecks) != 2 || len(content.PersonalGuidance) != 1 {
			t.Fatal("observed v02 does not match the retained incident material")
		}
		before, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		markdown := unitSummaryMarkdown(content, "2026-10-15", 3, true)
		if err = os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string][]byte{
			"frozen-content.json":  before,
			"derived-canonical.md": []byte(markdown),
			"derived-render.md":    []byte(unitSummaryRenderMarkdown(markdown, unitSummaryRenderContract(k12.UnitSummaryRequirements{Format: "brief"}))),
		} {
			if err = os.WriteFile(filepath.Join(output, name), value, 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, required := range []string{
			"来源：义务教育教科书 · 数学六年级上册 · 第27–45页、当前孩子的实际作答 · 数学（7条）。",
			"优先请她口述例 1和检查 1的关系",
		} {
			if !strings.Contains(markdown, required) {
				t.Errorf("observed projection is missing %q", required)
			}
		}
		if strings.Contains(markdown, "【原资料AI诊断例】") || strings.Contains(markdown, "【原资料教材题整理】") {
			t.Error("observed render-only material retained contradictory source prefixes")
		}
		after, err := json.Marshal(content)
		if err != nil || string(after) != string(before) {
			t.Fatal("observed render-only projection mutated frozen canonical content")
		}
		sourceAfter, err := os.ReadFile(fixture)
		if err != nil || sha256.Sum256(sourceAfter) != originalDigest {
			t.Fatal("observed frozen source bytes changed")
		}
		t.Logf("sources=26, source_sha256=%s, canonical_sha256=%s", hex.EncodeToString(originalDigest[:]), k12.UnitSummaryDigest(markdown))
	})
}
