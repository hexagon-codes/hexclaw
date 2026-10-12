package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHubAuthorDisplayNamesSurviveRefreshCacheAndSearch(t *testing.T) {
	updated := time.Now().UTC().Format(time.RFC3339Nano)
	index := fmt.Sprintf(`{"version":"2.1.0","updated_at":%q,"skills":[
		{"name":"publisher-method","display_name":"作者原名","display_names":{"zh-CN":"作者数学方法","en":"Published math method","ug-CN":"نەشر قىلىنغان ماتېماتىكا ئۇسۇلى"},"version":"1.2.3","author":"publisher","description":"author supplied metadata","category":"education","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":321},
		{"name":"math-tutor","display_name":"我的讲题法","version":"1.0.0","author":"custom-author","category":"education"}
	]}`, updated)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "index.json":
			_, _ = w.Write([]byte(index))
		case "mcp-registry.json":
			_, _ = fmt.Fprintf(w, `{"version":"2.1.0","updated_at":%q,"servers":[]}`, updated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	cache := t.TempDir()
	config := HubConfig{RepoURL: server.URL, Branch: "main"}
	h := New(config, dir)
	h.client = server.Client()
	h.SetCacheDir(cache)
	if err := h.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertMetadata := func(t *testing.T, snapshot *Catalog) {
		t.Helper()
		if snapshot == nil || len(snapshot.Skills) != 2 {
			t.Fatalf("expected the complete author catalog, got %#v", snapshot)
		}
		body, err := json.Marshal(snapshot.Skills)
		if err != nil {
			t.Fatal(err)
		}
		var entries []struct {
			Name         string            `json:"name"`
			DisplayName  string            `json:"display_name"`
			DisplayNames map[string]string `json:"display_names"`
			Version      string            `json:"version"`
			SHA256       string            `json:"sha256"`
			Size         int64             `json:"size"`
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			t.Fatal(err)
		}
		got := entries[0]
		if got.Name != "publisher-method" || got.DisplayName != "作者原名" || got.Version != "1.2.3" || got.Size != 321 || got.SHA256 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatalf("author identity or artifact changed: %#v", got)
		}
		for locale, expected := range map[string]string{"zh-CN": "作者数学方法", "en": "Published math method", "ug-CN": "نەشر قىلىنغان ماتېماتىكا ئۇسۇلى"} {
			if got.DisplayNames[locale] != expected {
				t.Errorf("author name lost for %s: got %q, want %q", locale, got.DisplayNames[locale], expected)
			}
		}
		custom := entries[1]
		if custom.Name != "math-tutor" || custom.DisplayName != "我的讲题法" || len(custom.DisplayNames) != 0 {
			t.Fatalf("custom author name must remain independent of a matching builtin identifier: %#v", custom)
		}
	}
	t.Run("refreshed_list_output", func(t *testing.T) { assertMetadata(t, h.GetCatalog()) })
	t.Run("declared_name_search", func(t *testing.T) {
		for _, query := range []string{"作者数学方法", "Published math method", "نەشر قىلىنغان ماتېماتىكا ئۇسۇلى", "publisher-method"} {
			got := h.Search(query)
			if len(got) != 1 || got[0].Name != "publisher-method" {
				t.Errorf("query %q must find the original identifier, got %#v", query, got)
			}
		}
	})
	t.Run("snapshot_isolation", func(t *testing.T) {
		snapshot := h.GetCatalog()
		snapshot.Skills[0].DisplayNames["en"] = "Caller edit"
		assertMetadata(t, h.GetCatalog())
	})
	t.Run("cold_cache_output", func(t *testing.T) {
		cold := New(config, dir)
		cold.client = server.Client()
		cold.SetCacheDir(cache)
		// 保持现有退避窗口，只验证冷读，避免无关的后台刷新。
		cold.lastAttempt = time.Now()
		cold.EnsureCatalog()
		assertMetadata(t, cold.GetCatalog())
	})
}

func TestHubOptionalDisplayNamesDoNotInvalidateLegacyMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		count int
	}{
		{name: "mixed_values", raw: `{"en":"Author name","zh-CN":42,"ug-CN":null}`, count: 1},
		{name: "non_object", raw: `"legacy optional value"`},
		{name: "array", raw: `["legacy optional value"]`},
		{name: "null", raw: `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"name":"author-method","display_name":"作者原名","display_names":%s,"version":"1.0.0","author":"publisher"}`, tc.raw)
			var meta SkillMeta
			if err := json.Unmarshal([]byte(body), &meta); err != nil {
				t.Fatalf("optional display values must not invalidate legacy metadata: %v", err)
			}
			encoded, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Name         string            `json:"name"`
				DisplayName  string            `json:"display_name"`
				DisplayNames map[string]string `json:"display_names"`
				Version      string            `json:"version"`
				Author       string            `json:"author"`
			}
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if got.Name != "author-method" || got.DisplayName != "作者原名" || got.Version != "1.0.0" || got.Author != "publisher" || len(got.DisplayNames) != tc.count {
				t.Fatalf("author identity or valid display values changed: %#v", got)
			}
			if tc.count != 0 && got.DisplayNames["en"] != "Author name" {
				t.Fatalf("valid author name was discarded: %#v", got)
			}
		})
	}
}
