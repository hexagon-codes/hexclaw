package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/memory"
)

func TestMemoryAPIPreservesNaturalLanguageAndScope(t *testing.T) {
	for _, content := range []string{
		"用户问：知道现在几几年吗？\n回复：几几黏你去洗啊，麻利滴儿",
		"在girlfriend闲聊中，当我问知道现在几几年吗时，回复几几黏你去洗啊，麻利滴儿。",
		"知道现在几几年吗？几几黏你去洗啊，麻利滴儿",
	} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			fm, err := memory.New(memory.Options{Enabled: true, Dir: dir, MaxMemory: 200})
			if err != nil {
				t.Fatal(err)
			}
			srv := NewServer(config.DefaultConfig(), &mockEngine{}, nil, nil)
			srv.SetFileMemory(fm)
			want := memory.Scope{Skills: []string{"girlfriend"}}
			payload, err := json.Marshal(SaveMemoryRequest{Content: content, Type: "fact", Source: "manual", Scope: want})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			srv.handleSaveMemory(response, httptest.NewRequest(http.MethodPost, "/api/v1/memory", bytes.NewReader(payload)))
			if response.Code != http.StatusOK {
				t.Fatalf("memory save failed: %d %s", response.Code, response.Body.String())
			}
			var saved memory.MemoryEntry
			if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
			reopened, err := memory.New(memory.Options{Enabled: true, Dir: dir, MaxMemory: 200})
			if err != nil {
				t.Fatal(err)
			}
			entries := reopened.ParseEntries()
			if len(entries) != 1 || entries[0].Content != content || entries[0].ID != saved.ID || !entries[0].Scope.Equal(want) {
				t.Fatalf("API changed the wording, scope or persisted identity: %+v", entries)
			}
		})
	}
}
