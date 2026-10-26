package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestSessionPersonaAPIUpdatesOwnedBinding(t *testing.T) {
	store := newTestStoreForAPI(t)
	for _, id := range []string{"one", "two"} {
		if err := store.CreateSession(t.Context(), &storage.Session{ID: id, UserID: "owner", Title: id, Meta: `{"project":"keep"}`}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	for _, name := range []string{"role-one", "role-two", "task"} {
		tags := "[persona]"
		if name == "task" {
			tags = "[writing]"
		}
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\nname: "+name+"\ntags: "+tags+"\n---\nbody"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mp := marketplace.NewMarketplace(dir)
	if err := mp.Init(); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(config.DefaultConfig(), &mockEngine{}, nil, store)
	srv.SetMarketplace(mp)
	update := func(owner, name string, code int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/one/persona?user_id="+owner, strings.NewReader(`{"skill_name":"`+name+`"}`))
		req.SetPathValue("id", "one")
		w := httptest.NewRecorder()
		srv.handleSetSessionPersona(w, req)
		if w.Code != code {
			t.Fatalf("owner=%s name=%s status=%d body=%s", owner, name, w.Code, w.Body.String())
		}
		if code == 200 {
			var result storage.Session
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var meta map[string]string
			if err := json.Unmarshal([]byte(result.Meta), &meta); err != nil {
				t.Fatal(err)
			}
			if result.ID != "one" || meta["persona_skill"] != name || meta["project"] != "keep" {
				t.Fatalf("response=%+v", result)
			}
		}
	}
	update("owner", "role-one", 200)
	update("other", "role-two", 404)
	update("owner", "task", 400)
	update("owner", "role-two", 200)
	if err := mp.SetEnabled("role-one", false); err != nil {
		t.Fatal(err)
	}
	update("owner", "role-one", 400)
	other, err := store.GetSession(t.Context(), "two")
	if err != nil || other.Meta != `{"project":"keep"}` {
		t.Fatalf("other session=%+v err=%v", other, err)
	}
	update("owner", "", 200)
	w := httptest.NewRecorder()
	srv.handleListSkills(w, httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil))
	var listed struct {
		Skills []skillStatusResponse `json:"skills"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Skills) != 3 {
		t.Fatalf("skills=%+v", listed.Skills)
	}
	for _, sk := range listed.Skills {
		if sk.IsPersona != (sk.Name != "task") {
			t.Fatalf("classification=%+v", sk)
		}
	}
}
