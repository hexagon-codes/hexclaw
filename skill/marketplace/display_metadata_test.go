package marketplace

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/hexagon-codes/hexclaw/skill"
)

const displayMetadataFixture = `---
name: math-method-fixture
display_name: Author math name
display_names: {"zh-CN":"数学辅导","en":"Math tutoring","ug-CN":"ماتېماتىكا"}
teaching_methods: [{"name":"波利亚四步","display_names":{"en":"Polya four steps"},"structure":{"zh-CN":"理解 → 计划 → 执行 → 回顾"}}]
description: exact source fixture
version: 1.0.0
---
Understand the problem, plan, carry out and review.
`

func TestSkillDisplayMetadataPreservesIdentityAndLoadedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte(displayMetadataFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSkillFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Meta.Name != "math-method-fixture" || s.Meta.DisplayName != "Author math name" || s.Meta.DisplayNames["en"] != "Math tutoring" || len(s.Meta.TeachingMethods) != 1 {
		t.Fatalf("metadata lost: %+v", s.Meta)
	}
	if _, ok := s.LoadedContextReceipt("not loaded"); ok {
		t.Fatal("unloaded skill invented a context receipt")
	}
	wrapped := WrapAsSkill(s)
	loader := wrapped.(skill.ContentLoader)
	body, err := loader.LoadContent()
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := wrapped.(skill.ContextReceiptProvider).LoadedContextReceipt(body)
	if !ok || receipt.Name != s.Name() || receipt.Version != "1.0.0" || receipt.Status != "loaded" || receipt.ContentDigest != "sha256:f6caef336258acf16fe84b114a4361ee26269c9402f2a5d1e4391f697ee4878d" || receipt.Methods[0].Name != "波利亚四步" {
		t.Fatalf("invalid loaded snapshot: %+v, ok=%v", receipt, ok)
	}
	if _, ok := wrapped.(skill.MetaProvider); ok {
		t.Fatal("display-only adapter changed activation gates")
	}
}

func TestSkillDisplayMetadataSeedEnrichmentPreservesCustomContent(t *testing.T) {
	old := []byte("---\nname: same-skill\nversion: 1.0.0\ndescription: original\n---\nexact original body\n")
	new := []byte("---\nname: same-skill\ndisplay_names: {\"zh-CN\":\"原有技能\",\"en\":\"Original skill\"}\nversion: 1.0.0\ndescription: original\n---\nexact original body\n")
	for _, tracked := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "tracked"}[tracked], func(t *testing.T) {
			mp := NewMarketplace(t.TempDir())
			if tracked {
				if _, err := mp.SeedFromFS(fstest.MapFS{"skills/same.md": {Data: old}}, "skills"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(mp.Dir(), "same.md"), old, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if n, err := mp.SeedFromFS(fstest.MapFS{"skills/same.md": {Data: new}}, "skills"); err != nil || n != 1 {
				t.Fatalf("metadata upgrade n=%d err=%v", n, err)
			}
			if n, err := mp.SeedFromFS(fstest.MapFS{"skills/same.md": {Data: new}}, "skills"); err != nil || n != 0 {
				t.Fatalf("duplicate enrichment n=%d err=%v", n, err)
			}
			custom := []byte("---\nname: same-skill\nversion: 1.0.0\ndescription: original\n---\nuser changed body\n")
			if err := os.WriteFile(filepath.Join(mp.Dir(), "same.md"), custom, 0o600); err != nil {
				t.Fatal(err)
			}
			if n, err := mp.SeedFromFS(fstest.MapFS{"skills/same.md": {Data: new}}, "skills"); err != nil || n != 0 {
				t.Fatalf("custom skill overwritten n=%d err=%v", n, err)
			}
			got, _ := os.ReadFile(filepath.Join(mp.Dir(), "same.md"))
			if string(got) != string(custom) {
				t.Fatal("custom body changed")
			}
		})
	}
	t.Run("legacy-author-name", func(t *testing.T) {
		mp := NewMarketplace(t.TempDir())
		custom := []byte("---\nname: same-skill\ndisplay_name: My chosen tutor\nversion: 1.0.0\ndescription: original\n---\nexact original body\n")
		if err := os.WriteFile(filepath.Join(mp.Dir(), "same.md"), custom, 0o600); err != nil {
			t.Fatal(err)
		}
		if n, err := mp.SeedFromFS(fstest.MapFS{"skills/same.md": {Data: new}}, "skills"); err != nil || n != 0 {
			t.Fatalf("author name was overwritten n=%d err=%v", n, err)
		}
		got, _ := os.ReadFile(filepath.Join(mp.Dir(), "same.md"))
		if string(got) != string(custom) {
			t.Fatal("custom visible name changed")
		}
	})
}
