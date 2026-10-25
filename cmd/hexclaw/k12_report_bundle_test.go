package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

func reportSkillFixture(t *testing.T, name, version, profileID string, api int, schema string, features []string) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	profile, err := json.Marshal(map[string]any{
		"profile_id": profileID, "render_profile_api": api, "input_schema": schema,
		"entrypoint": "assets/template.typ", "required_typst_features": features,
	})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"SKILL.md":            []byte(fmt.Sprintf("---\nname: %s\nversion: \"%s\"\ndescription: Report fixture\n---\n\nReport %s\n", name, version, version)),
		"assets/profile.json": profile,
		"assets/template.typ": []byte("#let report(document) = [Report]\n"),
	}
	manifest := marketplace.BundleManifest{ContractVersion: 1, SkillID: name, Version: version}
	for i, path := range []string{"SKILL.md", "assets/profile.json", "assets/template.typ"} {
		content := files[path]
		sum := sha256.Sum256(content)
		manifest.Files = append(manifest.Files, marketplace.BundleFile{
			Path: path, Role: []string{"skill", "profile", "template"}[i],
			Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	files["bundle.json"], err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, files
}

func reportMarketplace(t *testing.T) *marketplace.Marketplace {
	t.Helper()
	mp := marketplace.NewMarketplace(t.TempDir())
	mp.SetBundleValidator(validateReportSkillBundle)
	if err := mp.Init(); err != nil {
		t.Fatal(err)
	}
	return mp
}

func assertInstalledReportSkill(t *testing.T, mp *marketplace.Marketplace, version string, profile []byte) {
	t.Helper()
	sk, ok := mp.Get("homework-pdf")
	if !ok || sk.Meta.Version != version {
		t.Fatalf("installed report skill = %#v, found=%v; want version %s", sk, ok, version)
	}
	resource, err := mp.ReadResource("homework-pdf", "assets/profile.json")
	if err != nil || !bytes.Equal(resource.Content, profile) || resource.Version != version {
		t.Fatalf("installed report profile changed: version=%s, error=%v", resource.Version, err)
	}
	manifest, err := marketplace.ReadBundleManifest(filepath.Dir(sk.FilePath))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.SemanticDigest()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := render.LoadReportBundle(filepath.Dir(sk.FilePath), digest)
	if err != nil {
		t.Fatalf("installed report no longer loads: %v", err)
	}
	if err := bundle.ValidateHostCompatibility(); err != nil {
		t.Fatalf("installed report is incompatible with its host: %v", err)
	}
}

func TestReportSkillBundleAcceptsSupportedContracts(t *testing.T) {
	mp := reportMarketplace(t)
	for _, contract := range []struct {
		version string
		api     int
		schema  string
	}{
		{version: "1.0.0", api: 1, schema: "homework-report-render-v1"},
		{version: "2.0.0", api: 2, schema: "homework-report-render-v2"},
	} {
		source, files := reportSkillFixture(t, "homework-pdf", contract.version, "fixture-report", contract.api, contract.schema,
			[]string{"json", "grid", "table.header", "math.display", "image.fit-contain"})
		if _, err := mp.Install(source); err != nil {
			t.Fatalf("supported report contract %s rejected: %v", contract.schema, err)
		}
		assertInstalledReportSkill(t, mp, contract.version, files["assets/profile.json"])
	}
}

func TestReportSkillBundleRejectsUnsupportedUpdateBeforeReplace(t *testing.T) {
	for _, candidate := range []struct {
		name      string
		profileID string
		api       int
		schema    string
		features  []string
		wantErr   string
	}{
		{name: "profile_api", profileID: "fixture-report", api: 999, schema: "homework-report-render-v2", wantErr: "unsupported report profile"},
		{name: "profile_id", profileID: "", api: 2, schema: "homework-report-render-v2", wantErr: "report profile identity is unavailable"},
		{name: "input_schema", profileID: "fixture-report", api: 2, schema: "homework-report-render-v999", wantErr: "unsupported report input schema"},
		{name: "required_typst_feature", profileID: "fixture-report", api: 2, schema: "homework-report-render-v2", features: []string{"json", "fixture.future-feature"}, wantErr: "unsupported report compiler feature"},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			mp := reportMarketplace(t)
			oldSource, oldFiles := reportSkillFixture(t, "homework-pdf", "1.0.0", "fixture-report", 1, "homework-report-render-v1", []string{"json"})
			if _, err := mp.Install(oldSource); err != nil {
				t.Fatal(err)
			}
			source, _ := reportSkillFixture(t, "homework-pdf", "2.0.0", candidate.profileID, candidate.api, candidate.schema, candidate.features)
			if _, err := mp.Install(source); err == nil || !strings.Contains(err.Error(), candidate.wantErr) {
				t.Fatalf("unsupported update error = %v; want %q before replacing the old report", err, candidate.wantErr)
			}
			assertInstalledReportSkill(t, mp, "1.0.0", oldFiles["assets/profile.json"])
			reopened := marketplace.NewMarketplace(mp.Dir())
			if err := reopened.Init(); err != nil {
				t.Fatal(err)
			}
			assertInstalledReportSkill(t, reopened, "1.0.0", oldFiles["assets/profile.json"])
		})
	}
}

func TestReportSkillBundleKeepsLegacyMarkdownWhenUpdateRejected(t *testing.T) {
	mp := reportMarketplace(t)
	legacy := []byte("---\nname: homework-pdf\nversion: 0.9.0\n---\n\nLegacy Markdown report instructions\n")
	file := filepath.Join(t.TempDir(), "homework-pdf.md")
	if err := os.WriteFile(file, legacy, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := mp.Install(file); err != nil {
		t.Fatalf("legacy Markdown was subjected to report bundle requirements: %v", err)
	}
	source, _ := reportSkillFixture(t, "homework-pdf", "2.0.0", "fixture-report", 2, "homework-report-render-v2", []string{"fixture.future-feature"})
	if _, err := mp.Install(source); err == nil {
		t.Fatal("unsupported report bundle replaced the legacy Markdown skill")
	}
	reopened := marketplace.NewMarketplace(mp.Dir())
	if err := reopened.Init(); err != nil {
		t.Fatal(err)
	}
	sk, ok := reopened.Get("homework-pdf")
	if !ok || sk.Meta.Version != "0.9.0" || filepath.Base(sk.FilePath) != "homework-pdf.md" {
		t.Fatalf("legacy Markdown changed after rejecting update: %#v, found=%v", sk, ok)
	}
	data, err := os.ReadFile(sk.FilePath)
	if err != nil || !bytes.Equal(data, legacy) {
		t.Fatalf("legacy Markdown bytes changed after rejecting update: %v", err)
	}
}

func TestReportSkillBundleDoesNotConstrainOtherSkills(t *testing.T) {
	mp := reportMarketplace(t)
	source, files := reportSkillFixture(t, "other-skill", "1.0.0", "fixture-report", 999, "other-report-schema", []string{"other.compiler-feature"})
	if _, err := mp.Install(source); err != nil {
		t.Fatalf("report host requirements affected another bundle: %v", err)
	}
	resource, err := mp.ReadResource("other-skill", "assets/profile.json")
	if err != nil || !bytes.Equal(resource.Content, files["assets/profile.json"]) {
		t.Fatalf("other skill resource was not installed completely: %v", err)
	}
}
