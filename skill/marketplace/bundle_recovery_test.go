package marketplace_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

func bundleFixture(t *testing.T, root, name, version string, api int) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"SKILL.md":             []byte(fmt.Sprintf("---\nname: %s\nversion: \"%s\"\ndescription: Bundle fixture\n---\n\nInstalled %s\n", name, version, version)),
		"assets/profile.json":  []byte(fmt.Sprintf(`{"profile_id":"report","render_profile_api":%d,"input_schema":"report/v1","entrypoint":"templates/report.typ"}`, api)),
		"templates/report.typ": []byte("#let report(document) = [Report]\n"),
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	manifest := marketplace.BundleManifest{ContractVersion: 1, SkillID: name, Version: version}
	for _, path := range paths {
		content := files[path]
		sum := sha256.Sum256(content)
		manifest.Files = append(manifest.Files, marketplace.BundleFile{Path: path, Role: "resource", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files["bundle.json"] = data
	for path, content := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func assertBundleVersion(t *testing.T, mp *marketplace.Marketplace, version string) {
	t.Helper()
	sk, ok := mp.Get("homework-pdf")
	if !ok || sk.Meta.Version != version {
		t.Fatalf("installed bundle after reopening = %#v, found=%v; want version %s", sk, ok, version)
	}
	resource, err := mp.ReadResource("homework-pdf", "templates/report.typ")
	if err != nil || resource.Version != version || len(resource.Content) == 0 {
		t.Fatalf("reopened report resource = %#v, %v; want complete version %s", resource, err, version)
	}
}

func TestSeedBundleUpgradesOnlyIntactFactoryInstall(t *testing.T) {
	const name = "homework-pdf"
	old, next := t.TempDir(), t.TempDir()
	oldFiles := bundleFixture(t, old, name, "1.1.7", 1)
	nextFiles := bundleFixture(t, next, name, "1.1.8", 1)
	factory := func(files map[string][]byte, legacy bool) fstest.MapFS {
		source := fstest.MapFS{}
		for file, body := range files {
			source["bundles/"+name+"/"+file] = &fstest.MapFile{Data: body}
		}
		if legacy {
			data := fmt.Sprintf(`{"homework-pdf":{"1.1.7":"%x"}}`, sha256.Sum256(oldFiles["bundle.json"]))
			source["bundles/seed-predecessors.json"] = &fstest.MapFile{Data: []byte(data)}
		}
		return source
	}
	for _, tc := range []string{"known", "recorded", "unknown", "modified", "extra", "empty-directory", "manifest-edit", "disabled", "uninstalled"} {
		t.Run(tc, func(t *testing.T) {
			dir := t.TempDir()
			mp := marketplace.NewMarketplace(dir)
			if tc == "recorded" {
				if _, err := mp.SeedBundlesFromFS(factory(oldFiles, false), "bundles"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := mp.Install(old); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, name, "templates/report.typ")
			want := []byte("user data")
			var err error
			switch tc {
			case "modified":
				err = os.WriteFile(file, want, 0644)
			case "extra":
				file = filepath.Join(dir, name, "custom.txt")
				err = os.WriteFile(file, want, 0644)
			case "empty-directory":
				err = os.Mkdir(filepath.Join(dir, name, "custom"), 0755)
			case "manifest-edit":
				file = filepath.Join(dir, name, "bundle.json")
				want = append(append([]byte(nil), oldFiles["bundle.json"]...), '\n')
				err = os.WriteFile(file, want, 0644)
			case "disabled":
				err = mp.SetEnabled(name, false)
			case "uninstalled":
				err = mp.Uninstall(name)
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened := marketplace.NewMarketplace(dir)
			source := factory(nextFiles, tc != "unknown" && tc != "recorded")
			n, err := reopened.SeedBundlesFromFS(source, "bundles")
			if err != nil {
				t.Fatal(err)
			}
			if tc == "known" || tc == "recorded" {
				if n != 1 {
					t.Fatalf("upgraded %d packages, want one", n)
				}
				assertBundleVersion(t, reopened, "1.1.8")
				n, err = reopened.SeedBundlesFromFS(factory(oldFiles, true), "bundles")
				if err != nil || n != 0 {
					t.Fatalf("downgrade: %d, %v", n, err)
				}
				assertBundleVersion(t, reopened, "1.1.8")
				return
			}
			if n != 0 {
				t.Fatalf("unexpected package update: %d", n)
			}
			switch tc {
			case "modified", "extra", "manifest-edit":
				if got, err := os.ReadFile(file); err != nil || string(got) != string(want) {
					t.Fatalf("custom content changed: %v", err)
				}
			case "empty-directory":
				if _, err := os.Stat(filepath.Join(dir, name, "custom")); err != nil {
					t.Fatal(err)
				}
			case "uninstalled":
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatal("uninstalled skill reappeared")
				}
			case "disabled":
				if reopened.IsEnabled(name) {
					t.Fatal("skill unexpectedly enabled")
				}
			}
		})
	}
}

// 目录中的真实 rename 状态复现旧安装器被中止后的磁盘边界，不复用进程内注册表。
func TestBundleInstallRecoversInterruptedRename(t *testing.T) {
	for _, seedFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("seed_first_%v", seedFirst), func(t *testing.T) {
			dir := t.TempDir()
			old := t.TempDir()
			bundleFixture(t, old, "homework-pdf", "1.1.0", 1)
			mp := marketplace.NewMarketplace(dir)
			if _, err := mp.Install(old); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(dir, ".hexclaw-bundle-interrupted")
			bundleFixture(t, stage, "homework-pdf", "1.2.0", 2)
			if err := os.Rename(filepath.Join(dir, "homework-pdf"), stage+".previous"); err != nil {
				t.Fatal(err)
			}
			reopened := marketplace.NewMarketplace(dir)
			if seedFirst {
				factory := bundleFixture(t, t.TempDir(), "homework-pdf", "1.0.0", 1)
				seed := fstest.MapFS{"skills/homework-pdf.md": &fstest.MapFile{Data: factory["SKILL.md"]}}
				for path, content := range factory {
					seed["skill-bundles/homework-pdf/"+path] = &fstest.MapFile{Data: content}
				}
				if _, err := reopened.SeedFromFS(seed, "skills"); err != nil {
					t.Fatal(err)
				}
				if _, err := reopened.SeedBundlesFromFS(seed, "skill-bundles"); err != nil {
					t.Fatal(err)
				}
			}
			if err := reopened.Init(); err != nil {
				t.Fatal(err)
			}
			assertBundleVersion(t, reopened, "1.1.0")
		})
	}
}

func TestBundleInstallPublicationRecordSurvivesReopen(t *testing.T) {
	for _, phase := range []string{"prepared", "previous_moved", "published"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			old := t.TempDir()
			bundleFixture(t, old, "homework-pdf", "1.1.0", 1)
			if _, err := marketplace.NewMarketplace(dir).Install(old); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(dir, ".hexclaw-bundle-interrupted")
			bundleFixture(t, stage, "homework-pdf", "1.2.0", 2)
			manifest, err := marketplace.ReadBundleManifest(stage)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := manifest.SemanticDigest()
			if err != nil {
				t.Fatal(err)
			}
			record, err := json.Marshal(map[string]string{"skill_id": "homework-pdf", "digest": digest})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stage+".pending", record, 0644); err != nil {
				t.Fatal(err)
			}
			if phase != "prepared" {
				if err := os.Rename(filepath.Join(dir, "homework-pdf"), stage+".previous"); err != nil {
					t.Fatal(err)
				}
			}
			want := "1.1.0"
			if phase == "published" {
				if err := os.Rename(stage, filepath.Join(dir, "homework-pdf")); err != nil {
					t.Fatal(err)
				}
				// 完成发布后的收尾中断仍须移除旧单文件入口。
				if err := os.WriteFile(filepath.Join(dir, "homework-pdf.md"), []byte("---\nname: homework-pdf\nversion: 1.0.0\n---\nOld seed\n"), 0644); err != nil {
					t.Fatal(err)
				}
				want = "1.2.0"
			}
			reopened := marketplace.NewMarketplace(dir)
			if err := reopened.Init(); err != nil {
				t.Fatal(err)
			}
			assertBundleVersion(t, reopened, want)
			if _, err := os.Stat(stage + ".pending"); !os.IsNotExist(err) {
				t.Fatalf("publication record was not finalized: %v", err)
			}
			if err := reopened.Init(); err != nil {
				t.Fatal(err)
			}
			assertBundleVersion(t, reopened, want)
		})
	}
}

func TestBundleInstallRejectsIncompatibleUpdateBeforeReplacing(t *testing.T) {
	dir := t.TempDir()
	mp := marketplace.NewMarketplace(dir)
	mp.SetBundleValidator(func(root string, manifest marketplace.BundleManifest) error {
		if manifest.SkillID != "homework-pdf" {
			return nil
		}
		digest, err := manifest.SemanticDigest()
		if err != nil {
			return err
		}
		_, err = render.LoadReportBundle(root, digest)
		return err
	})
	old := t.TempDir()
	bundleFixture(t, old, "homework-pdf", "1.1.0", 1)
	if _, err := mp.Install(old); err != nil {
		t.Fatal(err)
	}
	candidate := t.TempDir()
	bundleFixture(t, candidate, "homework-pdf", "1.2.0", 999)
	if _, err := mp.Install(candidate); err == nil {
		t.Fatal("incompatible report profile replaced the usable installed bundle")
	}
	assertBundleVersion(t, mp, "1.1.0")
	reopened := marketplace.NewMarketplace(dir)
	if err := reopened.Init(); err != nil {
		t.Fatal(err)
	}
	assertBundleVersion(t, reopened, "1.1.0")
	manifest, err := marketplace.ReadBundleManifest(filepath.Join(dir, "homework-pdf"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.SemanticDigest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := render.LoadReportBundle(filepath.Join(dir, "homework-pdf"), digest); err != nil {
		t.Fatalf("previous installed report no longer loads: %v", err)
	}
	other := t.TempDir()
	bundleFixture(t, other, "other-skill", "1.0.0", 999)
	if _, err := mp.Install(other); err != nil {
		t.Fatalf("report compatibility check affected another skill: %v", err)
	}
	markdown := filepath.Join(t.TempDir(), "ordinary.md")
	if err := os.WriteFile(markdown, []byte("---\nname: ordinary\nversion: 1.0.0\n---\n\nOrdinary skill\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := mp.Install(markdown); err != nil {
		t.Fatalf("report compatibility check affected Markdown installation: %v", err)
	}
}
