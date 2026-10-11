package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

func TestSkillDisplayMetadataListAndContentHTTP(t *testing.T) {
	dir := t.TempDir()
	raw := "---\nname: author-fixture\ndisplay_name: Author chosen name\ndisplay_names: {\"zh-CN\":\"作者技能\",\"en\":\"Author skill\"}\nteaching_methods: [{\"name\":\"波利亚四步\",\"structure\":{\"zh-CN\":\"理解 → 计划 → 执行 → 回顾\"}}]\nversion: 1.0.0\n---\nOriginal instruction body\n"
	if err := os.WriteFile(filepath.Join(dir, "author-fixture.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	mp := marketplace.NewMarketplace(dir)
	if err := mp.Init(); err != nil {
		t.Fatal(err)
	}
	srv := &Server{mp: mp}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/skills", srv.handleListSkills)
	mux.HandleFunc("GET /api/v1/skills/{name}/content", srv.handleSkillContent)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	for _, path := range []string{"/api/v1/skills", "/api/v1/skills/author-fixture/content"} {
		resp, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("path=%s status=%d", path, resp.StatusCode)
		}
		item := body
		if path == "/api/v1/skills" {
			items := body["skills"].([]any)
			if len(items) != 1 {
				t.Fatal(items)
			}
			item = items[0].(map[string]any)
		} else if body["content"] != raw {
			t.Fatal("original instruction changed")
		}
		if item["name"] != "author-fixture" || item["display_name"] != "Author chosen name" || item["display_names"].(map[string]any)["en"] != "Author skill" || item["teaching_methods"].([]any)[0].(map[string]any)["name"] != "波利亚四步" {
			t.Fatalf("metadata not preserved over %s: %+v", path, item)
		}
	}
}
